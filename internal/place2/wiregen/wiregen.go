// Package wiregen produces schematic wiring for recognised functional clusters
// (decoupling caps, pull-ups, series LEDs, dividers, crystals) from CLOSED-FORM
// geometry — simple arithmetic on pin positions — instead of pathfinding.
//
// A generator either APPLIES (its preconditions hold, so the wire it emits is
// provably clean: straight or a single L that clears every symbol body and
// every wire already generated) or it DECLINES (returns nothing, leaving the
// pins for the A* router + geometric gate to handle). There is no search and
// no backtracking, so the layer is deterministic and can never make geometry
// worse than the router would have.
//
// Determinism: Apply sorts clusters (by anchor ref, then kind) and every
// generator iterates satellites and pins in sorted order, so identical input
// always yields byte-identical output regardless of the map-iteration order
// inside cluster.Detect.
package wiregen

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"mcp-kicad/internal/place2/cluster"
	"mcp-kicad/internal/place2/metrics"
	"mcp-kicad/internal/sexp"
)

// eps is the coordinate tolerance (mm), consistent with sexp's 2-decimal
// rounding and gate's own eps.
const eps = 0.01

// PinInput is one pin of the connection table, fully resolved to schematic
// coordinates. Ref is the ORIGINAL pin string as it appears in the connection
// table (e.g. "U1.27", "C1.1", "U1.1.+") so consumed pairs can be reported in
// exactly the form the router consumes.
type PinInput struct {
	Ref   string  // original connection-table string, e.g. "U1.27"
	Owner string  // symbol reference, e.g. "U1"
	Net   string  // net name this pin belongs to
	X, Y  float64 // schematic coordinates
	Dir   float64 // outgoing wire direction (screen CCW degrees: 0=E,90=N,180=W,270=S)
	LibID string  // owning symbol's lib_id
}

// NetInput is one net of the connection table with its pins resolved.
type NetInput struct {
	Name string
	Pins []PinInput
}

// Move records a satellite symbol repositioned to its canonical spot before
// wiring. Applied to the schematic by Apply itself; also returned for logging.
type Move struct {
	Ref  string
	X, Y float64
}

// Pair is one pin-to-pin connection satisfied by a generator, on the named net.
// A and B are the original connection-table pin strings. The netlist router
// consumes these: pins joined by pairs are treated as already-connected.
type Pair struct {
	Net string
	A   string
	B   string
}

// Result is the combined output of every generator that fired.
type Result struct {
	Wires     []*sexp.Node
	Junctions []*sexp.Node
	Moves     []Move
	Pairs     []Pair
	ByKind    map[string]int // cluster kind -> number of pin-pairs wired
}

// Empty reports whether nothing was generated (no wires and no moves).
func (r *Result) Empty() bool {
	return r == nil || (len(r.Wires) == 0 && len(r.Moves) == 0)
}

// ReportLine renders the one-line tool summary, e.g.
// "wiregen: 5 connection(s) wired by formula (crystal: 2, pullup: 2, series_led: 1)".
func (r *Result) ReportLine() string {
	if r == nil || len(r.Pairs) == 0 {
		return "wiregen: 0 connections wired by formula"
	}
	kinds := make([]string, 0, len(r.ByKind))
	for k := range r.ByKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	parts := make([]string, len(kinds))
	for i, k := range kinds {
		parts[i] = fmt.Sprintf("%s: %d", k, r.ByKind[k])
	}
	return fmt.Sprintf("wiregen: %d connection(s) wired by formula (%s)", len(r.Pairs), strings.Join(parts, ", "))
}

func (r *Result) record(kind string, w []*sexp.Node, junc []*sexp.Node, pairs []Pair) {
	r.Wires = append(r.Wires, w...)
	r.Junctions = append(r.Junctions, junc...)
	r.Pairs = append(r.Pairs, pairs...)
	if r.ByKind == nil {
		r.ByKind = map[string]int{}
	}
	r.ByKind[kind] += len(pairs)
}

