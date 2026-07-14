package templates

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"mcp-kicad/internal/place2/gate"
	"mcp-kicad/internal/sexp"
)

func TestListReturnsAllTemplates(t *testing.T) {
	ts, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{
		"i2c_pullups",
		"mcu_minimal_atmega328",
		"opamp_noninverting",
		"voltage_divider",
		"voltage_regulator_linear",
	}
	if len(ts) != len(want) {
		t.Fatalf("List returned %d templates, want %d: %+v", len(ts), len(want), ts)
	}
	for i, w := range want {
		if ts[i].Name != w {
			t.Errorf("templates[%d] = %q, want %q", i, ts[i].Name, w)
		}
	}
}

func TestGetByName(t *testing.T) {
	got, err := Get("opamp_noninverting")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Components) < 3 {
		t.Errorf("opamp_noninverting components = %d, want >= 3", len(got.Components))
	}
	if len(got.Nets) == 0 {
		t.Errorf("opamp_noninverting nets is empty")
	}
}

// candidateSymbolDirs are the KiCad global symbol library locations tried, in
// order, by the self-verifying stamp test. KiCad 10 first (the target), then 9.
func candidateSymbolDirs() []string {
	return []string{
		`C:\Program Files\KiCad\10.0\share\kicad\symbols`,
		`C:\Program Files\KiCad\9.0\share\kicad\symbols`,
	}
}

func symbolsDir() string {
	for _, d := range candidateSymbolDirs() {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			return d
		}
	}
	return ""
}

func testEmbed(sch *sexp.Schematic, dir, libID string) error {
	if sch.HasLibSymbol(libID) {
		return nil
	}
	lib, part := libID, ""
	if i := strings.IndexByte(libID, ':'); i >= 0 {
		lib, part = libID[:i], libID[i+1:]
	}
	defs, err := sexp.ExtractSymbolDefWithParents(filepath.Join(dir, lib+".kicad_sym"), lib, part)
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

func freshTestSch(t *testing.T) *sexp.Schematic {
	t.Helper()
	sch, err := sexp.ParseSchematic(`(kicad_sch (version 20231120) (generator "eeschema") (uuid "` + sexp.NewUUID() + `") (paper "A4") (lib_symbols))`)
	if err != nil {
		t.Fatalf("parse fresh schematic: %v", err)
	}
	return sch
}

// TestStampedTemplatesSatisfyContract is the guarantee that makes a bad template
// impossible to ship: every template, stamped into a fresh schematic, must have
// (a) zero geometric gate violations, (b) every declared net fully connected on
// one non-dangling net, and (c) every declared pin met by baked geometry at
// exactly its FindPinPosition coordinate.
func TestStampedTemplatesSatisfyContract(t *testing.T) {
	dir := symbolsDir()
	if dir == "" {
		t.Skip("no KiCad global symbol library found; skipping stamped-template verification")
	}
	all, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, tpl := range all {
		tpl := tpl
		t.Run(tpl.Name, func(t *testing.T) {
			sch := freshTestSch(t)
			res, err := Stamp(sch, tpl, StampOptions{
				Anchor:    [2]float64{100, 100},
				EmbedFunc: func(id string) error { return testEmbed(sch, dir, id) },
			})
			if err != nil {
				t.Fatalf("stamp: %v", err)
			}

			// (a) geometric gate: zero violations.
			if v := gate.Check(sch); len(v) > 0 {
				for _, x := range v {
					t.Errorf("gate violation %s: %s", x.Kind, x.Detail)
				}
			}

			pointNet := sexp.TracePointNets(sch)

			// (b)+(c): declared net contract.
			netRootName := map[string]bool{} // ensure declared nets stay distinct
			for _, n := range tpl.Nets {
				// A declared net must name at least two pins; combined with the
				// "all on one net" check below this guarantees it is non-dangling
				// (>= 2 component pins on the same net).
				if len(n.Pins) < 2 {
					t.Errorf("net %s: declares %d pin(s); a connected net needs >= 2", n.Name, len(n.Pins))
					continue
				}
				var netNames []string
				var positions [][2]float64
				for _, rp := range n.Pins {
					role, pin := splitRolePin(rp)
					ref, ok := res.RoleRefs[role]
					if !ok {
						t.Fatalf("net %s: role %q has no allocated reference", n.Name, role)
					}
					x, y, ok := sexp.FindPinPosition(sch, ref+"."+pin)
					if !ok {
						t.Fatalf("net %s: pin %s.%s (%s) does not resolve", n.Name, ref, pin, rp)
					}
					positions = append(positions, [2]float64{x, y})
					netNames = append(netNames, pointNet[[2]float64{sexp.Round2(x), sexp.Round2(y)}])
				}
				// all declared pins on one net name
				for i := 1; i < len(netNames); i++ {
					if netNames[i] != netNames[0] {
						t.Errorf("net %s: pins split across nets %q and %q", n.Name, netNames[0], netNames[i])
					}
				}
				// declared nets are mutually distinct
				if netNames[0] != "" {
					if netRootName[netNames[0]] {
						t.Errorf("net %s: shares actual net %q with another declared net", n.Name, netNames[0])
					}
					netRootName[netNames[0]] = true
				}
				// (c) each declared pin met by baked geometry at its exact position
				for i, p := range positions {
					if !metByGeometry(sch, p, positions) {
						t.Errorf("net %s: pin %s at (%.3f,%.3f) not met by any wire endpoint/label/coincident pin",
							n.Name, n.Pins[i], p[0], p[1])
					}
				}
			}
		})
	}
}

func metByGeometry(sch *sexp.Schematic, p [2]float64, netPins [][2]float64) bool {
	const eps = 0.01
	near := func(x, y float64) bool {
		return math.Abs(sexp.Round2(x)-p[0]) < eps && math.Abs(sexp.Round2(y)-p[1]) < eps
	}
	for _, w := range sch.Wires() {
		pts := sexp.FindList(w, "pts")
		if pts == nil {
			continue
		}
		for _, xy := range pts.Children {
			if xy.Head() != "xy" {
				continue
			}
			if near(parseTestF(sexp.AtomValue(xy, 1)), parseTestF(sexp.AtomValue(xy, 2))) {
				return true
			}
		}
	}
	for _, c := range sch.Root().Children {
		if c.Head() != "label" {
			continue
		}
		at := sexp.FindList(c, "at")
		if at == nil {
			continue
		}
		if near(parseTestF(sexp.AtomValue(at, 1)), parseTestF(sexp.AtomValue(at, 2))) {
			return true
		}
	}
	// A pin can also be met by another pin at the same point (direct pin overlap).
	for _, q := range netPins {
		if q != p && math.Abs(q[0]-p[0]) < eps && math.Abs(q[1]-p[1]) < eps {
			return true
		}
	}
	return false
}

func parseTestF(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}
