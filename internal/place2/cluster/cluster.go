// Package cluster groups schematic symbols into functional units
// (decoupling caps adjacent to their IC, I²C pull-ups on SDA/SCL, LC filters,
// crystal+caps, voltage dividers, header connectors, op-amp feedback Rs).
//
// Clusters are a layout HINT only: they make the layout phase keep related
// parts physically together (compound nodes in ELK; locality bias in the
// fallback). They never change electrical connectivity or net assignments.
//
// Detection runs over the symbol+net snapshot returned by sexp.ReadSymbols /
// sexp.TraceNets and applies the patterns in rules.go in priority order.
// First match wins per (kind, anchor) pair so a cap can't be in two clusters.
package cluster

import (
	"strings"

	"mcp-kicad/internal/sexp"
)

// ExtraRule is the public hook for canonical (non-core) detectors that live
// in cluster/canonical/. They are registered via RegisterExtra and run after
// the priority list so they only claim refs the core didn't take.
type ExtraRule struct {
	name   string
	detect func(syms []sexp.SchematicSymbol, nets []sexp.Net) []Cluster
}

// extraRules is appended to by RegisterExtra at init time.
var extraRules []ExtraRule

// RegisterExtra appends a canonical detector to the rule list. Safe to call
// from package-level init() in cluster/canonical or any other extension.
func RegisterExtra(name string, detect func(syms []sexp.SchematicSymbol, nets []sexp.Net) []Cluster) {
	extraRules = append(extraRules, ExtraRule{name: name, detect: detect})
}

// Cluster mirrors place2.Cluster — kept here so the rule code can stay
// importable from place2 without an import cycle.
type Cluster struct {
	Kind   string
	Refs   []string
	Anchor string
	// Confidence in [0,1]; higher wins when two patterns claim the same
	// satellite. Detectors set this when their match is unusually strong
	// (exact pin-name match, ≥3 corroborating signals, …). Zero means
	// "default priority — fall back to rulesByPriority order".
	Confidence float64
}

// Detect runs every rule in rules.go over the supplied snapshot and returns
// a stable, deduplicated cluster list. A single ref appears in at most one
// cluster (priority order: decoupling > pullup > lc_filter > crystal >
// voltage_divider > opamp_feedback > header). Canonical extra detectors
// (registered via RegisterExtra) run after the core list so they fill gaps
// without overriding established matches.
func Detect(syms []sexp.SchematicSymbol, nets []sexp.Net) []Cluster {
	if len(syms) == 0 {
		return nil
	}
	ctx := newContext(syms, nets)
	var clusters []Cluster
	used := make(map[string]bool) // refs already inside a cluster

	allRules := append([]rule{}, rulesByPriority...)
	for _, ex := range extraRules {
		fn := ex
		allRules = append(allRules, rule{
			name: fn.name,
			detect: func(_ *detectionContext) []Cluster {
				return fn.detect(syms, nets)
			},
		})
	}

	for _, rule := range allRules {
		for _, c := range rule.detect(ctx) {
			// Anchors can be shared across cluster kinds — an IC may both have
			// decoupling caps AND be the owner of pull-up resistors. Members
			// (the satellites) are still single-claim.
			var fresh []string
			anchorIncluded := false
			for _, r := range c.Refs {
				if r == c.Anchor {
					fresh = append(fresh, r)
					anchorIncluded = true
					continue
				}
				if !used[r] {
					fresh = append(fresh, r)
				}
			}
			// Need at least one satellite besides the anchor, otherwise the
			// cluster is empty.
			satellites := len(fresh)
			if anchorIncluded {
				satellites--
			}
			if satellites < 1 {
				continue
			}
			c.Refs = fresh
			for _, r := range fresh {
				if r != c.Anchor {
					used[r] = true
				}
			}
			clusters = append(clusters, c)
		}
	}
	return clusters
}

// detectionContext caches indices used by every rule so they don't recompute
// the same maps repeatedly.
//
// Multi-unit ICs are indexed both by bare reference (returns canonical unit 1)
// AND by "REF#unit" key. Per-unit nets are tracked in netsByRefUnit so a
// decoupling cap landing on the power-unit's V+/V- is detected against the
// CORRECT unit, not aggregated across all units of the IC (which would mask
// which unit electrically needs the bypass).
type detectionContext struct {
	symByRef      map[string]sexp.SchematicSymbol // bare ref → unit 1 (canonical)
	symByRefUnit  map[string]sexp.SchematicSymbol // "REF#unit" → that unit
	unitsByRef    map[string][]int                // ref → sorted unit numbers (≥1)
	netsByRef     map[string][]sexp.Net           // ref → nets across all units
	netsByRefUnit map[string][]sexp.Net           // "REF#unit" → nets that unit touches
	netByName     map[string]sexp.Net
	powerLibIDs   map[string]bool // every ref whose lib_id starts with power:
	// refOrder is the deterministic iteration order over symByRef. Detectors
	// MUST range over this (not the map directly) so cluster order and
	// satellite order are identical across runs — Go randomises map iteration.
	refOrder []string
}

