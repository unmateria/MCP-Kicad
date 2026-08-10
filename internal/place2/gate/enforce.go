package gate

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"mcp-kicad/internal/place2/metrics"
	"mcp-kicad/internal/sexp"
)

// DemotedNet records one net that Enforce demoted to labels.
type DemotedNet struct {
	Name         string // net name (post-rename, i.e. the label text actually written)
	Reason       string // human-readable cause, e.g. "crossed GND"
	WireSegments int    // wire segments removed
}

// EnforceResult summarizes what Enforce did.
type EnforceResult struct {
	Demoted    []DemotedNet
	Repaired   int // wires cut so a net's own crossing became a junction
	Violations int // remaining violations after the loop (0 unless maxIterations was hit)
}

// repairSelfViolations fixes the defects a net has against itself by cutting
// the wire at the offending point, so the point becomes wire ENDS and
// EnsureJunctions can dot it. Returns how many wires were cut.
//
// Only same-net defects qualify. A crossing between two nets is a short and a
// junction there would cement it; a wire over a FOREIGN pin is the silent short
// this repository exists to prevent. Both still get demoted.
func repairSelfViolations(sch *sexp.Schematic, violations []Violation) int {
	cut := 0
	for _, v := range violations {
		switch v.Kind {
		case SameNetNoJunction:
		case WireOverPin:
			// Net2 is the pin's net: only its OWN pin may be joined this way.
			if v.Net2 != v.Net {
				continue
			}
		default:
			continue
		}
		if n := sexp.SplitWiresAt(sch, v.X, v.Y); n > 0 {
			cut += n
			sch.AddJunction(sexp.NewJunction(v.X, v.Y))
		}
	}
	return cut
}

// String renders a one-line summary suitable for appending to a tool's text
// output, e.g. "gate: demoted 2 net(s) to labels (VOUT: crossed GND; LED_NODE: wire through R1 body)".
func (r EnforceResult) String() string {
	if len(r.Demoted) == 0 {
		return "gate: clean — 0 nets demoted"
	}
	parts := make([]string, len(r.Demoted))
	for i, d := range r.Demoted {
		parts[i] = fmt.Sprintf("%s: %s", d.Name, d.Reason)
	}
	return fmt.Sprintf("gate: demoted %d net(s) to labels (%s)", len(r.Demoted), strings.Join(parts, "; "))
}

// maxEnforceIterations bounds the demotion loop. Each iteration strictly
// removes wire segments from a real (finite) schematic, so this is a safety
// valve against an unforeseen bug, not something normal operation should hit.
const maxEnforceIterations = 1000

// Enforce repeatedly cleans and checks sch, demoting the worst-offending net
// to labels whenever a geometric violation is found, until zero violations
// remain. See the package doc for the termination argument.
func Enforce(sch *sexp.Schematic) EnforceResult {
	var result EnforceResult
	for i := 0; i < maxEnforceIterations; i++ {
		Clean(sch)
		violations := Check(sch)
		if len(violations) == 0 {
			result.Violations = 0
			return result
		}
		// Repair before demoting. A net that offends only against ITSELF is not
		// a short and never was: cutting the wire at the point and letting
		// EnsureJunctions dot it says exactly what is true. Demoting it instead
		// deletes real wires to buy a pair of tags, which is how a verified
		// schematic ends up reading as unwired.
		if n := repairSelfViolations(sch, violations); n > 0 {
			result.Repaired += n
			continue
		}
		netName, reason := pickWorstNet(sch, violations)
		if netName == "" {
			break // defensive — should be unreachable since violations always name a net
		}
		removed := demoteNet(sch, netName)
		result.Demoted = append(result.Demoted, DemotedNet{Name: netName, Reason: reason, WireSegments: removed})
	}
	Clean(sch)
	result.Violations = len(Check(sch))
	return result
}

// pickWorstNet picks the net to demote next: the one with the largest total
// wire length among all nets implicated by a violation. Ties break on net
// name, then on the alphabetically-lowest pin ref in the net (both purely
// for determinism across runs).
func pickWorstNet(sch *sexp.Schematic, violations []Violation) (name, reason string) {
	lengths := netLengths(sch)
	reasonFor := make(map[string]string)
	seen := make(map[string]bool)
	var names []string
	for _, v := range violations {
		for _, n := range culprits(v) {
			if n == "" || seen[n] {
				continue
			}
			seen[n] = true
			names = append(names, n)
			reasonFor[n] = violationReason(v, n)
		}
	}
	if len(names) == 0 {
		return "", ""
	}
	pinRef := netLowestPinRef(sch)
	sort.Slice(names, func(i, j int) bool {
		li, lj := lengths[names[i]], lengths[names[j]]
		if li != lj {
			return li > lj
		}
		if names[i] != names[j] {
			return names[i] < names[j]
		}
		return pinRef[names[i]] < pinRef[names[j]]
	})
	return names[0], reasonFor[names[0]]
}

