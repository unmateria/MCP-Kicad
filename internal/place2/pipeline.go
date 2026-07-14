// Package place2 is the next-generation placement+routing pipeline that will
// progressively replace internal/layout and internal/router.
//
// Pipeline phases (P1..P8):
//
//	P1 parse + intent capture
//	P2 cluster_components   (functional groups: decoupling, pull-ups, LC, ...)
//	P3 rule_layer           (power top, GND bottom, signal flow L→R, rotation)
//	P4 elk_layout           (Sugiyama+ports via elkjs subprocess; Go fallback)
//	P5 snap+rotate          (1.27 mm grid, final rotation)
//	P6 route_libavoid       (orthogonal visibility graph; A*++ fallback)
//	P7 decorate             (junctions, labels, rails)
//	P8 write KiCad sexp
//
// Each phase has a clear contract so individual steps can be evolved
// independently and benchmarked with internal/place2/metrics.
//
// Phase A delivers only the type skeleton + a passthrough Run() so callers
// can wire the pipeline into existing tools without committing to any new
// algorithm yet. Subsequent phases fill in the real implementations.
package place2

import (
	"mcp-kicad/internal/place2/cluster"
	_ "mcp-kicad/internal/place2/cluster/canonical" // register extra detectors
	"mcp-kicad/internal/place2/rules"
	"mcp-kicad/internal/sexp"
)

// IntentNet captures the user's original (pre-tracing) signal-flow ordering
// for one net, as supplied by connect_netlist. The first ref is treated as
// the upstream source by signal-flow rules and any DAG layouts that follow.
type IntentNet struct {
	Name string
	// Refs are reference designators (NOT REF.pin) in upstream→downstream order.
	Refs []string
}

// Cluster is a set of references that should be visually grouped because
// they form a functional unit (decoupling cap + IC, pull-up resistor + signal,
// LC filter, crystal + IC, voltage divider, ...).
//
// Clusters are an ELK compound-node hint and a routing locality hint; they
// never affect electrical correctness.
type Cluster struct {
	// Kind is the rule pattern that produced this cluster — e.g. "decoupling",
	// "pullup", "lc_filter", "crystal", "voltage_divider", "header",
	// "opamp_feedback".
	Kind string
	// Refs are member references (≥ 2). The first ref is the cluster's anchor
	// (typically the IC for decoupling; the signal pin owner for pullups).
	Refs []string
	// Anchor is the rule-significant ref the others orbit around. Equal to
	// Refs[0] for canonical clusters.
	Anchor string
}

// Options controls which phases of the pipeline run.
//
// Default = full pipeline. Setters are exposed mainly for tests, fallback
// paths, and the relayout MCP tool's `algorithm` parameter.
type Options struct {
	// ApplyClustering enables P2.
	ApplyClustering bool
	// ApplyRules enables P3 (power top, GND bottom, signal flow, rotations).
	ApplyRules bool
	// LayoutAlgorithm is one of "elk" (default), "elk_force", "fallback".
	LayoutAlgorithm string
	// SnapGrid forces a final 1.27 mm snap after the layout phase.
	SnapGrid bool
	// Route enables P6.
	Route bool
}

// DefaultOptions returns the full-pipeline default.
func DefaultOptions() Options {
	return Options{
		ApplyClustering: true,
		ApplyRules:      true,
		LayoutAlgorithm: "elk",
		SnapGrid:        true,
		Route:           true,
	}
}

// PlacementResult carries every artefact produced by Run().
//
// Positions is keyed by "REF" (single-unit) or "REF#unit" (multi-unit ICs)
// and gives the schematic-coordinate centre of each symbol after layout.
//
// Rotations is keyed identically and stores the desired final rotation in
// CCW degrees; absent entries mean "leave rotation unchanged".
//
// Clusters echoes the P2 clusters so callers can persist them or surface
// them through the cluster_components MCP tool without re-running detection.
//
// Notes is a free-form audit trail useful in tests and the relayout response.
type PlacementResult struct {
	Positions map[string][2]float64
	Rotations map[string]float64
	Clusters  []Cluster
	Notes     []string
}

// AddNote appends a one-line audit message.
func (p *PlacementResult) AddNote(msg string) {
	p.Notes = append(p.Notes, msg)
}

// Pipeline orchestrates the full P1..P8 sequence. It is stateless apart from
// configuration fields injected at construction time, so a single instance is
// safe to share across requests.
type Pipeline struct {
	Opts Options
	// Logger is invoked once per phase with a short status line. Nil is fine.
	Logger func(phase, msg string)
}

// New builds a Pipeline with the given options. Use DefaultOptions() to get
// the production defaults.
func New(opts Options) *Pipeline {
	return &Pipeline{Opts: opts}
}

