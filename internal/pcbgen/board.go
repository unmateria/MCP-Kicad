package pcbgen

import (
	"math"
	"sort"
	"strings"

	"mcp-kicad/internal/sexp"
)

// boardHeader is a two-layer board as KiCad 10 writes one (format 20260206,
// layer table copied from demos/pic_programmer). Everything KiCad leaves out
// of a file it fills with its defaults on load; `kicad-cli pcb upgrade`
// then rewrites the result in KiCad's own canonical form.
const boardHeader = `(kicad_pcb
	(version 20260206)
	(generator "mcp-kicad")
	(generator_version "10.0")
	(general
		(thickness 1.6)
		(legacy_teardrops no)
	)
	(paper "A4")
	(layers
		(0 "F.Cu" signal)
		(2 "B.Cu" signal)
		(9 "F.Adhes" user "F.Adhesive")
		(11 "B.Adhes" user "B.Adhesive")
		(13 "F.Paste" user)
		(15 "B.Paste" user)
		(5 "F.SilkS" user "F.Silkscreen")
		(7 "B.SilkS" user "B.Silkscreen")
		(1 "F.Mask" user)
		(3 "B.Mask" user)
		(17 "Dwgs.User" user "User.Drawings")
		(19 "Cmts.User" user "User.Comments")
		(21 "Eco1.User" user "User.Eco1")
		(23 "Eco2.User" user "User.Eco2")
		(25 "Edge.Cuts" user)
		(27 "Margin" user)
		(31 "F.CrtYd" user "F.Courtyard")
		(29 "B.CrtYd" user "B.Courtyard")
		(35 "F.Fab" user)
		(33 "B.Fab" user)
	)
	(setup
		(pad_to_mask_clearance 0)
		(allow_soldermask_bridges_in_footprints no)
	)
)
`

// BuildBoard serialises the placed parts, the outline and any routing into a
// .kicad_pcb. Tracks and vias carry their net by name, as pads do in KiCad 10
// (measured on demos/pic_programmer).
func BuildBoard(parts []*Part, outline Box, padNets map[string]PadNet, routing *RouteResult, rules RouteRules) (string, error) {
	nodes, err := sexp.Parse(boardHeader)
	if err != nil {
		return "", err
	}
	root := nodes[0]
	sorted := make([]*Part, len(parts))
	copy(sorted, parts)
	sort.SliceStable(sorted, func(i, j int) bool { return refLess(sorted[i].Comp.Ref, sorted[j].Comp.Ref) })
	for _, p := range sorted {
		fp, err := p.FP.Instantiate(p.Comp, p.X, p.Y, p.Rot, padNets)
		if err != nil {
			return "", err
		}
		root.Children = append(root.Children, fp)
	}
	for _, l := range sexp.NewEdgeCutsRect(outline.X1, outline.Y1, outline.W(), outline.H()) {
		l.Children = append(l.Children, sexp.List(sexp.Atom("uuid"), sexp.Str(sexp.NewUUID())))
		root.Children = append(root.Children, l)
	}
	if routing != nil {
		layerName := [2]string{"F.Cu", "B.Cu"}
		for _, sg := range routing.Segments {
			root.Children = append(root.Children, sexp.List(sexp.Atom("segment"),
				sexp.List(sexp.Atom("start"), num(sg.X1), num(sg.Y1)),
				sexp.List(sexp.Atom("end"), num(sg.X2), num(sg.Y2)),
				sexp.List(sexp.Atom("width"), num(sg.Width)),
				sexp.List(sexp.Atom("layer"), sexp.Str(layerName[sg.Layer])),
				sexp.List(sexp.Atom("net"), sexp.Str(sg.Net)),
				sexp.List(sexp.Atom("uuid"), sexp.Str(sexp.NewUUID())),
			))
			if rules.SingleSided && sg.Layer == 0 {
				// The jumper on the top silkscreen: where the wire goes.
				root.Children = append(root.Children, sexp.List(sexp.Atom("gr_line"),
					sexp.List(sexp.Atom("start"), num(sg.X1), num(sg.Y1)),
					sexp.List(sexp.Atom("end"), num(sg.X2), num(sg.Y2)),
					sexp.List(sexp.Atom("stroke"), sexp.List(sexp.Atom("width"), num(0.3)), sexp.List(sexp.Atom("type"), sexp.Atom("solid"))),
					sexp.List(sexp.Atom("layer"), sexp.Str("F.SilkS")),
					sexp.List(sexp.Atom("uuid"), sexp.Str(sexp.NewUUID())),
				))
			}
		}
		if routing.PlaneNet != "" {
			root.Children = append(root.Children, groundPlane(routing.PlaneNet, outline, rules))
		}
		for _, v := range routing.Vias {
			root.Children = append(root.Children, sexp.List(sexp.Atom("via"),
				sexp.List(sexp.Atom("at"), num(v.X), num(v.Y)),
				sexp.List(sexp.Atom("size"), num(rules.Via)),
				sexp.List(sexp.Atom("drill"), num(rules.Drill)),
				sexp.List(sexp.Atom("layers"), sexp.Str("F.Cu"), sexp.Str("B.Cu")),
				sexp.List(sexp.Atom("net"), sexp.Str(v.Net)),
				sexp.List(sexp.Atom("uuid"), sexp.Str(sexp.NewUUID())),
			))
		}
	}
	return sexp.Write([]*sexp.Node{root}), nil
}

