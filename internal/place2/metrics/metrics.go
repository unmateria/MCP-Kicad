// Package metrics computes objective layout-quality metrics on a parsed
// KiCad schematic. The numbers it returns are the gating signal for every
// phase of the placement+routing redesign: without them we cannot tell
// whether a change in the pipeline is an improvement or a regression.
//
// The metrics intentionally cover only what is observable in the .kicad_sch:
//   - BendCount        — number of 90° bends across all wires
//   - CrossingCount    — wire segments of distinct nets crossing orthogonally
//   - JunctionCount    — number of (junction ...) markers
//   - WireCount        — total wire segments
//   - TotalWireLen     — sum of segment lengths in mm
//   - AvgWireLen       — TotalWireLen / WireCount
//   - WireThruSymbol   — wires passing through a symbol body interior
//   - SymbolCount      — distinct placed symbol references (excl. power)
//   - NetCount         — non-dangling, non-power nets
//   - BboxArea, Density — schematic envelope and component density
package metrics

import (
	"fmt"
	"math"
	"strings"

	"mcp-kicad/internal/sexp"
)

// Metrics is the result of analysing one schematic.
type Metrics struct {
	NetCount       int
	SymbolCount    int
	WireCount      int
	BendCount      int
	CrossingCount  int
	JunctionCount  int
	WireThruSymbol int
	TotalWireLen   float64
	AvgWireLen     float64
	BboxArea       float64
	Density        float64
}

// String returns a human-readable one-line-per-metric summary.
func (m Metrics) String() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "symbols:           %d\n", m.SymbolCount)
	fmt.Fprintf(&sb, "nets:              %d\n", m.NetCount)
	fmt.Fprintf(&sb, "wire_segments:     %d\n", m.WireCount)
	fmt.Fprintf(&sb, "bends:             %d\n", m.BendCount)
	fmt.Fprintf(&sb, "crossings:         %d\n", m.CrossingCount)
	fmt.Fprintf(&sb, "junctions:         %d\n", m.JunctionCount)
	fmt.Fprintf(&sb, "wires_thru_symbol: %d\n", m.WireThruSymbol)
	fmt.Fprintf(&sb, "total_wire_len:    %.2f mm\n", m.TotalWireLen)
	fmt.Fprintf(&sb, "avg_wire_len:      %.2f mm\n", m.AvgWireLen)
	fmt.Fprintf(&sb, "bbox_area:         %.2f mm^2\n", m.BboxArea)
	fmt.Fprintf(&sb, "density:           %.4f sym/mm^2\n", m.Density)
	return sb.String()
}

