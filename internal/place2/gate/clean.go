package gate

import (
	"math"
	"sort"
	"strconv"

	"mcp-kicad/internal/place2/metrics"
	"mcp-kicad/internal/sexp"
)

// Clean is a cheap pre-pass that fixes the "two wires drawn on top of each
// other" complaint without demoting anything: it merges same-net collinear
// overlapping/adjacent wire segments into single segments, drops
// zero-length wires, and removes duplicate junction markers. It never
// touches geometry belonging to two DIFFERENT nets — that is Check/Enforce's
// job.
func Clean(sch *sexp.Schematic) {
	dropZeroLengthWires(sch)
	mergeCollinearSameNet(sch)
	dedupeJunctions(sch)
}

func dropZeroLengthWires(sch *sexp.Schematic) int {
	filtered := sch.Root().Children[:0:0]
	removed := 0
	for _, c := range sch.Root().Children {
		if c.Head() == "wire" {
			ax, ay, bx, by, ok := metrics.WireCoords(c)
			if ok && math.Abs(ax-bx) < eps && math.Abs(ay-by) < eps {
				removed++
				continue
			}
		}
		filtered = append(filtered, c)
	}
	sch.Root().Children = filtered
	return removed
}

// mergeCollinearSameNet groups wire segments by (net, direction, offset)
// and, within each group, merges intervals that overlap or touch into a
// single wire. Segments of the same net that are collinear but genuinely
// disjoint (a real gap between them) are left untouched.
//
// CRITICAL correctness rule: a merge must never eliminate a vertex that
// something else relies on as a connection point — a component pin, a net
// label, a junction, or a branch wire (e.g. a perpendicular stub T-ing into
// this line, which is NOT a member of the collinear group being merged
// here). This codebase's netlist tracer (sexp.TraceNets) only unions a
// wire's own two declared endpoints; collapsing two segments that meet
// exactly at such a point into one longer wire silently turns that point
// into a bare interior point of the new wire, which is no longer a
// registered endpoint anywhere — electrically orphaning whatever was
// attached there.
//
// A boundary point is safe to swallow only if EVERY wire touching it belongs
// to the same collinear group being merged (a plain two-segment chain with
// nothing else attached) AND it isn't a static protected point (pin/label/
// junction) that must survive as a real endpoint regardless of which wires
// touch it.
func mergeCollinearSameNet(sch *sexp.Schematic) int {
	netOf := sexp.TracePointNets(sch)
	segs := collectWireSegs(sch, netOf)
	staticProtected := staticProtectedPoints(sch)

	// Global point -> every wire node touching it, computed ONCE from the
	// pre-merge state so group-membership checks below stay stable even as
	// toRemove/toAdd accumulate changes to be applied at the very end.
	touchingWires := make(map[[2]float64][]*sexp.Node)
	for _, s := range segs {
		touchingWires[round2pt(s.ax, s.ay)] = append(touchingWires[round2pt(s.ax, s.ay)], s.node)
		touchingWires[round2pt(s.bx, s.by)] = append(touchingWires[round2pt(s.bx, s.by)], s.node)
	}

	type key struct {
		net    string
		dir    int
		offset float64
	}
	groups := make(map[key][]wireSeg)
	for _, s := range segs {
		dir := metrics.SegDir(s.ax, s.ay, s.bx, s.by)
		if dir == -1 {
			continue
		}
		var offset float64
		if dir == 0 {
			offset = sexp.Round2(s.ay)
		} else {
			offset = sexp.Round2(s.ax)
		}
		k := key{s.net, dir, offset}
		groups[k] = append(groups[k], s)
	}

	// pointOnAxis converts a coordinate along the group's varying axis back
	// to the full 2D point, for protected-point lookups.
	pointOnAxis := func(k key, v float64) [2]float64 {
		if k.dir == 0 {
			return round2pt(v, k.offset)
		}
		return round2pt(k.offset, v)
	}

	type interval struct {
		lo, hi float64
		nodes  []*sexp.Node
	}

	toRemove := make(map[*sexp.Node]bool)
	var toAdd []*sexp.Node
	changed := 0

	for k, group := range groups {
		if len(group) < 2 {
			continue
		}
		groupNodes := make(map[*sexp.Node]bool, len(group))
		for _, s := range group {
			groupNodes[s.node] = true
		}
		isProtected := func(p [2]float64) bool {
			if staticProtected[p] {
				return true
			}
			for _, n := range touchingWires[p] {
				if !groupNodes[n] {
					return true // some OTHER wire (different net/direction/offset) relies on this point
				}
			}
			return false
		}

		sort.Slice(group, func(i, j int) bool {
			return varyingStart(group[i], k.dir) < varyingStart(group[j], k.dir)
		})
		var merged []interval
		for _, s := range group {
			lo, hi := varyingRange(s, k.dir)
			canJoin := false
			if len(merged) > 0 {
				last := &merged[len(merged)-1]
				if lo <= last.hi+eps {
					newLo := last.lo
					newHi := last.hi
					if hi > newHi {
						newHi = hi
					}
					// A protected point is only endangered if the merge would
					// actually demote it from a real wire terminal to a bare
					// interior point of the combined range. Check every
					// original endpoint that could end up strictly inside
					// (last.hi, and this segment's own lo AND hi — hi matters
					// too when this segment's range is fully contained inside
					// the running interval, e.g. two overlapping stubs from
					// the same pin with different reach).
					strictlyInterior := func(v float64) bool {
						return v > newLo+eps && v < newHi-eps
					}
					danger := false
					for _, v := range [3]float64{last.hi, lo, hi} {
						if strictlyInterior(v) && isProtected(pointOnAxis(k, v)) {
							danger = true
							break
						}
					}
					canJoin = !danger
				}
			}
			if canJoin {
				last := &merged[len(merged)-1]
				if hi > last.hi {
					last.hi = hi
				}
				last.nodes = append(last.nodes, s.node)
			} else {
				merged = append(merged, interval{lo: lo, hi: hi, nodes: []*sexp.Node{s.node}})
			}
		}

		anyMerge := false
		for _, m := range merged {
			if len(m.nodes) > 1 {
				anyMerge = true
				break
			}
		}
		if !anyMerge {
			continue
		}

		for _, m := range merged {
			if len(m.nodes) <= 1 {
				continue // leave lone segments in this bucket untouched
			}
			for _, n := range m.nodes {
				toRemove[n] = true
			}
			var nw *sexp.Node
			if k.dir == 0 {
				nw = sexp.NewWire(m.lo, k.offset, m.hi, k.offset)
			} else {
				nw = sexp.NewWire(k.offset, m.lo, k.offset, m.hi)
			}
			toAdd = append(toAdd, nw)
			changed++
		}
	}

	if len(toRemove) == 0 {
		return 0
	}
	filtered := sch.Root().Children[:0:0]
	for _, c := range sch.Root().Children {
		if c.Head() == "wire" && toRemove[c] {
			continue
		}
		filtered = append(filtered, c)
	}
	filtered = append(filtered, toAdd...)
	sch.Root().Children = filtered
	return changed
}