// Run executes the placement portion of the pipeline (P1..P5) on `sch` using
// the supplied intent. Routing (P6+) is currently a no-op stub and will be
// filled in during Phase D.
//
// Phase A is a pure passthrough: clustering returns no clusters, rules apply
// nothing, and layout is whatever the schematic already had. The function
// nonetheless walks the AST and populates a PlacementResult so the caller
// surface stays stable — subsequent phases just plug in real implementations.
//
// Important: the function does NOT mutate `sch`. Apply the returned positions
// via sexp.Schematic.MoveSymbol / SetSymbolRotation when you want the changes
// committed to the AST.
func (p *Pipeline) Run(sch *sexp.Schematic, intent []IntentNet) PlacementResult {
	res := PlacementResult{
		Positions: make(map[string][2]float64),
		Rotations: make(map[string]float64),
	}

	syms := sexp.ReadSymbols(sch)
	for _, s := range syms {
		key := s.Reference
		if s.Unit > 1 {
			key = symKey(s.Reference, s.Unit)
		}
		res.Positions[key] = [2]float64{s.X, s.Y}
	}

	if p.Logger != nil {
		p.Logger("P1", "parsed schematic, captured intent")
	}

	nets := sexp.TraceNets(sch)

	// P2 — clustering.
	if p.Opts.ApplyClustering {
		cs := cluster.Detect(syms, nets)
		for _, c := range cs {
			res.Clusters = append(res.Clusters, Cluster{Kind: c.Kind, Refs: c.Refs, Anchor: c.Anchor})
		}
		if p.Logger != nil {
			p.Logger("P2", formatClusterCount(cs))
		}
	}

	// P4 — ELK layout. When the algorithm is "elk" and Node+elkjs are
	// available, use them; otherwise fall through to the legacy positions
	// already in res.Positions and let ApplyClusterPull do the heavy lifting.
	if p.Opts.LayoutAlgorithm == "elk" || p.Opts.LayoutAlgorithm == "" {
		if elkPositions, ok := runELKLayout(syms, nets, res.Clusters); ok {
			for k, v := range elkPositions {
				res.Positions[k] = v
			}
			if p.Logger != nil {
				p.Logger("P4", "ELK layout applied")
			}
		} else {
			if p.Logger != nil {
				p.Logger("P4", "ELK unavailable — fallback to position passthrough")
			}
		}
	}

	// P2.5 — pull cluster satellites toward their anchor BEFORE the rules
	// pass operates on positions. Without this step every cap/Pull-up sits
	// in whatever PlaceFlow column it landed in, regardless of which IC it
	// belongs to.
	if p.Opts.ApplyClustering {
		ApplyClusterPull(syms, res.Clusters, res.Positions)
	}

	// P3 — rules: power rails, bus alignment, signal flow, rotations.
	if p.Opts.ApplyRules {
		// Apply power-rail vertical convention first; bus alignment second so
		// it operates on already-positioned power symbols.
		moved := rules.ApplyPowerRails(syms, res.Positions)
		moved += rules.ApplyBusAlignment(syms, res.Positions, 4)
		moved += rules.ApplySignalFlow(syms, nets, res.Positions)
		for k, v := range rules.ApplyRotations(syms, nets, res.Positions) {
			res.Rotations[k] = v
		}
		if p.Logger != nil {
			p.Logger("P3", "rules applied")
		}
		_ = moved
	}

	// P4 — ELK layout (Phase C will fill this in).
	if p.Logger != nil {
		p.Logger("P4", "layout disabled (Phase C)")
	}

	// P5 — snap (Phase C/E).
	if p.Opts.SnapGrid {
		for k, pos := range res.Positions {
			res.Positions[k] = [2]float64{
				sexp.SnapGrid(pos[0]),
				sexp.SnapGrid(pos[1]),
			}
		}
	}

	res.AddNote("place2.Pipeline: cluster + rules wired (Phase B)")
	return res
}

func formatClusterCount(cs []cluster.Cluster) string {
	if len(cs) == 0 {
		return "no clusters detected"
	}
	by := make(map[string]int)
	for _, c := range cs {
		by[c.Kind]++
	}
	var parts []string
	for k, n := range by {
		parts = append(parts, k+":"+itoa(n))
	}
	return "clusters: " + joinComma(parts)
}

func joinComma(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += ", " + p
	}
	return out
}

// AutoPlace returns the position assigned to a single symbol by a fresh run
// of Run(). Used by add_symbol(auto_place=true) to replace the legacy
// autoPlacePosition heuristic without forcing every caller to assemble the
// full intent slice. Callers that already have a result from Run() should
// look up the position directly.
func (p *Pipeline) AutoPlace(_ sexp.SchematicSymbol, sch *sexp.Schematic) (x, y float64) {
	res := p.Run(sch, nil)
	// Phase A: until rules and ELK arrive, fall back to the same column-fill
	// heuristic the legacy code uses. The full pipeline replaces this in B/C.
	syms := sexp.ReadSymbols(sch)
	x, y = legacyAutoPlace(syms)
	_ = res
	return
}

// symKey builds the stable position-map key for one unit of a multi-unit IC.
func symKey(ref string, unit int) string {
	if unit <= 1 {
		return ref
	}
	return ref + "#" + itoa(unit)
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
