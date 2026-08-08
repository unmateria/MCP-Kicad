package tools

import (
	"fmt"
	"sort"
	"strings"

	"mcp-kicad/internal/compile"
	"mcp-kicad/internal/place2/metrics"
	"mcp-kicad/internal/sexp"
)

// Defect kinds reported by VerifyNetlist.
const (
	// DefectSplit: the pins of one declared net did not all end up on the
	// same electrical net. The schematic implements LESS connectivity than
	// the source asked for.
	DefectSplit = "SPLIT"

	// DefectMerged: two declared nets ended up on one electrical net — a
	// short. Geometry alone cannot catch this: once a wire lands on a foreign
	// pin the two nets ARE one net, so the gate sees a single consistent net
	// and ERC stays quiet between passive pins.
	DefectMerged = "MERGED"
)

// NetDefect is one discrepancy between the netlist the source declares and
// the netlist the emitted schematic actually implements.
type NetDefect struct {
	Kind   string
	Net    string
	Detail string
}

func (d NetDefect) String() string {
	return fmt.Sprintf("[%s] %s: %s", d.Kind, d.Net, d.Detail)
}

// VerifyNetlist checks the compiler's central post-condition: the emitted
// schematic implements exactly the netlist the source declares.
//
// Measured against KiCad 10 (kicad-cli sch export netlist), electrical
// connection requires a wire ENDPOINT or an explicit junction — a wire
// merely crossing a pin tip mid-segment connects nothing, and sexp.TraceNets
// agrees with that on all three lab cases. So the netlist we trace here is
// the netlist KiCad will read.
//
// Two directions are checked, because each catches what the other cannot:
//
//   - completeness — every pin of a declared net shares one traced net;
//   - exclusivity  — no two declared nets share a traced net.
//
// Pins the compiler adds on its own (power symbols, PWR_FLAG, template
// internals) are deliberately NOT flagged as intruders: only pins the source
// itself declared elsewhere count, which is exactly the short worth failing
// on and leaves no room for false positives from stamped substructures.
func VerifyNetlist(sch *sexp.Schematic, d *compile.Design) []NetDefect {
	traced := sexp.TraceNets(sch)

	// locate returns the index of the traced net carrying a declared pin, or
	// -1 when no net claims it (electrically isolated).
	locate := func(decl string) int {
		for i, net := range traced {
			for _, p := range net.Pins {
				if p.Matches(decl) {
					return i
				}
			}
		}
		return -1
	}

	names := make([]string, 0, len(d.Nets))
	for name := range d.Nets {
		names = append(names, name)
	}
	sort.Strings(names)

	var defects []NetDefect
	owner := make(map[int]string) // traced net index -> first declared net seen on it

	for _, name := range names {
		pins := d.Nets[name]
		if len(pins) == 0 {
			continue
		}

		where := make([]int, len(pins))
		for i, decl := range pins {
			where[i] = locate(decl)
		}

		home := majorityNet(where)
		var strays []string
		for i, idx := range where {
			if idx == home {
				continue
			}
			switch {
			case idx < 0:
				strays = append(strays, pins[i]+" (isolated)")
			default:
				strays = append(strays, fmt.Sprintf("%s (on %s)", pins[i], traced[idx].Name))
			}
		}
		if len(strays) > 0 {
			detail := fmt.Sprintf("%d declared pins did not join it: %s", len(strays), strings.Join(strays, ", "))
			if home < 0 {
				detail = "no pin of this net is electrically connected"
			}
			defects = append(defects, NetDefect{Kind: DefectSplit, Net: name, Detail: detail})
		}

		// Exclusivity is checked against EVERY traced net this declared net
		// touches, not just the majority one: when a net is split in half the
		// shorted half is often the minority, and claiming only the majority
		// would let exactly the short we are hunting slip through.
		claimed := make(map[int]bool, len(where))
		for _, idx := range where {
			if idx < 0 || claimed[idx] {
				continue
			}
			claimed[idx] = true
			if prev, taken := owner[idx]; taken {
				defects = append(defects, NetDefect{Kind: DefectMerged, Net: name,
					Detail: fmt.Sprintf("shorted to %s — both sit on traced net %q", prev, traced[idx].Name)})
				continue
			}
			owner[idx] = name
		}
	}
	return defects
}

// majorityNet returns the traced-net index most of the pins landed on, so the
// report blames the strays rather than the majority. Ties break toward the
// lowest index; -1 when every pin is isolated.
func majorityNet(where []int) int {
	count := make(map[int]int, len(where))
	for _, idx := range where {
		if idx >= 0 {
			count[idx]++
		}
	}
	best, bestCount := -1, 0
	for _, idx := range where { // iterate the slice, not the map: determinism
		if idx < 0 {
			continue
		}
		if count[idx] > bestCount || (count[idx] == bestCount && idx < best) {
			best, bestCount = idx, count[idx]
		}
	}
	return best
}