func newContext(syms []sexp.SchematicSymbol, nets []sexp.Net) *detectionContext {
	ctx := &detectionContext{
		symByRef:      make(map[string]sexp.SchematicSymbol, len(syms)),
		symByRefUnit:  make(map[string]sexp.SchematicSymbol, len(syms)),
		unitsByRef:    make(map[string][]int),
		netsByRef:     make(map[string][]sexp.Net),
		netsByRefUnit: make(map[string][]sexp.Net),
		netByName:     make(map[string]sexp.Net, len(nets)),
		powerLibIDs:   make(map[string]bool),
	}
	for _, s := range syms {
		u := s.Unit
		if u <= 0 {
			u = 1
		}
		// First-occurrence-wins for bare-ref (canonical unit, usually 1).
		if _, ok := ctx.symByRef[s.Reference]; !ok {
			ctx.symByRef[s.Reference] = s
			ctx.refOrder = append(ctx.refOrder, s.Reference) // deterministic order
		}
		ctx.symByRefUnit[unitKey(s.Reference, u)] = s
		ctx.unitsByRef[s.Reference] = appendUnique(ctx.unitsByRef[s.Reference], u)
		if strings.HasPrefix(s.LibID, "power:") || s.LibID == "Device:PWR_FLAG" {
			ctx.powerLibIDs[s.Reference] = true
		}
	}
	for ref, units := range ctx.unitsByRef {
		sortInts(units)
		ctx.unitsByRef[ref] = units
	}
	seenAgg := make(map[[2]string]bool)
	seenUnit := make(map[[2]string]bool)
	for _, n := range nets {
		ctx.netByName[n.Name] = n
		for _, p := range n.Pins {
			aggKey := [2]string{n.Name, p.Reference}
			if !seenAgg[aggKey] {
				seenAgg[aggKey] = true
				ctx.netsByRef[p.Reference] = append(ctx.netsByRef[p.Reference], n)
			}
			u := p.Unit
			if u <= 0 {
				u = 1
			}
			ukey := unitKey(p.Reference, u)
			unitNetKey := [2]string{n.Name, ukey}
			if !seenUnit[unitNetKey] {
				seenUnit[unitNetKey] = true
				ctx.netsByRefUnit[ukey] = append(ctx.netsByRefUnit[ukey], n)
			}
		}
	}
	return ctx
}

// unitKey builds the canonical "REF#unit" key for indexing symByRefUnit /
// netsByRefUnit. Always uses "#1" for unit 1 so the key shape stays uniform.
// Use anchorKey for cluster anchors (which omit #1 to keep single-unit
// schematics backward-compatible).
func unitKey(ref string, unit int) string {
	if unit <= 1 {
		return ref + "#1"
	}
	return ref + "#" + itoa(unit)
}

// anchorKey returns the cluster-anchor encoding: bare "REF" when the IC has
// only one unit, "REF#unit" when there's more than one unit (so layout/
// downstream code can look up the specific power-unit position).
func anchorKey(ctx *detectionContext, ref string, unit int) string {
	if unit <= 1 || len(ctx.unitsByRef[ref]) <= 1 {
		return ref
	}
	return ref + "#" + itoa(unit)
}

// SplitAnchor splits "REF#unit" into (ref, unit). Returns unit=1 when the
// input has no "#" suffix (single-unit symbols).
func SplitAnchor(s string) (ref string, unit int) {
	if idx := strings.Index(s, "#"); idx >= 0 {
		ref = s[:idx]
		unit = 1
		for _, c := range s[idx+1:] {
			if c < '0' || c > '9' {
				unit = 1
				break
			}
			unit = unit*10 + int(c-'0')
		}
		if unit < 1 {
			unit = 1
		}
		return ref, unit
	}
	return s, 1
}

func appendUnique(s []int, v int) []int {
	for _, e := range s {
		if e == v {
			return s
		}
	}
	return append(s, v)
}

func sortInts(s []int) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	if n < 0 {
		return "-" + itoa(-n)
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// nonPowerPinRefs returns the references of all non-power components with at
// least one pin on `net`. Order matches the net's pin order so signal-flow
// hints survive.
func (ctx *detectionContext) nonPowerPinRefs(net sexp.Net) []string {
	seen := make(map[string]bool)
	var refs []string
	for _, p := range net.Pins {
		if seen[p.Reference] || ctx.powerLibIDs[p.Reference] {
			continue
		}
		seen[p.Reference] = true
		refs = append(refs, p.Reference)
	}
	return refs
}