// generator is one closed-form wiring pattern. Handles reports which cluster
// kinds it serves; TryWire either emits clean geometry for the cluster (and
// returns true) or declines (returns false, no mutation).
type generator interface {
	Handles(kind string) bool
	TryWire(gc *genCtx) (kindWires, kindJuncs []*sexp.Node, pairs []Pair, ok bool)
}

// registry lists generators in application priority order (decoupling first,
// per the design brief). A cluster is dispatched to the first generator that
// Handles its kind.
var registry = []generator{
	decouplingGen{},
	twoPinGen{},
	dividerGen{},
	crystalGen{},
}

// Apply runs the generators with repositioning enabled. It is the entry point
// for callers that own the final placement (and for the tests that exercise the
// satellite-snug lever). The connect_netlist pipeline uses ApplyOpts(false).
func Apply(sch *sexp.Schematic, clusters []cluster.Cluster, nets []NetInput) *Result {
	return ApplyOpts(sch, clusters, nets, true)
}

// ApplyOpts runs every generator over the detected clusters and mutates sch in
// place: it (optionally) repositions satellites (sch.MoveSymbol), adds the
// generated wires and junctions, and returns a Result describing what was
// consumed. Clusters are processed in a deterministic order and later
// generators see the wires and moves of earlier ones (via the shared genCtx
// occupancy state).
//
// allowMoves gates repositioning. The connect_netlist integration passes false:
// a moved satellite's new position would survive into the downstream relayout
// (which re-places everything) and perturb it, so in the pipeline wiregen only
// wires clusters that are ALREADY adjacent. Repositioning stays available (and
// tested) for callers that own the final placement.
func ApplyOpts(sch *sexp.Schematic, clusters []cluster.Cluster, nets []NetInput, allowMoves bool) *Result {
	res := &Result{ByKind: map[string]int{}}
	if len(clusters) == 0 || len(nets) == 0 {
		return res
	}
	st := newState(sch, nets)
	st.allowMoves = allowMoves

	// Deterministic cluster order: anchor ref, then kind.
	ordered := append([]cluster.Cluster(nil), clusters...)
	sort.SliceStable(ordered, func(i, j int) bool {
		ai, _ := cluster.SplitAnchor(ordered[i].Anchor)
		aj, _ := cluster.SplitAnchor(ordered[j].Anchor)
		if ai != aj {
			return ai < aj
		}
		return ordered[i].Kind < ordered[j].Kind
	})

	for _, c := range ordered {
		var gen generator
		for _, g := range registry {
			if g.Handles(c.Kind) {
				gen = g
				break
			}
		}
		if gen == nil {
			continue
		}
		gc := &genCtx{state: st, cluster: c}
		w, junc, pairs, ok := gen.TryWire(gc)
		if !ok || len(w) == 0 {
			continue
		}
		for _, n := range w {
			sch.AddWire(n)
		}
		for _, n := range junc {
			sch.AddJunction(n)
		}
		res.record(c.Kind, w, junc, pairs)
		res.Moves = append(res.Moves, gc.moves...)
		// Wires were already committed to occupancy by the generator as each
		// connection was built (needed for intra-cluster corridor checks); no
		// second commit here.
	}
	return res
}

// state holds the shared, mutable index used by every generator: pin lookup,
// live symbol geometry, and the running set of generated segments.
type state struct {
	sch         *sexp.Schematic
	pinsByOwner map[string][]*PinInput
	byRef       map[string]*PinInput // original ref string -> pin
	bodies      map[string][4]float64
	center      map[string][2]float64
	isPower     map[string]bool
	locked      map[string]bool // owner already has a wire attached — must not move
	segs        [][4]float64    // generated segments (ax,ay,bx,by)
	allowMoves  bool            // repositioning permitted
}