// Compute analyses a parsed schematic and returns its metrics. The schematic
// may be partially populated — any missing data is reported as zero rather
// than as an error so the function can be used during pipeline development.
func Compute(sch *sexp.Schematic) Metrics {
	syms := sexp.ReadSymbols(sch)
	wires := sch.Wires()

	var m Metrics

	// Symbol count excludes power symbols (they don't count as components).
	refSeen := make(map[string]bool)
	for _, s := range syms {
		if strings.HasPrefix(s.LibID, "power:") || s.LibID == "Device:PWR_FLAG" {
			continue
		}
		if !refSeen[s.Reference] {
			refSeen[s.Reference] = true
			m.SymbolCount++
		}
	}

	// Net count: non-dangling, non-anonymous nets with at least 2 pins.
	nets := sexp.TraceNets(sch)
	netByPoint := make(map[[2]float64]string)
	for _, net := range nets {
		if net.Dangling || len(net.Pins) < 2 {
			continue
		}
		if !strings.HasPrefix(net.Name, "Net-(") {
			m.NetCount++
		}
		// Index every pin position to its net so we can later detect crossings
		// (segments crossing belong to the same net iff their endpoints match).
		for _, pinRef := range net.Pins {
			for _, sym := range syms {
				if sym.Reference != pinRef.Reference {
					continue
				}
				if pinRef.Unit != 0 && sym.Unit != pinRef.Unit {
					continue
				}
				for _, p := range sym.Pins {
					if p.Number == pinRef.PinNumber || p.Name == pinRef.PinName {
						netByPoint[round2pt(p.X, p.Y)] = net.Name
					}
				}
			}
		}
	}

	// Walk wires once to harvest segments + endpoints + total length.
	type seg struct {
		ax, ay, bx, by float64
		net            string
	}
	var segs []seg
	for _, w := range wires {
		ax, ay, bx, by, ok := WireCoords(w)
		if !ok {
			continue
		}
		m.WireCount++
		dx := bx - ax
		dy := by - ay
		m.TotalWireLen += math.Sqrt(dx*dx + dy*dy)
		// Net for this segment: pick whichever endpoint matches a pin in our index.
		netName := netByPoint[round2pt(ax, ay)]
		if netName == "" {
			netName = netByPoint[round2pt(bx, by)]
		}
		segs = append(segs, seg{ax, ay, bx, by, netName})
	}
	if m.WireCount > 0 {
		m.AvgWireLen = m.TotalWireLen / float64(m.WireCount)
	}

	// BendCount: sum over all wire endpoints of how many segments meet there
	// at a 90° angle. We count bends as the number of corners in a connected
	// path, which is approximated by counting all (point, distinct-direction)
	// pairs sharing the same endpoint.
	endpointDirs := make(map[[2]float64]map[int]int) // point → directionBitset → count
	for _, s := range segs {
		dir := SegDir(s.ax, s.ay, s.bx, s.by)
		for _, pt := range [...][2]float64{round2pt(s.ax, s.ay), round2pt(s.bx, s.by)} {
			if endpointDirs[pt] == nil {
				endpointDirs[pt] = make(map[int]int)
			}
			endpointDirs[pt][dir]++
		}
	}
	for _, dirs := range endpointDirs {
		hasH := dirs[0] > 0
		hasV := dirs[1] > 0
		if hasH && hasV {
			m.BendCount++
		}
	}

	// CrossingCount: pairs of segments belonging to DIFFERENT nets that cross
	// each other strictly in their interior. Same-net crossings are valid
	// junctions (T-intersection or 4-way) and counted separately.
	for i := 0; i < len(segs); i++ {
		for j := i + 1; j < len(segs); j++ {
			a, b := segs[i], segs[j]
			if a.net != "" && a.net == b.net {
				continue
			}
			if SegmentsCrossOrthogonal(a.ax, a.ay, a.bx, a.by, b.ax, b.ay, b.bx, b.by) {
				m.CrossingCount++
			}
		}
	}

	// JunctionCount: direct count of (junction ...) markers.
	for _, c := range sch.Root().Children {
		if c.Head() == "junction" {
			m.JunctionCount++
		}
	}

	// WireThruSymbol: any segment that passes through the body interior of a
	// non-power symbol.
	for _, s := range segs {
		for _, sym := range syms {
			if strings.HasPrefix(sym.LibID, "power:") || sym.LibID == "Device:PWR_FLAG" {
				continue
			}
			x1, y1, x2, y2 := BodyBBox(sym)
			if sexp.SegmentCrossesBox(s.ax, s.ay, s.bx, s.by, x1, y1, x2, y2) {
				m.WireThruSymbol++
				break
			}
		}
	}

	// Bbox + density.
	if len(syms) > 0 {
		minX, minY := math.MaxFloat64, math.MaxFloat64
		maxX, maxY := -math.MaxFloat64, -math.MaxFloat64
		for _, s := range syms {
			x1, y1, x2, y2 := sexp.SymbolBBox(s)
			if x1 < minX {
				minX = x1
			}
			if y1 < minY {
				minY = y1
			}
			if x2 > maxX {
				maxX = x2
			}
			if y2 > maxY {
				maxY = y2
			}
		}
		w := maxX - minX
		h := maxY - minY
		if w > 0 && h > 0 {
			m.BboxArea = w * h
			if m.SymbolCount > 0 {
				m.Density = float64(m.SymbolCount) / m.BboxArea
			}
		}
	}

	return m
}