// culprits lists the nets whose demotion could actually clear a violation.
// A crossing or a collinear overlap is symmetric — removing either net's
// wires fixes it. WireOverPin is not: the offending geometry belongs to the
// wire, and the pin it runs over is the victim, so demoting the pin's net
// would leave the wire in place and the violation standing.
func culprits(v Violation) []string {
	switch v.Kind {
	case CrossNetCrossing, CollinearOverlap:
		return []string{v.Net, v.Net2}
	default:
		return []string{v.Net}
	}
}

func violationReason(v Violation, netName string) string {
	other := func() string {
		if netName == v.Net2 {
			return v.Net
		}
		return v.Net2
	}
	switch v.Kind {
	case CrossNetCrossing:
		return fmt.Sprintf("crossed %s", other())
	case CollinearOverlap:
		return fmt.Sprintf("overlapped %s", other())
	case WireThruSymbol:
		return v.Detail
	case SameNetNoJunction:
		return "self-crossing without a junction"
	case WireOverPin:
		return v.Detail
	}
	return string(v.Kind)
}

// netLengths sums the segment length of every wire belonging to each net.
func netLengths(sch *sexp.Schematic) map[string]float64 {
	netOf := sexp.TracePointNets(sch)
	lengths := make(map[string]float64)
	for _, w := range sch.Wires() {
		ax, ay, bx, by, ok := metrics.WireCoords(w)
		if !ok {
			continue
		}
		net := netOf[round2pt(ax, ay)]
		dx, dy := bx-ax, by-ay
		lengths[net] += math.Sqrt(dx*dx + dy*dy)
	}
	return lengths
}

// netLowestPinRef returns, for every net, the alphabetically-lowest pin ref
// string (e.g. "C1.2") — used only as a deterministic tie-break.
func netLowestPinRef(sch *sexp.Schematic) map[string]string {
	out := make(map[string]string)
	for _, n := range sexp.TraceNets(sch) {
		lowest := ""
		for _, p := range n.Pins {
			s := p.String()
			if lowest == "" || s < lowest {
				lowest = s
			}
		}
		out[n.Name] = lowest
	}
	return out
}

// Demote strips a net down to labels on demand: every wire segment and
// orphaned junction goes, every non-power pin gets a label. The gate uses it
// for geometry violations; callers use it for UNIFORMITY — when most of a
// connector's twin nets ended up labeled, the one that routed reads worse
// wired than labeled, and no geometric rule can know that.
func Demote(sch *sexp.Schematic, netName string) int { return demoteNet(sch, netName) }

// demoteNet removes every wire segment and now-orphaned junction belonging
// to netName, then places a net label at every non-power pin of that net.
// Returns the number of wire segments removed.
func demoteNet(sch *sexp.Schematic, netName string) int {
	var pins []sexp.PinRef
	for _, n := range sexp.TraceNets(sch) {
		if n.Name == netName {
			pins = n.Pins
			break
		}
	}

	labelName := demotedLabelName(netName)
	positions := pinPositionsForLabels(sch, pins)

	netOf := sexp.TracePointNets(sch)
	removed := 0
	filtered := sch.Root().Children[:0:0]
	for _, c := range sch.Root().Children {
		if c.Head() == "wire" {
			ax, ay, _, _, ok := metrics.WireCoords(c)
			if ok && netOf[round2pt(ax, ay)] == netName {
				removed++
				continue
			}
		}
		filtered = append(filtered, c)
	}
	sch.Root().Children = filtered

	// Drop junctions that no longer touch at least 2 wire segments now that
	// this net's wires are gone (the simplest correct rule per CLAUDE.md:
	// a junction with fewer than 2 touching segments is meaningless).
	remainingWires := sch.Wires()
	filtered2 := sch.Root().Children[:0:0]
	for _, c := range sch.Root().Children {
		if c.Head() == "junction" {
			atN := sexp.FindList(c, "at")
			if atN != nil {
				x, _ := strconv.ParseFloat(sexp.AtomValue(atN, 1), 64)
				y, _ := strconv.ParseFloat(sexp.AtomValue(atN, 2), 64)
				if countTouchingWires(remainingWires, x, y) < 2 {
					continue
				}
			}
		}
		filtered2 = append(filtered2, c)
	}
	sch.Root().Children = filtered2

	removeLabelsNamed(sch, netName)
	for _, p := range positions {
		sch.AddLabel(sexp.NewNetLabel(labelName, p[0], p[1], 0))
	}

	return removed
}