func newState(sch *sexp.Schematic, nets []NetInput) *state {
	st := &state{
		sch:         sch,
		pinsByOwner: map[string][]*PinInput{},
		byRef:       map[string]*PinInput{},
		bodies:      map[string][4]float64{},
		center:      map[string][2]float64{},
		isPower:     map[string]bool{},
		locked:      map[string]bool{},
	}
	for i := range nets {
		for j := range nets[i].Pins {
			p := &nets[i].Pins[j]
			pc := *p // copy so caller's slice is never mutated
			st.pinsByOwner[pc.Owner] = append(st.pinsByOwner[pc.Owner], &pc)
			st.byRef[pc.Ref] = &pc
		}
	}
	for _, sym := range sexp.ReadSymbols(sch) {
		if strings.HasPrefix(sym.LibID, "power:") || sym.LibID == "Device:PWR_FLAG" {
			st.isPower[sym.Reference] = true
			continue
		}
		x1, y1, x2, y2 := metrics.BodyBBox(sym)
		st.bodies[sym.Reference] = [4]float64{x1, y1, x2, y2}
		st.center[sym.Reference] = [2]float64{sym.X, sym.Y}
	}
	// Lock owners that already touch an existing wire endpoint — they cannot be
	// repositioned without dragging live geometry.
	for _, w := range sch.Wires() {
		ax, ay, bx, by, ok := metrics.WireCoords(w)
		if !ok {
			continue
		}
		st.segs = append(st.segs, [4]float64{ax, ay, bx, by})
		for owner, pins := range st.pinsByOwner {
			for _, p := range pins {
				if (near(p.X, ax) && near(p.Y, ay)) || (near(p.X, bx) && near(p.Y, by)) {
					st.locked[owner] = true
				}
			}
		}
	}
	return st
}

// commit registers freshly emitted wires as occupancy so later generators
// route around them, and locks their endpoint owners against repositioning.
func (st *state) commit(wires []*sexp.Node) {
	for _, w := range wires {
		ax, ay, bx, by, ok := metrics.WireCoords(w)
		if !ok {
			continue
		}
		st.segs = append(st.segs, [4]float64{ax, ay, bx, by})
	}
}

// segClear reports whether the axis-aligned segment (ax,ay)->(bx,by) avoids
// every non-power symbol body interior AND does not collinearly overlap any
// previously generated segment. Endpoints sit at pin tips, which are outside
// the inset BodyBBox by construction, so a wire leaving a pin outward is clear;
// a wire heading into a body is rejected.
func (st *state) segClear(ax, ay, bx, by float64) bool {
	for _, b := range st.bodies {
		if sexp.SegmentCrossesBox(ax, ay, bx, by, b[0], b[1], b[2], b[3]) {
			return false
		}
	}
	for _, s := range st.segs {
		if collinearOverlap(ax, ay, bx, by, s[0], s[1], s[2], s[3]) {
			return false
		}
	}
	return true
}

// segClearExcept is segClear but skips the named owner's body — used when that
// owner is about to be relocated so its current position is irrelevant.
func (st *state) segClearExcept(owner string, ax, ay, bx, by float64) bool {
	for ref, b := range st.bodies {
		if ref == owner {
			continue
		}
		if sexp.SegmentCrossesBox(ax, ay, bx, by, b[0], b[1], b[2], b[3]) {
			return false
		}
	}
	for _, s := range st.segs {
		if collinearOverlap(ax, ay, bx, by, s[0], s[1], s[2], s[3]) {
			return false
		}
	}
	return true
}

// bodyClearAt reports whether placing owner's body at the given bbox would
// avoid every OTHER symbol body (inflated by margin) and every generated
// segment. Used to validate a reposition destination.
func (st *state) destClear(owner string, x1, y1, x2, y2 float64) bool {
	const margin = 1.27
	x1 -= margin
	y1 -= margin
	x2 += margin
	y2 += margin
	for ref, b := range st.bodies {
		if ref == owner {
			continue
		}
		if boxesOverlap(x1, y1, x2, y2, b[0], b[1], b[2], b[3]) {
			return false
		}
	}
	for _, s := range st.segs {
		if sexp.SegmentCrossesBox(s[0], s[1], s[2], s[3], x1, y1, x2, y2) {
			return false
		}
	}
	return true
}

