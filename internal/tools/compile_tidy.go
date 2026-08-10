package tools

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"mcp-kicad/internal/compile"
	"mcp-kicad/internal/place2/metrics"
	"mcp-kicad/internal/place2/textplace"
	"mcp-kicad/internal/sexp"
)

// tidyBudget caps how many candidate placements one compile may try. Each one
// is a full geometry pass — placement, wiring, gate, dots, text — costing
// milliseconds, so this is a time budget, not a quality one: the search almost
// always runs out of ideas long before it runs out of tries.
const tidyBudget = 24

// tidyMaxExtraCells is the most this pass will add to any one anchor. Spacing
// is the author's decision; the compiler is allowed to open a gap that clears
// text, not to redesign the layout. Past three cells the drawing stops being
// the one that was written.
const tidyMaxExtraCells = 3

// sheetScore ranks a finished schematic the way somebody reading it would.
//
// Compared in order, because these are not commensurable: a schematic whose
// netlist is wrong is not "slightly worse" than one that is right, and a wire
// demoted to a pair of labels is not paid for by a shorter total length.
type sheetScore struct {
	defects    int     // netlist post-condition failures — nothing else matters
	labels     int     // LOAD-BEARING labels: the gate demoting a net trades wires for these
	collisions int     // every text overlap the reader will see
	area       float64 // mm² of that overlap
	docDropped int     // documentation labels the compiler had to discard
	overflow   float64 // how far past the sheet's usable area it spills
	bends      int     // corners a reader's eye has to follow
	wireLen    float64 // total wire, the tie-break that prefers the smaller change
}

// maxFill is where a drawing stops fitting on its page. Content has to live
// inside the margins, and a sheet that spills off cannot be printed.
//
// There is deliberately NO lower bound to go with it. One used to exist — a
// targetFill of 0.42, on the reasoning that a drawing crammed into a corner
// reads badly and paper is free — and it was the single worst defect this
// compiler had. Filling a page is the trivial way to clear text collisions and
// hand the router room, so the search took it every time: ne555_astable came
// out with its spacing multiplied by NINE (×1.5, ×2.0, ×1.5, ×2.0 — each A4→A3
// upgrade dropped the fill again and re-armed the next step), covering 29× the
// area a human needs for the same seven parts and using 10× the wire.
//
// A drawing's size is a property of the CIRCUIT, not of the paper it is printed
// on. A hand-drawn NE555 astable uses 2% of an A4 and reads perfectly. Removing
// the lower bound cut the corpus's total area by 62% and its total wire by 34%,
// with no design getting bigger and none losing its verified netlist.
const maxFill = 0.72

func sheetFill(sch *sexp.Schematic) float64 {
	p, known := paperSizes[sch.PaperSize()]
	if !known {
		p = paperSizes["A4"]
	}
	x1, y1, x2, y2, ok := sexp.ContentBBox(sch)
	if !ok {
		return 1
	}
	return ((x2 - x1) * (y2 - y1)) / (p.w * p.h)
}

func scoreSheet(sch *sexp.Schematic, defects []NetDefect, drops []labelDrop) sheetScore {
	s := sheetScore{defects: len(defects), docDropped: len(drops)}
	// EVERY collision counts here, intrinsic ones included. Excluding them was
	// a measured mistake: greenhouse_controller went from 41 overlaps to 47
	// because the search happily cleared a wire overlap by shoving a label onto
	// its own symbol, which scored as an improvement and read as a regression.
	// "Spacing cannot fix it" is a reason not to CHASE a collision, never a
	// reason to stop counting what the reader still has to squint at.
	for _, c := range textplace.Collisions(sch) {
		s.collisions++
		s.area += c.Area
	}
	// Only labels that CARRY connectivity count on this axis. "Labels rank
	// above collisions" is about demotions — a wire traded for a pair of tags
	// — but counting every label made keeping a documentation name always a
	// loss, so the search preferred sheets stripped of their net names.
	s.labels = loadBearingLabels(sch)
	if fill := sheetFill(sch); fill > maxFill {
		s.overflow = fill - maxFill
	}
	m := metrics.Compute(sch)
	s.bends = m.BendCount
	s.wireLen = m.TotalWireLen
	return s
}

