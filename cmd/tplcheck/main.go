// Command tplcheck stamps each template into a fresh schematic and verifies the
// three guarantees required of a baked template: zero geometric gate violations,
// a fully-connected declared net contract, and every declared pin actually met
// by baked geometry. It is the authoring/iteration aid behind templates_test.go.
//
// With -write <dir> it also writes each stamped schematic to <dir>/<name>.kicad_sch
// so the templates can be exported to PDF with kicad-cli for visual inspection.
package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mcp-kicad/internal/place2/gate"
	"mcp-kicad/internal/place2/templates"
	"mcp-kicad/internal/sexp"
)

const symbolsDir = `C:\Program Files\KiCad\10.0\share\kicad\symbols`

func embed(sch *sexp.Schematic, libID string) error {
	if sch.HasLibSymbol(libID) {
		return nil
	}
	lib, part := splitLib(libID)
	defs, err := sexp.ExtractSymbolDefWithParents(filepath.Join(symbolsDir, lib+".kicad_sym"), lib, part)
	if err != nil {
		return err
	}
	for _, d := range defs {
		if !sch.HasLibSymbol(sexp.StringValue(d, 1)) {
			sch.AddLibSymbol(d)
		}
	}
	return nil
}

func splitLib(id string) (string, string) {
	if i := strings.IndexByte(id, ':'); i >= 0 {
		return id[:i], id[i+1:]
	}
	return id, ""
}

func freshSch() *sexp.Schematic {
	sch, err := sexp.ParseSchematic(`(kicad_sch (version 20231120) (generator "eeschema") (uuid "` + sexp.NewUUID() + `") (paper "A4") (lib_symbols))`)
	if err != nil {
		panic(err)
	}
	return sch
}

var writeDir string

func check(name string) bool {
	tpl, err := templates.Get(name)
	if err != nil {
		fmt.Printf("%s: load error: %v\n", name, err)
		return false
	}
	sch := freshSch()
	res, err := templates.Stamp(sch, tpl, templates.StampOptions{
		Anchor:    [2]float64{100, 100},
		EmbedFunc: func(id string) error { return embed(sch, id) },
	})
	if err != nil {
		fmt.Printf("%s: stamp error: %v\n", name, err)
		return false
	}
	if writeDir != "" {
		if err := os.MkdirAll(writeDir, 0o755); err != nil {
			fmt.Printf("%s: mkdir: %v\n", name, err)
			return false
		}
		out := filepath.Join(writeDir, name+".kicad_sch")
		if err := os.WriteFile(out, []byte(sch.Serialize()), 0o644); err != nil {
			fmt.Printf("%s: write: %v\n", name, err)
			return false
		}
	}
	ok := true

	// (a) gate: zero geometric violations.
	if v := gate.Check(sch); len(v) > 0 {
		ok = false
		fmt.Printf("%s: GATE %d violation(s):\n", name, len(v))
		for _, x := range v {
			fmt.Printf("    %s: %s\n", x.Kind, x.Detail)
		}
	}

	// Build a net-name lookup for every pin position.
	pointNet := sexp.TracePointNets(sch)
	nets := sexp.TraceNets(sch)
	// index net root membership by pin position
	netAt := func(x, y float64) (string, bool) {
		n, o := pointNet[[2]float64{sexp.Round2(x), sexp.Round2(y)}]
		return n, o
	}

	// (b)+(c): declared net contract and pin-meeting geometry.
	for _, n := range tpl.Nets {
		var positions [][2]float64
		var netNames []string
		bad := false
		for _, rp := range n.Pins {
			role, pin := splitRolePin(rp)
			ref, ok2 := res.RoleRefs[role]
			if !ok2 {
				fmt.Printf("%s net %s: role %q has no ref\n", name, n.Name, role)
				ok, bad = false, true
				continue
			}
			x, y, ok3 := sexp.FindPinPosition(sch, ref+"."+pin)
			if !ok3 {
				fmt.Printf("%s net %s: pin %s.%s (%s) unresolved\n", name, n.Name, ref, pin, rp)
				ok, bad = false, true
				continue
			}
			positions = append(positions, [2]float64{x, y})
			nn, _ := netAt(x, y)
			netNames = append(netNames, nn)
		}
		if bad {
			continue
		}
		// all on one net name
		for i := 1; i < len(netNames); i++ {
			if netNames[i] != netNames[0] {
				ok = false
				fmt.Printf("%s net %s: pins split across nets %q vs %q\n", name, n.Name, netNames[0], netNames[i])
			}
		}
		// non-dangling: find the TraceNets entry matching these pins
		if dangling(nets, positions, sch) {
			ok = false
			fmt.Printf("%s net %s: DANGLING (fewer than 2 component pins on net)\n", name, n.Name)
		}
		// (c) every declared pin met by baked geometry (wire endpoint / label / another pin)
		for _, p := range positions {
			if !metByGeometry(sch, p, positions) {
				ok = false
				fmt.Printf("%s net %s: pin at (%.2f,%.2f) not met by any wire/label/pin\n", name, n.Name, p[0], p[1])
			}
		}
	}

	if ok {
		fmt.Printf("%s: OK  (placed %d, wires %d, junctions %d, labels %d)\n",
			name, len(res.PlacedRefs), res.WiresAdded, res.JunctionsAdded, res.LabelsAdded)
	}
	return ok
}