// moveSatellite repositions owner so that its anchor pin (pivotRef) lands on
// target (tx,ty). Only succeeds when owner is unlocked (no wires yet) and the
// destination body bbox is clear. On success it mutates the schematic, updates
// every index for owner, records the Move on gc, and returns true.
func (gc *genCtx) moveSatellite(owner, pivotRef string, tx, ty float64) bool {
	st := gc.state
	if !st.allowMoves || st.locked[owner] {
		return false
	}
	pivot := st.byRef[pivotRef]
	if pivot == nil || pivot.Owner != owner {
		return false
	}
	c, ok := st.center[owner]
	if !ok {
		return false
	}
	dx := tx - pivot.X
	dy := ty - pivot.Y
	if math.Abs(dx) < eps && math.Abs(dy) < eps {
		return true // already there
	}
	newCX := sexp.SnapGrid(c[0] + dx)
	newCY := sexp.SnapGrid(c[1] + dy)
	dx = newCX - c[0]
	dy = newCY - c[1]
	b := st.bodies[owner]
	if !st.destClear(owner, b[0]+dx, b[1]+dy, b[2]+dx, b[3]+dy) {
		return false
	}
	if st.sch.MoveSymbol(owner, newCX, newCY) == 0 {
		return false
	}
	// Re-read the moved symbol from the schematic so pin coordinates match the
	// serialized file exactly (MoveSymbol snaps + rewrites the AST).
	for _, sym := range sexp.ReadSymbols(st.sch) {
		if sym.Reference != owner {
			continue
		}
		st.center[owner] = [2]float64{sym.X, sym.Y}
		nx1, ny1, nx2, ny2 := metrics.BodyBBox(sym)
		st.bodies[owner] = [4]float64{nx1, ny1, nx2, ny2}
		for _, sp := range sym.Pins {
			for _, p := range st.pinsByOwner[owner] {
				if p.Owner == owner && pinMatches(p.Ref, owner, sp) {
					p.X = sexp.Round2(sp.X)
					p.Y = sexp.Round2(sp.Y)
					p.Dir = sp.Direction
				}
			}
		}
		break
	}
	gc.moves = append(gc.moves, Move{Ref: owner, X: newCX, Y: newCY})
	return true
}

// pinMatches reports whether the connection-table pin string ref (e.g.
// "C1.1" or "U1.2.+") identifies the resolved symbol pin sp of owner.
func pinMatches(ref, owner string, sp sexp.PinInfo) bool {
	rest := strings.TrimPrefix(ref, owner+".")
	// Drop an optional numeric unit prefix ("2." in "U1.2.+").
	if idx := strings.Index(rest, "."); idx >= 0 {
		if _, err := strconv.Atoi(rest[:idx]); err == nil {
			rest = rest[idx+1:]
		}
	}
	return rest == sp.Number || rest == sp.Name
}

// maxSpan is the longest Manhattan distance (mm) a formula wire may span at the
// pins' CURRENT positions. Beyond it the pins are not a real local cluster, so
// the generator first tries to snug the satellite adjacent (short wire); only
// if that fails does it decline. This guarantees the formula layer never draws
// a wire longer than the router would, so total wire length can only improve.
const maxSpan = 38.1 // 30 * 1.27 mm — cluster-adjacency bound for a formula wire; beyond it the router (and, in placement-owning callers, repositioning) takes over

// genCtx is the per-cluster handle passed to a generator.
type genCtx struct {
	state   *state
	cluster cluster.Cluster
	moves   []Move
}

// wireOne connects an anchor pin to a satellite pin with the short-wire
// guarantee: a direct clean wire when the pins are already close, otherwise a
// reposition that snugs the satellite next to the anchor pin. Returns the wire
// nodes and true, or declines (nil,false) leaving the pins to the router.
func (gc *genCtx) wireOne(sat string, aPin, sPin *PinInput) ([]*sexp.Node, bool) {
	if manhattan(aPin, sPin) <= maxSpan {
		if w, ok := gc.connect(aPin, sPin); ok {
			return w, true
		}
	}
	return gc.alignAndConnect(sat, aPin, sPin)
}

func manhattan(a, b *PinInput) float64 {
	return math.Abs(a.X-b.X) + math.Abs(a.Y-b.Y)
}

// anchorRef returns the cluster anchor with any unit suffix stripped.
func (gc *genCtx) anchorRef() string {
	ref, _ := cluster.SplitAnchor(gc.cluster.Anchor)
	return ref
}