// checkPinContacts rejects a placement where two symbols' pins land on the
// same coordinate without the source putting them on one net.
//
// Touching pin tips ARE a connection in KiCad, wire or no wire, so this is a
// short built into the geometry itself. The existing overlap check cannot see
// it: it compares symbol BODIES, and pin tips stick out beyond the body, so
// two parts can touch pins while their bodies stay comfortably apart.
//
// Coincident pins that the source does declare on one net are left alone —
// that is the legitimate "butt the capacitor straight onto the IC pin" the
// format allows, and it needs no wire.
//
// Reported as a compile error rather than a netlist defect because the fix is
// in the source's cell counts, and because at this point the wiring has not
// been drawn yet: everything downstream would be reasoning about a netlist
// the author never asked for.
func checkPinContacts(sch *sexp.Schematic, d *compile.Design) error {
	type pinAt struct {
		ref, number, name string
	}

	// label renders a pin the way a human reads a datasheet: "U1.2 (TRIG)".
	// The bare number sent a real session to the PNG to work out what was
	// touching what.
	label := func(p pinAt) string {
		if p.name == "" || p.name == "~" || p.name == p.number {
			return p.ref + "." + p.number
		}
		return fmt.Sprintf("%s.%s (%s)", p.ref, p.number, p.name)
	}
	at := make(map[[2]float64][]pinAt)
	var order [][2]float64
	for _, sym := range sexp.ReadSymbols(sch) {
		for _, p := range sym.Pins {
			key := [2]float64{sexp.Round2(p.X), sexp.Round2(p.Y)}
			if len(at[key]) == 0 {
				order = append(order, key)
			}
			at[key] = append(at[key], pinAt{sym.Reference, p.Number, p.Name})
		}
	}

	// sameNet reports whether the source declares both pins on one net. The
	// map iteration is safe here: the answer is a yes/no that does not depend
	// on which net is examined first.
	sameNet := func(a, b pinAt) bool {
		for _, pins := range d.Nets {
			foundA, foundB := false, false
			for _, decl := range pins {
				if matchesDecl(decl, a.ref, a.number) {
					foundA = true
				}
				if matchesDecl(decl, b.ref, b.number) {
					foundB = true
				}
			}
			if foundA && foundB {
				return true
			}
		}
		return false
	}

	for _, key := range order { // iterate the slice, not the map: determinism
		pins := at[key]
		for i := 0; i < len(pins); i++ {
			for j := i + 1; j < len(pins); j++ {
				if pins[i].ref == pins[j].ref || sameNet(pins[i], pins[j]) {
					continue
				}
				return fmt.Errorf(
					"%s and %s both sit at (%.2f, %.2f): touching pins are one net in KiCad, "+
						"but the source declares them apart — change the dir/cells that places them",
					label(pins[i]), label(pins[j]), key[0], key[1])
			}
		}
	}
	return nil
}

// matchesDecl reports whether a declared "REF.pin" / "REF.unit.pin" names the
// given placed pin.
func matchesDecl(decl, ref, number string) bool {
	return sexp.PinRef{Reference: ref, PinNumber: number}.Matches(decl)
}

// flushPowerPairs reports pairs of power symbols of DIFFERENT rails whose
// bodies are drawn touching.
//
// The netlist stays correct — KiCad and VerifyNetlist both agree the nets are
// separate — but on paper a GND triangle flush against a VCC arrow reads as
// one connected thing, and a reviewer will stop and ask. The placer avoids it
// when it can; when the two pins face each other on one line there is nowhere
// left to go, and then the fix belongs in the source: put more cells between
// the parts. Silence would leave the author staring at a schematic that looks
// shorted and verifies clean.
func flushPowerPairs(sch *sexp.Schematic) []string {
	type placed struct {
		ref, rail      string
		x1, y1, x2, y2 float64
	}
	netOf := sexp.TracePointNets(sch)

	var syms []placed
	for _, s := range sexp.ReadSymbols(sch) {
		if !strings.HasPrefix(s.LibID, "power:") || len(s.Pins) == 0 {
			continue
		}
		rail := netOf[[2]float64{sexp.Round2(s.Pins[0].X), sexp.Round2(s.Pins[0].Y)}]
		x1, y1, x2, y2 := metrics.BodyBBox(s)
		syms = append(syms, placed{s.Reference, rail, x1, y1, x2, y2})
	}

	const eps = 0.01
	var out []string
	for i := 0; i < len(syms); i++ {
		for j := i + 1; j < len(syms); j++ {
			a, b := syms[i], syms[j]
			if a.rail == b.rail {
				continue // same rail flush is the intended bus alignment
			}
			if a.x1 <= b.x2+eps && b.x1 <= a.x2+eps && a.y1 <= b.y2+eps && b.y1 <= a.y2+eps {
				out = append(out, fmt.Sprintf("%s (%s) and %s (%s)", a.ref, a.rail, b.ref, b.rail))
			}
		}
	}
	sort.Strings(out)
	return out
}
