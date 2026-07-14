package tools

import (
	"context"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-kicad/internal/sexp"
)

type designContextInput struct {
	SchematicPath string `json:"schematic_path" jsonschema:"required"`
}

func (e *Env) handleGetDesignContext(_ context.Context, _ *mcp.CallToolRequest, in designContextInput) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)
	if in.SchematicPath == "" {
		return toolText("error: schematic_path is required"), nil, nil
	}
	data, err := os.ReadFile(in.SchematicPath)
	if err != nil {
		return toolText(fmt.Sprintf("error reading schematic: %v", err)), nil, nil
	}
	sch, err := sexp.ParseSchematic(string(data))
	if err != nil {
		return toolText(fmt.Sprintf("error parsing schematic: %v", err)), nil, nil
	}

	syms := sexp.ReadSymbols(sch)
	nets := sexp.TraceNets(sch)
	connSet := sexp.ConnectedPins(sch)

	var sb strings.Builder

	// --- COMPONENTS section ---
	for _, sym := range syms {
		fmt.Fprintf(&sb, "COMPONENT %s (%s) rot=%.0f°  @ %.2f,%.2f\n",
			sym.Reference, sym.LibID, sym.Rotation, sym.X, sym.Y)
		for _, p := range sym.Pins {
			key := [2]float64{math.Round(p.X*100) / 100, math.Round(p.Y*100) / 100}
			status := "UNCONNECTED"
			if connSet[key] {
				status = "connected"
			}
			netName := pinNet(sym.Reference, p, nets)
			fmt.Fprintf(&sb, "  pin %-4s (%-12s) %-3s @ %.2f,%.2f    net: %-18s [%s]\n",
				p.Number, p.Electrical, directionName(p.Direction), p.X, p.Y, netName, status)
		}
	}

	// --- NETS section ---
	fmt.Fprintf(&sb, "\nNETS (%d total):\n", len(nets))
	for _, net := range nets {
		refs := make([]string, len(net.Pins))
		for i, pr := range net.Pins {
			refs[i] = pr.String()
		}
		sort.Strings(refs)
		flag := ""
		if net.Dangling {
			flag = " [dangling]"
		}
		fmt.Fprintf(&sb, "  %-20s%s → %s\n", net.Name, flag, strings.Join(refs, ", "))
	}

	// --- PROBLEMS section ---
	problems := []string{}
	problems = append(problems, detectWrongDirWires(sch, syms)...)
	problems = append(problems, detectCrossings(sch)...)
	problems = append(problems, detectNearMisses(sch, syms, connSet)...)

	fmt.Fprintf(&sb, "\nPROBLEMS (%d found):\n", len(problems))
	if len(problems) == 0 {
		sb.WriteString("  none\n")
	}
	for _, p := range problems {
		fmt.Fprintf(&sb, "  %s\n", p)
	}

	return toolText(strings.TrimRight(sb.String(), "\n")), nil, nil
}

// pinNet returns the net name for a given pin by scanning the traced nets.
func pinNet(ref string, p sexp.PinInfo, nets []sexp.Net) string {
	for _, net := range nets {
		for _, pr := range net.Pins {
			if pr.Reference == ref && (pr.PinNumber == p.Number || pr.PinName == p.Name) {
				return net.Name
			}
		}
	}
	return "—"
}

// detectWrongDirWires finds wire segments that exit a pin in the wrong direction.
// A correct wire exits from pin.X,pin.Y in the direction pin.Direction points.
// A wrong-direction wire exits in the opposite direction (or perpendicular).
func detectWrongDirWires(sch *sexp.Schematic, syms []sexp.SchematicSymbol) []string {
	const eps = 0.01
	var problems []string

	// Build a map of wire-segment pairs from their pts nodes.
	type seg struct{ ax, ay, bx, by float64 }
	var segs []seg
	for _, wire := range sch.Wires() {
		pts := sexp.FindList(wire, "pts")
		if pts == nil || len(pts.Children) < 2 {
			continue
		}
		var xys [][2]float64
		for _, xy := range pts.Children {
			if xy.Head() != "xy" {
				continue
			}
			xys = append(xys, [2]float64{
				sexp.Round2(parseFloat(sexp.AtomValue(xy, 1))),
				sexp.Round2(parseFloat(sexp.AtomValue(xy, 2))),
			})
		}
		if len(xys) >= 2 {
			segs = append(segs, seg{xys[0][0], xys[0][1], xys[1][0], xys[1][1]})
		}
	}

	for _, sym := range syms {
		for _, p := range sym.Pins {
			ddx, ddy := p.DirDelta()
			px, py := math.Round(p.X*100)/100, math.Round(p.Y*100)/100

			for _, s := range segs {
				ax, ay := math.Round(s.ax*100)/100, math.Round(s.ay*100)/100
				bx, by := math.Round(s.bx*100)/100, math.Round(s.by*100)/100

				var otherX, otherY float64
				atA := math.Abs(ax-px) < eps && math.Abs(ay-py) < eps
				atB := math.Abs(bx-px) < eps && math.Abs(by-py) < eps
				if atA {
					otherX, otherY = bx, by
				} else if atB {
					otherX, otherY = ax, ay
				} else {
					continue
				}

				// Compute direction from pin to other endpoint.
				dx := otherX - px
				dy := otherY - py
				// Only flag if the wire goes purely opposite (anti-parallel) to pin direction.
				// Anti-parallel means dx/dy sign is opposite to ddx/ddy (for the dominant axis).
				if math.Abs(dx) < eps && math.Abs(dy) < eps {
					continue
				}
				dot := dx*ddx + dy*ddy
				if dot < 0 {
					problems = append(problems, fmt.Sprintf(
						"[WRONG_DIR] Pin %s.%s faces %s but wire exits toward (%.2f,%.2f)",
						sym.Reference, p.Number, directionName(p.Direction), otherX, otherY))
				}
			}
		}
	}
	return problems
}