// satellites returns the cluster's satellite refs (everything except the
// anchor), sorted for determinism.
func (gc *genCtx) satellites() []string {
	anchor := gc.cluster.Anchor
	bare := gc.anchorRef()
	var out []string
	for _, r := range gc.cluster.Refs {
		if r == anchor || r == bare {
			continue
		}
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// pinsOf returns owner's pins sorted by ref for determinism.
func (gc *genCtx) pinsOf(owner string) []*PinInput {
	ps := append([]*PinInput(nil), gc.state.pinsByOwner[owner]...)
	sort.Slice(ps, func(i, j int) bool { return ps[i].Ref < ps[j].Ref })
	return ps
}

// connect builds a clean wire from pin a to pin b: a straight segment when the
// pins share an axis, otherwise a single L. Preference among the two L corners
// goes to the one whose first segment leaves a in a's outgoing pin direction.
// Returns the wire nodes (and a junction node when b is a mid-point of an
// existing generated segment is NOT handled here — callers add taps). ok is
// false when neither form clears the corridor.
func (gc *genCtx) connect(a, b *PinInput) ([]*sexp.Node, bool) {
	st := gc.state
	ax, ay, bx, by := a.X, a.Y, b.X, b.Y
	if near(ax, bx) || near(ay, by) {
		if st.segClear(ax, ay, bx, by) {
			return []*sexp.Node{sexp.NewWire(ax, ay, bx, by)}, true
		}
		return nil, false
	}
	corners := [][2]float64{{bx, ay}, {ax, by}}
	// Prefer the corner whose first segment (a->corner) matches a.Dir.
	if !firstSegMatchesDir(ax, ay, corners[0][0], corners[0][1], a.Dir) &&
		firstSegMatchesDir(ax, ay, corners[1][0], corners[1][1], a.Dir) {
		corners[0], corners[1] = corners[1], corners[0]
	}
	for _, c := range corners {
		if st.segClear(ax, ay, c[0], c[1]) && st.segClear(c[0], c[1], bx, by) {
			return []*sexp.Node{
				sexp.NewWire(ax, ay, c[0], c[1]),
				sexp.NewWire(c[0], c[1], bx, by),
			}, true
		}
	}
	return nil, false
}

// --- geometry helpers ---------------------------------------------------

func near(a, b float64) bool { return math.Abs(a-b) < eps }

func firstSegMatchesDir(ax, ay, cx, cy, dir float64) bool {
	switch int(math.Round(dir)) % 360 {
	case 0:
		return cx > ax+eps && near(cy, ay)
	case 180:
		return cx < ax-eps && near(cy, ay)
	case 90:
		return cy < ay-eps && near(cx, ax)
	case 270:
		return cy > ay+eps && near(cx, ax)
	}
	return false
}

func boxesOverlap(ax1, ay1, ax2, ay2, bx1, by1, bx2, by2 float64) bool {
	if ax1 > ax2 {
		ax1, ax2 = ax2, ax1
	}
	if ay1 > ay2 {
		ay1, ay2 = ay2, ay1
	}
	if bx1 > bx2 {
		bx1, bx2 = bx2, bx1
	}
	if by1 > by2 {
		by1, by2 = by2, by1
	}
	return ax1 < bx2-eps && ax2 > bx1+eps && ay1 < by2-eps && ay2 > by1+eps
}

// collinearOverlap reports whether two axis-aligned segments lie on the same
// line and their intervals overlap more than a boundary touch.
func collinearOverlap(ax, ay, bx, by, cx, cy, dx, dy float64) bool {
	horizA := near(ay, by)
	horizC := near(cy, dy)
	vertA := near(ax, bx)
	vertC := near(cx, dx)
	if horizA && horizC && near(ay, cy) {
		return intervalsOverlap(ax, bx, cx, dx)
	}
	if vertA && vertC && near(ax, cx) {
		return intervalsOverlap(ay, by, cy, dy)
	}
	return false
}

func intervalsOverlap(a1, a2, b1, b2 float64) bool {
	lo := math.Max(math.Min(a1, a2), math.Min(b1, b2))
	hi := math.Min(math.Max(a1, a2), math.Max(b1, b2))
	return hi > lo+eps
}