// refLess orders references the way a reader expects: C2 before C10.
func refLess(a, b string) bool {
	pa, na := splitRef(a)
	pb, nb := splitRef(b)
	if pa != pb {
		return pa < pb
	}
	if na != nb {
		return na < nb
	}
	return a < b
}

func splitRef(r string) (string, int) {
	i := strings.IndexFunc(r, func(c rune) bool { return c >= '0' && c <= '9' })
	if i < 0 {
		return r, 0
	}
	n := 0
	for _, c := range r[i:] {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return r[:i], n
}

// groundPlane is a pour of net over the whole back layer, its outline the
// board's; KiCad keeps it the edge clearance inside when it fills it. Syntax
// as KiCad 10 writes it (demos/ecc83 after `pcb upgrade`). Left unfilled:
// `kicad-cli pcb drc --refill-zones` fills it with KiCad's own engine.
func groundPlane(net string, o Box, rules RouteRules) *sexp.Node {
	pt := func(x, y float64) *sexp.Node { return sexp.List(sexp.Atom("xy"), num(x), num(y)) }
	clear, gap, spoke := rules.planeGeometry()
	connectPads := sexp.List(sexp.Atom("connect_pads"), sexp.List(sexp.Atom("clearance"), num(clear)))
	if rules.PlaneSolid {
		connectPads = sexp.List(sexp.Atom("connect_pads"), sexp.Atom("yes"), sexp.List(sexp.Atom("clearance"), num(clear)))
	}
	// No sliver of pour thinner than half a track: a milled board cannot
	// isolate one, and it would only peel off.
	minThick := math.Max(0.25, rules.Track/2)
	return sexp.List(sexp.Atom("zone"),
		sexp.List(sexp.Atom("net"), sexp.Str(net)),
		sexp.List(sexp.Atom("layer"), sexp.Str("B.Cu")),
		sexp.List(sexp.Atom("uuid"), sexp.Str(sexp.NewUUID())),
		sexp.List(sexp.Atom("hatch"), sexp.Atom("edge"), sexp.Atom("0.5")),
		connectPads,
		sexp.List(sexp.Atom("min_thickness"), num(minThick)),
		sexp.List(sexp.Atom("fill"), sexp.Atom("yes"),
			sexp.List(sexp.Atom("thermal_gap"), num(gap)),
			sexp.List(sexp.Atom("thermal_bridge_width"), num(spoke)),
			sexp.List(sexp.Atom("island_removal_mode"), sexp.Atom("0"))),
		sexp.List(sexp.Atom("polygon"), sexp.List(sexp.Atom("pts"),
			pt(o.X1, o.Y1), pt(o.X2, o.Y1), pt(o.X2, o.Y2), pt(o.X1, o.Y2))),
	)
}