// staticProtectedPoints returns every point that must remain a real wire
// endpoint regardless of which wires touch it: component pin positions, net
// label positions, and junction markers.
func staticProtectedPoints(sch *sexp.Schematic) map[[2]float64]bool {
	protected := make(map[[2]float64]bool)
	for _, sym := range sexp.ReadSymbols(sch) {
		for _, p := range sym.Pins {
			protected[round2pt(p.X, p.Y)] = true
		}
	}
	for _, c := range sch.Root().Children {
		var atN *sexp.Node
		switch c.Head() {
		case "label", "junction":
			atN = sexp.FindList(c, "at")
		}
		if atN == nil {
			continue
		}
		x, _ := strconv.ParseFloat(sexp.AtomValue(atN, 1), 64)
		y, _ := strconv.ParseFloat(sexp.AtomValue(atN, 2), 64)
		protected[round2pt(x, y)] = true
	}
	return protected
}

func varyingRange(s wireSeg, dir int) (lo, hi float64) {
	if dir == 0 {
		lo, hi = s.ax, s.bx
	} else {
		lo, hi = s.ay, s.by
	}
	if lo > hi {
		lo, hi = hi, lo
	}
	return
}

func varyingStart(s wireSeg, dir int) float64 {
	lo, _ := varyingRange(s, dir)
	return lo
}

func dedupeJunctions(sch *sexp.Schematic) int {
	seen := make(map[[2]float64]bool)
	filtered := sch.Root().Children[:0:0]
	removed := 0
	for _, c := range sch.Root().Children {
		if c.Head() == "junction" {
			atN := sexp.FindList(c, "at")
			if atN != nil {
				x, _ := strconv.ParseFloat(sexp.AtomValue(atN, 1), 64)
				y, _ := strconv.ParseFloat(sexp.AtomValue(atN, 2), 64)
				p := round2pt(x, y)
				if seen[p] {
					removed++
					continue
				}
				seen[p] = true
			}
		}
		filtered = append(filtered, c)
	}
	sch.Root().Children = filtered
	return removed
}