// detectCrossings finds pairs of wire segments that cross each other
// (not at shared endpoints — those are valid T/X junctions).
func detectCrossings(sch *sexp.Schematic) []string {
	type seg struct{ ax, ay, bx, by float64 }
	var segs []seg
	for _, wire := range sch.Wires() {
		pts := sexp.FindList(wire, "pts")
		if pts == nil {
			continue
		}
		var xys [][2]float64
		for _, xy := range pts.Children {
			if xy.Head() != "xy" {
				continue
			}
			xys = append(xys, [2]float64{
				parseFloat(sexp.AtomValue(xy, 1)),
				parseFloat(sexp.AtomValue(xy, 2)),
			})
		}
		if len(xys) >= 2 {
			segs = append(segs, seg{xys[0][0], xys[0][1], xys[1][0], xys[1][1]})
		}
	}

	const eps = 0.01
	var problems []string
	for i := 0; i < len(segs); i++ {
		for j := i + 1; j < len(segs); j++ {
			a, b := segs[i], segs[j]
			ix, iy, ok := segIntersect(a.ax, a.ay, a.bx, a.by, b.ax, b.ay, b.bx, b.by)
			if !ok {
				continue
			}
			// Shared endpoint = junction, not a crossing.
			sharedEndpoint :=
				(math.Abs(ix-a.ax) < eps && math.Abs(iy-a.ay) < eps) ||
					(math.Abs(ix-a.bx) < eps && math.Abs(iy-a.by) < eps) ||
					(math.Abs(ix-b.ax) < eps && math.Abs(iy-b.ay) < eps) ||
					(math.Abs(ix-b.bx) < eps && math.Abs(iy-b.by) < eps)
			if !sharedEndpoint {
				problems = append(problems, fmt.Sprintf(
					"[CROSSING] Wire (%.2f,%.2f)→(%.2f,%.2f) crosses wire (%.2f,%.2f)→(%.2f,%.2f) at (%.2f,%.2f)",
					a.ax, a.ay, a.bx, a.by, b.ax, b.ay, b.bx, b.by, ix, iy))
			}
		}
	}
	return problems
}

// detectNearMisses finds unconnected pins that have a wire endpoint within
// 0.5 mm — likely a routing gap.
func detectNearMisses(sch *sexp.Schematic, syms []sexp.SchematicSymbol, connSet map[[2]float64]bool) []string {
	const threshold = 0.5
	endpoints := sexp.WireEndpoints(sch)

	var problems []string
	for _, sym := range syms {
		for _, p := range sym.Pins {
			key := [2]float64{math.Round(p.X*100) / 100, math.Round(p.Y*100) / 100}
			if connSet[key] {
				continue
			}
			best := math.MaxFloat64
			var bx, by float64
			for _, ep := range endpoints {
				d := math.Abs(ep[0]-p.X) + math.Abs(ep[1]-p.Y)
				if d < best {
					best = d
					bx, by = ep[0], ep[1]
				}
			}
			if best > 0 && best <= threshold {
				problems = append(problems, fmt.Sprintf(
					"[NEAR_MISS] Pin %s.%s @ (%.2f,%.2f) — unconnected; nearest wire endpoint %.2fmm away at (%.2f,%.2f)",
					sym.Reference, p.Number, p.X, p.Y, best, bx, by))
			}
		}
	}
	return problems
}

// segIntersect returns the intersection point of two line segments, if any.
// Uses the parametric form; returns ok=false for parallel or non-intersecting segments.
func segIntersect(ax, ay, bx, by, cx, cy, dx, dy float64) (ix, iy float64, ok bool) {
	// Direction vectors.
	rx, ry := bx-ax, by-ay
	sx, sy := dx-cx, dy-cy

	denom := cross2d(rx, ry, sx, sy)
	if math.Abs(denom) < 1e-10 {
		return 0, 0, false // parallel
	}
	qpx, qpy := cx-ax, cy-ay
	t := cross2d(qpx, qpy, sx, sy) / denom
	u := cross2d(qpx, qpy, rx, ry) / denom

	const eps = 1e-9
	if t >= -eps && t <= 1+eps && u >= -eps && u <= 1+eps {
		return ax + t*rx, ay + t*ry, true
	}
	return 0, 0, false
}

func cross2d(ax, ay, bx, by float64) float64 {
	return ax*by - ay*bx
}