// SegDir returns 0 for horizontal, 1 for vertical, or -1 for diagonal/zero.
func SegDir(ax, ay, bx, by float64) int {
	switch {
	case math.Abs(ay-by) < 0.01 && math.Abs(ax-bx) > 0.01:
		return 0
	case math.Abs(ax-bx) < 0.01 && math.Abs(ay-by) > 0.01:
		return 1
	default:
		return -1
	}
}

// SegmentsCrossOrthogonal reports whether one segment is horizontal and the
// other vertical AND they intersect strictly in the interior of both. Wires
// sharing an endpoint do not count.
func SegmentsCrossOrthogonal(ax, ay, bx, by, cx, cy, dx, dy float64) bool {
	// Normalise so a is left of b for the horizontal segment.
	dirAB := SegDir(ax, ay, bx, by)
	dirCD := SegDir(cx, cy, dx, dy)
	if dirAB == -1 || dirCD == -1 || dirAB == dirCD {
		return false
	}
	hx1, hy, hx2 := ax, ay, bx
	vy1, vx, vy2 := cy, cx, dy
	if dirAB == 1 {
		hx1, hy, hx2 = cx, cy, dx
		vy1, vx, vy2 = ay, ax, by
	}
	if hx1 > hx2 {
		hx1, hx2 = hx2, hx1
	}
	if vy1 > vy2 {
		vy1, vy2 = vy2, vy1
	}
	// Strict interior intersection.
	return vx > hx1 && vx < hx2 && hy > vy1 && hy < vy2
}

// BodyBBox returns the symbol body bbox (inset from pin tips) so wires that
// terminate AT a pin don't count as "through symbol". Exported for reuse by
// internal/place2/gate, which needs the identical inset logic to avoid
// false-flagging legitimate pin connections as WIRE_THRU_SYMBOL violations.
func BodyBBox(sym sexp.SchematicSymbol) (x1, y1, x2, y2 float64) {
	const pinLen = 2.54
	const defaultHalf = 5.08
	if len(sym.Pins) == 0 {
		return sym.X - defaultHalf, sym.Y - defaultHalf,
			sym.X + defaultHalf, sym.Y + defaultHalf
	}
	x1, y1 = sym.Pins[0].X, sym.Pins[0].Y
	x2, y2 = x1, y1
	for _, p := range sym.Pins[1:] {
		if p.X < x1 {
			x1 = p.X
		}
		if p.Y < y1 {
			y1 = p.Y
		}
		if p.X > x2 {
			x2 = p.X
		}
		if p.Y > y2 {
			y2 = p.Y
		}
	}
	x1 += pinLen
	y1 += pinLen
	x2 -= pinLen
	y2 -= pinLen
	if x1 > x2 {
		x1, x2 = x2, x1
	}
	if y1 > y2 {
		y1, y2 = y2, y1
	}
	return
}

func round2pt(x, y float64) [2]float64 {
	return [2]float64{math.Round(x*100) / 100, math.Round(y*100) / 100}
}

// WireCoords pulls the two endpoints from a (wire (pts (xy ..) (xy ..))).
// Exported for reuse by internal/place2/gate.
func WireCoords(w *sexp.Node) (ax, ay, bx, by float64, ok bool) {
	pts := sexp.FindList(w, "pts")
	if pts == nil {
		return
	}
	var xs, ys [2]float64
	n := 0
	for _, xy := range pts.Children {
		if xy.Head() != "xy" || n >= 2 {
			continue
		}
		xs[n] = parseF(sexp.AtomValue(xy, 1))
		ys[n] = parseF(sexp.AtomValue(xy, 2))
		n++
	}
	if n < 2 {
		return
	}
	return xs[0], ys[0], xs[1], ys[1], true
}

func parseF(s string) float64 {
	var v float64
	_, _ = fmt.Sscanf(s, "%f", &v)
	return v
}
