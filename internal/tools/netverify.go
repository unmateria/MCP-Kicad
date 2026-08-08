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

	// DefectOrphan: a symbol the COMPILER added — a power symbol or a
	// PWR_FLAG — ended up connected to nothing.
	//
	// This is our bug, never the author's, and it was invisible: the checks
	// above only look at pins the source declares, and nobody declares a
	// #PWR. A real session hit it and got "netlist: verified" alongside eight
	// ERC errors, which is the worst possible outcome — two verification
	// layers of the same system disagreeing, with the reassuring one printed
	// as the success criterion.
	DefectOrphan = "ORPHAN"
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

	defects = append(defects, orphanPowerSymbols(sch)...)
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

// orphanPowerSymbols finds power symbols and PWR_FLAGs whose pin touches
// nothing at all.
//
// They are emitted with a stub wire to the pin they serve, so an orphan means
// the stub never landed — the symbol was pushed aside to dodge an obstacle, or
// deduplicated against another symbol that does not actually reach this pin.
//
// The test is PHYSICAL contact, not net membership, because KiCad joins every
// power:GND into one net by name: asking whether the net is populated always
// says yes and hides the fault. KiCad's own ERC asks the same physical
// question and reports "Pin not connected" — and when the floating symbol is
// a PWR_FLAG, its rail loses its driver too ("Input Power pin not driven").
//
// This is our bug, never the author's: nobody declares a #PWR in the source,
// so nothing else in this file was looking at them.
func orphanPowerSymbols(sch *sexp.Schematic) []NetDefect {
	// Every point a wire ends at, and every non-power pin position.
	touch := make(map[[2]float64]int)
	for _, w := range sch.Wires() {
		if ax, ay, bx, by, ok := metrics.WireCoords(w); ok {
			touch[[2]float64{sexp.Round2(ax), sexp.Round2(ay)}]++
			touch[[2]float64{sexp.Round2(bx), sexp.Round2(by)}]++
		}
	}
	for _, sym := range sexp.ReadSymbols(sch) {
		if strings.HasPrefix(sym.LibID, "power:") {
			continue
		}
		for _, p := range sym.Pins {
			touch[[2]float64{sexp.Round2(p.X), sexp.Round2(p.Y)}]++
		}
	}

	var out []NetDefect
	for _, sym := range sexp.ReadSymbols(sch) {
		if !strings.HasPrefix(sym.LibID, "power:") || len(sym.Pins) == 0 {
			continue
		}
		key := [2]float64{sexp.Round2(sym.Pins[0].X), sexp.Round2(sym.Pins[0].Y)}
		if touch[key] > 0 {
			continue
		}
		out = append(out, NetDefect{
			Kind: DefectOrphan,
			Net:  sym.Reference,
			Detail: fmt.Sprintf("%s (%s) at (%.2f, %.2f) touches nothing — its stub never reached the pin it serves",
				sym.Reference, sym.LibID, sym.Pins[0].X, sym.Pins[0].Y),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Detail < out[j].Detail })
	return out
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
				// Say what to change, not just that something is wrong. The
				// author cannot know a symbol's internal pin span from the
				// source — a resistor at 90° puts its far pin 7.62 mm away —
				// so "adjust dir/cells" means guess and recompile. One cell
				// more than the collision separates them.
				return fmt.Errorf(
					"%s and %s both sit at (%.2f, %.2f): touching pins are one net in KiCad, "+
						"but the source declares them apart — add 1 cell to whichever of them you placed "+
						"with `place` (the far pin of a part sits its own body-length away from the pin you anchored)",
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

// dropOrphanPowerSymbols removes power symbols and PWR_FLAGs left touching
// nothing, and reports how many went.
//
// The gate demotes a net by deleting its wires and replacing them with labels.
// When that net carried a power symbol's stub, the stub goes too and the
// symbol is stranded: connectivity survives (labels plus KiCad's implicit
// power nets), which is why the netlist still verifies, but KiCad's ERC counts
// every stranded pin as unconnected — and a stranded PWR_FLAG takes its rail's
// driver with it. That is how a real session got "verified" and eight ERC
// errors at once.
//
// Deleting them is right rather than merely convenient: after a demotion the
// pin they served already carries a net label, so the symbol contributes
// nothing but a floating glyph.
func dropOrphanPowerSymbols(sch *sexp.Schematic) int {
	orphans := orphanPowerSymbols(sch)
	if len(orphans) == 0 {
		return 0
	}
	doomed := make(map[string]bool, len(orphans))
	for _, o := range orphans {
		doomed[o.Net] = true // Net carries the reference for this defect kind
	}

	kept := sch.Root().Children[:0]
	removed := 0
	for _, c := range sch.Root().Children {
		if c.Head() == "symbol" {
			ref := ""
			for _, ch := range c.Children {
				if ch.Head() == "property" && sexp.StringValue(ch, 1) == "Reference" {
					ref = sexp.StringValue(ch, 2)
					break
				}
			}
			if doomed[ref] {
				removed++
				continue
			}
		}
		kept = append(kept, c)
	}
	sch.Root().Children = kept
	return removed
}

// checkPowerRails refuses two declared power nets that point at the same power
// symbol.
//
// A KiCad power symbol names its net after its OWN pin, not after the key it
// was given in the source. So mapping both "GND" and "GND_PWR" to power:GND
// produces two symbols that KiCad silently merges into one "GND" net — the
// isolated ground of an opto-coupled design quietly bonded to the logic
// ground. The schematic verifies, the ERC is clean, and the isolation is gone.
//
// Nothing downstream can recover the intent, so it is refused here, where the
// author can still say what they meant.
func checkPowerRails(d *compile.Design) error {
	byLib := map[string][]string{}
	var libs []string
	for name := range d.PowerNets {
		lib := d.PowerNets[name]
		if len(byLib[lib]) == 0 {
			libs = append(libs, lib)
		}
		byLib[lib] = append(byLib[lib], name)
	}
	sort.Strings(libs)
	for _, lib := range libs {
		names := byLib[lib]
		if len(names) < 2 {
			continue
		}
		sort.Strings(names)
		return fmt.Errorf(
			"power_nets maps %s to %q — KiCad names a power net after the SYMBOL, "+
				"so these would merge into one net and the separation you declared would vanish. "+
				"Give each rail its own symbol (power:GNDPWR, power:GNDA, power:VDD…), or drop the "+
				"extra rail from power_nets and let it be an ordinary named net carried by labels",
			strings.Join(names, " and "), lib)
	}
	return nil
}

// demotionAdvice explains a gate demotion the author cannot fix with spacing.
//
// When a net's pins point AWAY from each other, no distance makes the wire
// straight — it has to loop around one of the parts, the gate refuses it, and
// the connection becomes a label. A relay whose contacts face up while the
// connector's face left is the classic case: a session tried 3, 6, 12 and 26
// cells before concluding, correctly, that two of the three contacts could
// never be wired in line. The fix is rotation or mirror, not millimetres, and
// saying so is the difference between one recompile and four.
func demotionAdvice(sch *sexp.Schematic, netName string) string {
	var pins []sexp.PinInfo
	for _, net := range sexp.TraceNets(sch) {
		if net.Name != netName {
			continue
		}
		for _, pr := range net.Pins {
			for _, sym := range sexp.ReadSymbols(sch) {
				if sym.Reference != pr.Reference {
					continue
				}
				for _, p := range sym.Pins {
					if p.Number == pr.PinNumber {
						pins = append(pins, p)
					}
				}
			}
		}
	}
	if len(pins) < 2 {
		return ""
	}
	// Two pins that face away from EACH OTHER cannot be joined by a straight
	// or L-shaped wire at any distance: both wires leave in the wrong
	// direction and something has to loop back.
	for i := 0; i < len(pins); i++ {
		for j := i + 1; j < len(pins); j++ {
			a, b := pins[i], pins[j]
			adx, ady := a.DirDelta()
			bdx, bdy := b.DirDelta()
			aAway := adx*(b.X-a.X)+ady*(b.Y-a.Y) < 0
			bAway := bdx*(a.X-b.X)+bdy*(a.Y-b.Y) < 0
			if aAway && bAway {
				return fmt.Sprintf(
					"pins %s and %s face away from each other, so no spacing makes this wire straight — rotate or mirror one of the parts",
					a.Number, b.Number)
			}
		}
	}
	return ""
}