// pinPositionsForLabels resolves the schematic coordinates of every
// non-power pin in pins, deduplicated by rounded position. Power-symbol pins
// are skipped: a #PWR symbol's own pin coincides with the target component
// pin it was placed on, which is already included via the component side, so
// a #PWR pin never contributes a NEW position.
func pinPositionsForLabels(sch *sexp.Schematic, pins []sexp.PinRef) [][2]float64 {
	allSyms := sexp.ReadSymbols(sch)
	byRef := make(map[string][]sexp.SchematicSymbol, len(allSyms))
	for _, s := range allSyms {
		byRef[s.Reference] = append(byRef[s.Reference], s)
	}
	seen := make(map[[2]float64]bool)
	var positions [][2]float64
	for _, pr := range pins {
		if strings.HasPrefix(pr.LibID, "power:") {
			continue
		}
		for _, sym := range byRef[pr.Reference] {
			if pr.Unit != 0 && sym.Unit != pr.Unit {
				continue
			}
			for _, p := range sym.Pins {
				// Match by pin NUMBER only — pin numbers are unique within a
				// symbol/unit, whereas PinName is often the generic "~"
				// shared by every pin of the part (e.g. Device:R), which
				// would otherwise match every pin instead of just this one.
				if p.Number != pr.PinNumber {
					continue
				}
				key := round2pt(p.X, p.Y)
				if !seen[key] {
					seen[key] = true
					positions = append(positions, [2]float64{p.X, p.Y})
				}
			}
		}
	}
	return positions
}

func countTouchingWires(wires []*sexp.Node, x, y float64) int {
	count := 0
	for _, w := range wires {
		ax, ay, bx, by, ok := metrics.WireCoords(w)
		if !ok {
			continue
		}
		if pointOnSegmentInclusive(x, y, ax, ay, bx, by) {
			count++
		}
	}
	return count
}

// pointOnSegmentInclusive reports whether (px,py) lies on the axis-aligned
// segment [ (ax,ay), (bx,by) ], endpoints included.
func pointOnSegmentInclusive(px, py, ax, ay, bx, by float64) bool {
	if ay-by < eps && by-ay < eps { // horizontal (within tolerance)
		if py-ay < -eps || py-ay > eps {
			return false
		}
		lo, hi := ax, bx
		if lo > hi {
			lo, hi = hi, lo
		}
		return px >= lo-eps && px <= hi+eps
	}
	if ax-bx < eps && bx-ax < eps { // vertical (within tolerance)
		if px-ax < -eps || px-ax > eps {
			return false
		}
		lo, hi := ay, by
		if lo > hi {
			lo, hi = hi, lo
		}
		return py >= lo-eps && py <= hi+eps
	}
	return false
}

func removeLabelsNamed(sch *sexp.Schematic, name string) {
	filtered := sch.Root().Children[:0:0]
	for _, c := range sch.Root().Children {
		if c.Head() == "label" {
			n := sexp.StringValue(c, 1)
			if n == "" {
				n = sexp.AtomValue(c, 1)
			}
			if n == name {
				continue
			}
		}
		filtered = append(filtered, c)
	}
	sch.Root().Children = filtered
}

// demotedLabelName decides the label text for a demoted net: keep an
// existing label/power-rail name as-is, or turn the auto-generated
// "Net-(REF.pin)" form into a stable, readable "NET_REF_pin" identifier.
func demotedLabelName(netName string) string {
	if !strings.HasPrefix(netName, "Net-(") {
		return netName
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(netName, "Net-("), ")")
	return "NET_" + sanitizeIdent(inner)
}

func sanitizeIdent(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+':
			b.WriteRune('P')
		case r == '-':
			b.WriteRune('N')
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	for strings.Contains(out, "__") {
		out = strings.ReplaceAll(out, "__", "_")
	}
	return strings.Trim(out, "_")
}
