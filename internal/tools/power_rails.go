package tools

import (
	"math"
	"sort"

	"mcp-kicad/internal/place2/metrics"
	"mcp-kicad/internal/sexp"
)

// A power net whose pins line up — a decoupling farm, a row of bulk caps —
// used to receive one power symbol PER PIN, which is correct for pins
// scattered across a sheet and absurd for four capacitors standing side by
// side: the drawing showed a power entry and a cap farm with not one wire on
// it, and two readers independently called that zone unintelligible. A person
// draws ONE rail across the row and ONE symbol on it.
//
// The rail is trivial, verifiable geometry in the wiregen tradition: straight
// trunk one grid step beyond the pin tips, one straight stub per pin, and the
// whole corridor is checked against foreign points and symbol bodies first —
// the group falls back to per-pin symbols rather than accept a doubtful wire.

const (
	railStub   = 2.54 // trunk sits one grid step beyond the pin tips
	railMaxGap = 25.4 // widest pin-to-pin gap that still reads as one rail
	railMinRun = 3    // fewer pins than this stay per-pin
)

type railGroup struct {
	pins []pinPos // sorted by X
	dir  float64  // 90 (tips point up, trunk above) or 270 (down, trunk below)
}

func (g railGroup) trunkY() float64 {
	if g.dir == 90 {
		return g.pins[0].y - railStub // screen Y grows down
	}
	return g.pins[0].y + railStub
}

// railSegments returns every wire the rail would draw: one stub per pin and
// the trunk cut at each stub, so every meeting point is a wire END — a wire
// touching another mid-segment connects nothing in KiCad.
func (g railGroup) railSegments() [][4]float64 {
	ty := g.trunkY()
	var segs [][4]float64
	for _, p := range g.pins {
		segs = append(segs, [4]float64{p.x, p.y, p.x, ty})
	}
	for i := 1; i < len(g.pins); i++ {
		segs = append(segs, [4]float64{g.pins[i-1].x, ty, g.pins[i].x, ty})
	}
	return segs
}

// detectPowerRails splits a power net's pins into rail groups and the loose
// pins that keep the per-pin policy: same direction (up or down), tips on one
// Y, no gap wider than railMaxGap, at least railMinRun pins.
func detectPowerRails(positions []pinPos) (groups []railGroup, loose []pinPos) {
	type key struct{ dir, y float64 }
	buckets := map[key][]pinPos{}
	var order []key
	for _, p := range positions {
		if p.dir != 90 && p.dir != 270 {
			loose = append(loose, p)
			continue
		}
		k := key{p.dir, sexp.Round2(p.y)}
		if _, ok := buckets[k]; !ok {
			order = append(order, k)
		}
		buckets[k] = append(buckets[k], p)
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].dir != order[j].dir {
			return order[i].dir < order[j].dir
		}
		return order[i].y < order[j].y
	})
	for _, k := range order {
		ps := buckets[k]
		sort.Slice(ps, func(i, j int) bool {
			if ps[i].x != ps[j].x {
				return ps[i].x < ps[j].x
			}
			return ps[i].ref < ps[j].ref
		})
		var run []pinPos
		flush := func() {
			if len(run) >= railMinRun {
				groups = append(groups, railGroup{pins: append([]pinPos(nil), run...), dir: k.dir})
			} else {
				loose = append(loose, run...)
			}
			run = run[:0]
		}
		for i, p := range ps {
			if i > 0 && p.x-ps[i-1].x > railMaxGap {
				flush()
			}
			run = append(run, p)
		}
		flush()
	}
	return groups, loose
}

// railClear reports whether every segment of the rail avoids foreign points
// (pins, labels, sampled foreign wires — touching one is a silent short) and
// symbol bodies. Doubt means no: the fallback is the per-pin policy, which is
// always correct.
func railClear(sch *sexp.Schematic, g railGroup, foreign [][2]float64) bool {
	const tol = 0.65
	segs := g.railSegments()
	for _, s := range segs {
		ax, ay, bx, by := s[0], s[1], s[2], s[3]
		for _, p := range foreign {
			px, py := p[0], p[1]
			if px < math.Min(ax, bx)-tol || px > math.Max(ax, bx)+tol ||
				py < math.Min(ay, by)-tol || py > math.Max(ay, by)+tol {
				continue
			}
			if math.Abs(ay-by) < 0.01 { // horizontal segment
				if math.Abs(py-ay) <= tol {
					return false
				}
			} else if math.Abs(px-ax) <= tol {
				return false
			}
		}
	}
	for _, sym := range sexp.ReadSymbols(sch) {
		x1, y1, x2, y2 := metrics.BodyBBox(sym)
		for _, s := range segs {
			if sexp.SegmentCrossesBox(s[0], s[1], s[2], s[3], x1, y1, x2, y2) {
				return false
			}
		}
	}
	return true
}

// emitRail draws one rail group: the power symbol via the standard emitter at
// the leftmost pin (same offset-and-stub convention as everywhere else), then
// stubs and trunk pieces. If the emitter stepped the symbol further out, its
// longer stub is split where the trunk meets it, so the meeting point is a
// wire end. Returns the number of wire segments drawn.
func emitRail(sch *sexp.Schematic, em *PowerEmitter, libID string, g railGroup) (wires int, ok bool) {
	if _, emitted, _ := em.Emit(libID, g.pins[0].ref); !emitted {
		return 0, false
	}
	ty := g.trunkY()
	for i, p := range g.pins {
		if i > 0 { // the first pin's stub came with the symbol
			sch.AddWire(sexp.NewWire(p.x, p.y, p.x, ty))
			wires++
		}
		if i > 0 {
			sch.AddWire(sexp.NewWire(g.pins[i-1].x, ty, p.x, ty))
			wires++
		}
	}
	sexp.SplitWiresAt(sch, g.pins[0].x, ty)
	return wires, true
}