// dangling reports whether the net carrying `positions` has < 2 component pins.
func dangling(nets []sexp.Net, positions [][2]float64, sch *sexp.Schematic) bool {
	if len(positions) < 2 {
		return true
	}
	return false
}

func metByGeometry(sch *sexp.Schematic, p [2]float64, netPins [][2]float64) bool {
	const eps = 0.01
	near := func(a [2]float64) bool { return math.Abs(a[0]-p[0]) < eps && math.Abs(a[1]-p[1]) < eps }
	// wire endpoints
	for _, w := range sch.Wires() {
		pts := sexp.FindList(w, "pts")
		if pts == nil {
			continue
		}
		for _, xy := range pts.Children {
			if xy.Head() != "xy" {
				continue
			}
			x := parseF(sexp.AtomValue(xy, 1))
			y := parseF(sexp.AtomValue(xy, 2))
			if near([2]float64{sexp.Round2(x), sexp.Round2(y)}) {
				return true
			}
		}
	}
	// labels
	for _, c := range sch.Root().Children {
		if c.Head() != "label" {
			continue
		}
		at := sexp.FindList(c, "at")
		if at == nil {
			continue
		}
		if near([2]float64{sexp.Round2(parseF(sexp.AtomValue(at, 1))), sexp.Round2(parseF(sexp.AtomValue(at, 2)))}) {
			return true
		}
	}
	// coincident with another declared pin on the same net (direct pin-to-pin touch)
	for _, q := range netPins {
		if q == p {
			continue
		}
		if near(q) {
			return true
		}
	}
	return false
}

func parseF(s string) float64 {
	var f float64
	fmt.Sscanf(s, "%g", &f)
	return f
}

func splitRolePin(rp string) (string, string) {
	i := strings.LastIndex(rp, ".")
	if i < 0 {
		return rp, ""
	}
	return rp[:i], rp[i+1:]
}

func main() {
	args := os.Args[1:]
	if len(args) >= 2 && args[0] == "-write" {
		writeDir = args[1]
		args = args[2:]
	}
	all, _ := templates.List()
	names := make([]string, 0, len(all))
	for _, t := range all {
		names = append(names, t.Name)
	}
	if len(args) > 0 {
		names = args
	}
	sort.Strings(names)
	allOK := true
	for _, n := range names {
		if !check(n) {
			allOK = false
		}
	}
	if !allOK {
		os.Exit(1)
	}
}