// loadBearingLabels counts the labels whose removal would change what their
// name connects — the same criterion dropCollidingDocLabels protects. A label
// that only documents an already-wired net does not count.
func loadBearingLabels(sch *sexp.Schematic) int {
	seen := map[string]bool{}
	var names []string
	for _, l := range sexp.FindAllLists(sch.Root(), "label") {
		name := sexp.StringValue(l, 1)
		if name == "" {
			name = sexp.AtomValue(l, 1)
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	sort.Strings(names) // determinism: the count must not depend on file order
	count := 0
	for _, name := range names {
		before := pinsUnderName(sch, name)
		beforeList := make([]string, 0, len(before))
		for pin := range before {
			beforeList = append(beforeList, pin)
		}
		sort.Strings(beforeList)
		removed := sch.RemoveLabelsNamed(name)
		if len(removed) == 0 {
			continue
		}
		// Load-bearing means a MEMBER would be lost, not the name: the pins
		// that were reachable under this name must still be one net.
		if !netIntact(sch, beforeList) {
			count += len(removed)
		}
		for _, n := range removed {
			sch.AddLabel(n)
		}
	}
	return count
}

// betterThan compares lexicographically. Ties are not improvements: a
// candidate has to earn the change it makes.
func (a sheetScore) betterThan(b sheetScore) bool {
	switch {
	case a.defects != b.defects:
		return a.defects < b.defects
	case a.labels != b.labels:
		return a.labels < b.labels
	case a.collisions != b.collisions:
		return a.collisions < b.collisions
	case !nearlyEqual(a.area, b.area):
		return a.area < b.area
	case a.docDropped != b.docDropped:
		// After area, before overflow: keeping a net's name never buys a new
		// collision or more overlap, but it does outrank corners and wire.
		return a.docDropped < b.docDropped
	case !nearlyEqual(a.overflow, b.overflow):
		// Ranked under the things that make a drawing WRONG or unreadable, and
		// over the things that only make it long: a sheet that does not fit its
		// page is worth more corners and more millimetres of wire to fix.
		return a.overflow < b.overflow
	case a.bends != b.bends:
		return a.bends < b.bends
	default:
		return a.wireLen < b.wireLen-0.01
	}
}

func nearlyEqual(a, b float64) bool { return a-b < 0.01 && b-a < 0.01 }

// tidy hunts for a placement that reads better by doing what the collision
// report already advises: putting more grid cells between the parts whose text
// collides.
//
// Fixing collisions one at a time does NOT work, and that is measured, not
// feared: a session that followed the report pair by pair cleared the labels it
// named and introduced two new overlaps elsewhere. `NeedCells` answers "what
// clears THIS one", never "what makes the sheet better" — so every candidate
// here is applied to a copy of the design, the whole sheet is rebuilt, and the
// change is kept only if the sheet as a whole improved.
//
// It only ever ADDS cells, never removes them: the author's spacing is a floor.
func (e *Env) tidy(d *compile.Design, best *sexp.Schematic, bestReport string, bestDefects []NetDefect, bestDrops []labelDrop) (*sexp.Schematic, string, []NetDefect, string) {
	bestScore := scoreSheet(best, bestDefects, bestDrops)
	// No early exit on the bend axis: it is always available, and a sheet with
	// no text problem at all can still be routed with fewer corners.

	current := d
	bendPen := 0              // 0 = the router's default cost for turning
	netOrder := 0             // 0 = nets routed in name order
	added := map[string]int{} // ref → cells this pass has already added
	var done []string
	tries := 0

	for tries < tidyBudget {
		improved := false
		cands := append(tidyCandidates(best, current, added, bestDrops), netOrderCandidates(best, current)...)
		cands = append(cands, bendCandidates(bendPen)...)
		cands = append(cands, anchorPinCandidates(current)...)
		cands = append(cands, netRouteOrderCandidates(netOrder)...)
		for _, cand := range cands {
			if tries >= tidyBudget {
				break
			}
			tries++

			trial := cloneDesign(current)
			if !cand.apply(trial) {
				continue
			}
			opts := buildOpts{BendPenalty: bendPen, NetOrder: netOrder}
			switch cand.kind {
			case "bend":
				opts.BendPenalty = cand.extra
			case "netorder":
				opts.NetOrder = cand.extra
			}
			sch, report, defects, drops, err := e.buildSchematic(trial, opts)
			if err != nil {
				continue // an unbuildable candidate is simply not a candidate
			}
			score := scoreSheet(sch, defects, drops)
			if !score.betterThan(bestScore) {
				continue
			}

			if cand.kind == "space" {
				added[cand.ref] += cand.extra
			}
			if cand.kind == "bend" {
				bendPen = cand.extra
			}
			if cand.kind == "netorder" {
				netOrder = cand.extra
			}
			if cand.kind != "space" {
				done = append(done, cand.what)
			}
			current, best, bestReport, bestDefects, bestDrops, bestScore = trial, sch, report, defects, drops, score
			improved = true
			break // re-measure before choosing again: the sheet just changed
		}
		if !improved {
			break
		}
	}

	if len(done) == 0 && len(added) == 0 {
		return best, bestReport, bestDefects, ""
	}
	// Spacing is summarised per symbol: two accepted +1 bumps on R5 are one
	// "R5 +2" the author can paste back, not two lines saying the same thing.
	refs := make([]string, 0, len(added))
	for ref := range added {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	for i, ref := range refs {
		done = append(done[:i], append([]string{fmt.Sprintf("%s +%d cell(s)", ref, added[ref])}, done[i:]...)...)
	}
	note := fmt.Sprintf("layout: %s — each kept only after measuring that it left the whole sheet "+
		"tidier (%d candidates tried). Copy them into the source to keep them.",
		strings.Join(done, ", "), tries)
	return best, bestReport, bestDefects, note
}

// tidyCandidate is one edit to try on a copy of the design. Two kinds exist:
// opening space between parts, and re-ordering the pins of a net so its daisy
// chain stops zig-zagging. Both are expressed as "edit the source the way an
// author would", which is what keeps the result something you can paste back.
type tidyCandidate struct {
	what  string // how the report names it
	kind  string // "space", "order", "bend", "anchor" or "netorder"
	ref   string // symbol (space) or net name (order)
	extra int    // cells to add (space) or bend penalty to route with (bend)
	order []string
}

func (c tidyCandidate) apply(d *compile.Design) bool {
	if c.kind == "anchor" {
		for bi := range d.Blocks {
			for si := range d.Blocks[bi].Symbols {
				sym := &d.Blocks[bi].Symbols[si]
				if sym.Ref == c.ref && sym.Place != nil {
					sym.Place.Pin = c.order[0]
					sym.Place.Cells += c.extra
					return true
				}
			}
		}
		return false
	}
	if c.kind == "bend" || c.kind == "netorder" {
		return true // nothing in the source changes; the router is told instead
	}
	if c.kind == "order" {
		if _, ok := d.Nets[c.ref]; !ok {
			return false
		}
		nets := make(map[string][]string, len(d.Nets))
		for k, v := range d.Nets {
			nets[k] = v
		}
		nets[c.ref] = append([]string(nil), c.order...)
		d.Nets = nets
		return true
	}
	return bumpCells(d, c.ref, c.extra)
}

// anchorPinCandidates finds symbols anchored by the wrong pin.
//
// `place` pins one of the symbol's pins a number of cells away from another
// symbol's pin. When the pin chosen is NOT the one that shares a net with the
// anchor target, the part is placed back to front: the connection has to reach
// across the body to the far pin, the router refuses, and the net degrades to a
// pair of labels. Measured on buck_mc34063a, whose sense resistor was anchored
// by pin 1 while pin 2 is the one on the anchor's net — two labels where four
// wire segments belong.
//
// Purely textual: nets and anchors both name pins as "REF.pin", so no geometry
// is needed to see the mismatch. The swap is still scored like everything else,
// because a design may have had a reason.
func anchorPinCandidates(d *compile.Design) []tidyCandidate {
	netOf := map[string]string{} // "REF.pin" → net name
	names := make([]string, 0, len(d.Nets))
	for name := range d.Nets {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, pin := range d.Nets[name] {
			netOf[pin] = name
		}
	}

	var out []tidyCandidate
	for _, b := range d.Blocks {
		for _, sym := range b.Symbols {
			if sym.Place == nil {
				continue
			}
			target, ok := netOf[sym.Place.At]
			if !ok {
				continue // the anchor pin is on no declared net
			}
			if netOf[sym.Ref+"."+sym.Place.Pin] == target {
				continue // already anchored by the pin that connects
			}
			// Which of this symbol's pins IS on the anchor's net?
			for _, pin := range d.Nets[target] {
				ref, own, cut := strings.Cut(pin, ".")
				if !cut || ref != sym.Ref {
					continue
				}
				// Offered with a little extra room as well as bare. Turning the
				// part round moves its far pin to where the near one was, and on
				// buck_mc34063a the swap alone still had nowhere to route: the
				// improvement only appears two cells further out. A search that
				// changes one thing at a time cannot cross that valley, so the
				// pair is offered as a single edit.
				for _, extra := range []int{0, 1, 2} {
					what := fmt.Sprintf("%s anchored by pin %s instead of %s", sym.Ref, own, sym.Place.Pin)
					if extra > 0 {
						what += fmt.Sprintf(" (+%d cell(s))", extra)
					}
					out = append(out, tidyCandidate{
						what: what, kind: "anchor", ref: sym.Ref,
						order: []string{own}, extra: extra,
					})
				}
				break
			}
		}
	}
	return out
}

// netRouteOrderCandidates proposes routing the nets in a different order.
//
// Each wire the router finishes becomes a soft obstacle for the next, so the
// order decides who gets the clean corridor and who has to go around. Sorted
// by name that is arbitrary, and arbitrary showed: on buck_mc34063a a VIN hop
// between two adjacent pins was routed right over the top of the IC because
// the wires that would have shared its lane were already there.
func netRouteOrderCandidates(current int) []tidyCandidate {
	var out []tidyCandidate
	for _, mode := range []int{1, 2} {
		if mode == current {
			continue
		}
		what := "nets routed shortest-first"
		if mode == 2 {
			what = "nets routed longest-first"
		}
		out = append(out, tidyCandidate{what: what, kind: "netorder", extra: mode})
	}
	return out
}

// bendCandidates proposes routing the whole sheet with a stiffer cost for
// changing direction.
//
// Measured on the reference corpus: 54% of the corners in a compiled schematic
// are the router's choice rather than something the pin positions force, and a
// sweep of the constant showed why it cannot simply be raised — at 64 the
// corpus loses 13 corners, at 16 and 32 it gains two dozen text collisions.
// Which trade a given sheet wants is a question only that sheet can answer, so
// it is asked here and settled by the same score as everything else.
func bendCandidates(current int) []tidyCandidate {
	var out []tidyCandidate
	for _, pen := range []int{32, 64} {
		if pen <= current {
			continue
		}
		out = append(out, tidyCandidate{
			what:  fmt.Sprintf("router turn cost %d", pen),
			kind:  "bend",
			extra: pen,
		})
	}
	return out
}

// netOrderCandidates proposes walking each net's pins nearest-neighbour first
// instead of in the order they were declared.
//
// routeNets daisy-chains pin[0]→pin[1]→…, so the declaration order IS the wire
// path: a net listed in schematic-reading order rather than in physical order
// makes the router double back, and every double-back is corners the reader has
// to follow. Reordering cannot change connectivity — the same pins are on the
// same net — so VerifyNetlist is indifferent and only the geometry moves.
func netOrderCandidates(sch *sexp.Schematic, d *compile.Design) []tidyCandidate {
	names := make([]string, 0, len(d.Nets))
	for name := range d.Nets {
		names = append(names, name)
	}
	sort.Strings(names) // never range a map in the placement path

	var out []tidyCandidate
	for _, name := range names {
		pins := d.Nets[name]
		if len(pins) < 3 {
			continue // a two-pin net has only one chain
		}
		// Power nets get one symbol per pin and no chain at all, so re-ordering
		// them cannot move a single wire. Measured: 10 of the 14 candidates this
		// used to offer were power nets, every one of them tried and rejected —
		// budget spent on edits that could not possibly win.
		if _, manual := d.PowerNets[name]; manual || netNameToPowerLibID(name) != "" {
			continue
		}
		type pt struct {
			ref  string
			x, y float64
		}
		pos := make([]pt, 0, len(pins))
		for _, ref := range pins {
			p, ok := sexp.FindPin(sch, ref)
			if !ok {
				pos = nil
				break
			}
			pos = append(pos, pt{ref, p.X, p.Y})
		}
		if len(pos) != len(pins) {
			continue
		}

		// Greedy nearest neighbour from the declared first pin: the author's
		// starting point is kept, only the walk changes.
		used := make([]bool, len(pos))
		order := []string{pos[0].ref}
		used[0] = true
		cur := pos[0]
		for len(order) < len(pos) {
			best, bestD := -1, 0.0
			for i, q := range pos {
				if used[i] {
					continue
				}
				dist := math.Hypot(q.x-cur.x, q.y-cur.y)
				if best == -1 || dist < bestD {
					best, bestD = i, dist
				}
			}
			used[best] = true
			order = append(order, pos[best].ref)
			cur = pos[best]
		}
		if equalStrings(order, pins) {
			continue // already the shortest walk
		}
		out = append(out, tidyCandidate{
			what:  "net " + name + " re-ordered",
			kind:  "order",
			ref:   name,
			order: order,
		})
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// tidyCandidates proposes moves from the collisions the sheet actually has.
// Each collision names the text's own symbol and what it sits on, and either
// of those two moving apart could clear it — so both are offered, and the
// search decides by measuring.
func tidyCandidates(sch *sexp.Schematic, d *compile.Design, added map[string]int, drops []labelDrop) []tidyCandidate {
	placeable := map[string]bool{}
	for _, b := range d.Blocks {
		for _, s := range b.Symbols {
			if s.Place != nil {
				placeable[s.Ref] = true
			}
		}
	}

	seen := map[string]bool{}
	var out []tidyCandidate
	add := func(ref string, extra int) {
		if ref == "" || !placeable[ref] || extra < 1 {
			return
		}
		if added[ref]+extra > tidyMaxExtraCells {
			return
		}
		c := tidyCandidate{
			what:  fmt.Sprintf("%s +%d cell(s)", ref, extra),
			kind:  "space",
			ref:   ref,
			extra: extra,
		}
		if !seen[c.what] {
			seen[c.what] = true
			out = append(out, c)
		}
	}

	for _, c := range textplace.Collisions(sch) {
		if c.Intrinsic {
			continue
		}
		need := c.NeedCells()
		if need < 1 {
			need = 1
		}
		// The advice, and one cell, because the advice is a local figure and
		// the smaller change is usually the one that does not disturb a
		// neighbour.
		for _, ref := range []string{c.Text, refFromWith(c.With)} {
			add(ref, need)
			if need != 1 {
				add(ref, 1)
			}
		}
	}

	// Dropped documentation labels leave no collision behind — the label is
	// gone from the finished sheet — so the loop above can never propose the
	// spacing that would have saved one. The drop record says exactly what it
	// would have taken; offer that, on the collider and on every symbol of
	// the net that lost its name.
	for _, dr := range drops {
		if dr.NeedCells < 1 {
			continue // no spacing would have kept it
		}
		add(refFromWith(dr.With), dr.NeedCells)
		if dr.NeedCells != 1 {
			add(refFromWith(dr.With), 1)
		}
		for _, pin := range d.Nets[dr.Net] {
			if ref, _, ok := strings.Cut(pin, "."); ok {
				add(ref, dr.NeedCells)
				if dr.NeedCells != 1 {
					add(ref, 1)
				}
			}
		}
	}
	return out
}

// refFromWith pulls a reference designator out of a collision's "with" field,
// which reads like "body U1", "text C3", "pin C2.1" or "wire VIN".
func refFromWith(with string) string {
	kind, rest, ok := strings.Cut(with, " ")
	if !ok {
		return ""
	}
	switch kind {
	case "body", "text":
		return rest
	case "pin":
		// Two shapes share this prefix: "pin C2.1" and "pin number C2.1".
		// Stripping the second one FIRST matters — cutting "number C2.1" at
		// the dot yields "number C2", a reference no symbol has, so every
		// pin-number collision was quietly contributing no candidate at all.
		rest = strings.TrimPrefix(rest, "number ")
		if ref, _, ok := strings.Cut(rest, "."); ok {
			return ref
		}
	}
	return ""
}

// bumpCells adds extra grid cells to one symbol's anchor. Reports whether it
// found the symbol; a design whose author placed it without `place` cannot be
// spaced this way and is left alone.
func bumpCells(d *compile.Design, ref string, extra int) bool {
	for bi := range d.Blocks {
		for si := range d.Blocks[bi].Symbols {
			s := &d.Blocks[bi].Symbols[si]
			if s.Ref != ref || s.Place == nil {
				continue
			}
			s.Place.Cells += extra
			return true
		}
	}
	return false
}

// cloneDesign copies deeply enough that a trial cannot alter the design the
// caller still holds: the block and symbol slices, and every Place, which is
// the only thing a trial changes.
func cloneDesign(d *compile.Design) *compile.Design {
	out := *d
	out.Blocks = make([]compile.Block, len(d.Blocks))
	copy(out.Blocks, d.Blocks)
	for bi := range out.Blocks {
		syms := make([]compile.Symbol, len(d.Blocks[bi].Symbols))
		copy(syms, d.Blocks[bi].Symbols)
		for si := range syms {
			if syms[si].Place != nil {
				p := *syms[si].Place
				syms[si].Place = &p
			}
		}
		out.Blocks[bi].Symbols = syms
	}
	return &out
}
