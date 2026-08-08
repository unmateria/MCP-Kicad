package tools

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"mcp-kicad/internal/place2/metrics"
	"mcp-kicad/internal/place2/power"
	"mcp-kicad/internal/sexp"
)

// PowerEmitter is the unified entry point for placing `power:*` symbols.
// It embeds the symbol definition, allocates a #PWR reference, snaps the
// position one grid step off the target pin, draws the stub wire and dedupes
// against earlier emissions in the same pass.
//
// Three previously-divergent code paths converge here:
//   - tools/schematic.go::add_power_rail (single-shot user op)
//   - tools/netlist.go::routeNets power shortcut
type PowerEmitter struct {
	env *Env
	sch *sexp.Schematic
	reg *power.Registry
}

// NewPowerEmitter constructs an emitter scoped to one schematic-mutation pass.
func (e *Env) NewPowerEmitter(sch *sexp.Schematic) *PowerEmitter {
	return &PowerEmitter{env: e, sch: sch, reg: power.NewRegistry()}
}

// Emit places one power symbol at `target` (e.g. "U1.VCC"). When a symbol of
// the same lib_id already lives in the same dedup bucket — either because the
// emitter put it there earlier or because it was carried over from the
// schematic — Emit becomes a no-op and returns dedup=true.
func (p *PowerEmitter) Emit(libID, targetRef string) (msg string, ok bool, dedup bool) {
	if !strings.HasPrefix(libID, "power:") {
		return fmt.Sprintf("error: %q is not a power: symbol", libID), false, false
	}
	target, found := sexp.FindPin(p.sch, targetRef)
	if !found {
		return fmt.Sprintf("error: pin %q not found", targetRef), false, false
	}
	if err := p.env.embedLibSymbol(p.sch, libID); err != nil {
		return fmt.Sprintf("error: embed %s: %v", libID, err), false, false
	}
	libDefAngle := sexp.PowerSymbolPinAngle(libSymbolDef(p.sch, libID))

	// Two things make a spot unusable, and both were learned the hard way.
	//
	// A power symbol dropped onto another part's PIN joins that net to the
	// rail — a short with no wire to show for it. The target's own pin is of
	// course not an obstacle: the stub runs from it by definition.
	//
	// And a spot whose BODY would touch an already-placed symbol's body is a
	// lie to the reader: a GND triangle drawn flush against a VCC arrow reads
	// as one connected thing. The nets stay correctly separate — KiCad and the
	// netlist verifier both agree — but a person reviewing the sheet sees
	// GND touching VCC, which is worse than useless. Reported from a real
	// session; the placer only checked pins before.
	netOf := sexp.TracePointNets(p.sch)
	targetNet := netOf[[2]float64{sexp.Round2(target.X), sexp.Round2(target.Y)}]

	blocked := func(x, y float64) bool {
		for _, s := range sexp.ReadSymbols(p.sch) {
			for _, pin := range s.Pins {
				if approxEq(pin.X, x) && approxEq(pin.Y, y) &&
					!(approxEq(pin.X, target.X) && approxEq(pin.Y, target.Y)) {
					return true
				}
			}
		}
		return false
	}
	ugly := func(x, y float64) bool {
		cand := powerBodyBox(x, y)
		// The stub itself must not cut through anything. One GND stub crossing
		// a connector body was enough for the gate to demote the WHOLE GND
		// rail — and since a rail's only wires are its stubs, that stranded
		// eleven power symbols at once and left the sheet labelled instead of
		// drawn. Cheaper to not draw that stub in the first place.
		for _, s := range sexp.ReadSymbols(p.sch) {
			if isPowerLib(s.LibID) {
				continue
			}
			bx1, by1, bx2, by2 := metrics.BodyBBox(s)
			if sexp.SegmentCrossesBox(target.X, target.Y, x, y, bx1, by1, bx2, by2) {
				return true
			}
		}
		for _, s := range sexp.ReadSymbols(p.sch) {
			// Bodies of same-rail power symbols may sit flush — that is the
			// bus alignment the project wants. Anything else must not touch.
			if len(s.Pins) > 0 && isPowerLib(s.LibID) {
				if n := netOf[[2]float64{sexp.Round2(s.Pins[0].X), sexp.Round2(s.Pins[0].Y)}]; n == targetNet && n != "" {
					continue
				}
			}
			x1, y1, x2, y2 := metrics.BodyBBox(s)
			if boxesTouch(cand, [4]float64{x1, y1, x2, y2}) {
				return true
			}
		}
		return false
	}
	dec := power.ComputeClear(libID, target, libDefAngle, "", blocked, ugly)

	if p.reg.Has(libID, dec.X, dec.Y) {
		return fmt.Sprintf("dedup: %s already at (%.2f,%.2f)", libID, dec.X, dec.Y), true, true
	}
	// Also dedup against symbols already in the schematic (carry-over from
	// previous passes that did not use the registry).
	for _, s := range sexp.ReadSymbols(p.sch) {
		if s.LibID != libID {
			continue
		}
		if approxEq(s.X, dec.X) && approxEq(s.Y, dec.Y) {
			p.reg.Mark(libID, dec.X, dec.Y)
			return fmt.Sprintf("dedup: %s %s already placed at (%.2f,%.2f)", libID, s.Reference, dec.X, dec.Y), true, true
		}
	}
	dec.Ref = nextPwrRef(p.sch)
	p.reg.Mark(libID, dec.X, dec.Y)

	pinNums := extractPinNumbers(p.sch, libID, 1)
	p.sch.AddSymbol(sexp.NewSymbolInstance(libID, dec.Ref, dec.PartName, "",
		dec.X, dec.Y, dec.Rotation, 1, pinNums, p.sch.UUID(), false, false, nil))
	// Stub wire from target pin to power-symbol pin (skip when zero-length —
	// e.g. when AnchorOffset returned (0,0) due to unknown direction).
	if dec.StubFrom != dec.StubTo {
		p.sch.AddWire(sexp.NewWire(dec.StubFrom[0], dec.StubFrom[1], dec.StubTo[0], dec.StubTo[1]))
	}
	return fmt.Sprintf("placed %s (%s) at (%.2f,%.2f) rot=%.0f stub %.2fmm",
		dec.Ref, libID, dec.X, dec.Y, dec.Rotation, distance(dec.StubFrom, dec.StubTo)), true, false
}

// ensurePowerFlags adds a Device:PWR_FLAG to every net that has a power-input
// pin but no driver (no power-output/output pin and no existing PWR_FLAG).
// This is the KiCad-standard cure for "Input Power pin not driven" ERC errors
// on rails whose only source is a connector/battery/regulator input. Returns
// the number of flags added. Deterministic: nets are already sorted by
// TraceNets and the attachment pin is chosen by lowest pin ref.
func (e *Env) ensurePowerFlags(sch *sexp.Schematic) int {
	nets := sexp.TraceNets(sch)
	syms := sexp.ReadSymbols(sch)
	em := e.NewPowerEmitter(sch)
	added := 0
	for _, net := range nets {
		if net.Dangling || len(net.Pins) < 2 {
			continue
		}
		hasPowerIn, hasDriver := false, false
		var passives, others []string
		for _, p := range net.Pins {
			switch p.Electrical {
			case "power_in":
				hasPowerIn = true
			case "power_out", "output":
				hasDriver = true
			}
			if p.LibID == "power:PWR_FLAG" {
				hasDriver = true
			}
			if strings.HasPrefix(p.LibID, "power:") {
				continue
			}
			s := p.String()
			others = append(others, s)
			// Prefer a passive 2-pin part (Device:R/C/L/LED): it sits in open
			// space away from the IC power pins, so the flag's stub has clean
			// geometry the gate won't demote.
			if strings.HasPrefix(p.LibID, "Device:") {
				passives = append(passives, s)
			}
		}
		if !hasPowerIn || hasDriver {
			continue
		}
		cands := passives
		if len(cands) == 0 {
			cands = others
		}
		if len(cands) == 0 {
			cands = []string{net.Pins[0].String()}
		}
		sort.Strings(cands)
		if target := bestFlagSpot(sch, syms, cands); target != "" {
			if _, ok, _ := em.EmitPwrFlag(target); ok {
				added++
			}
		}
	}
	return added
}

// EmitPwrFlag places one Device:PWR_FLAG at `targetRef` (e.g. "U1.GND"). A
// PWR_FLAG is a power-OUTPUT stub that tells KiCad's ERC a net is
// intentionally driven, silencing "Input Power pin not driven" errors on
// rails whose only source is a connector/battery/regulator input pin. It is
// placed with the same pin-direction offset + stub convention as a power
// symbol and dedups by snapped position so a net gets at most one flag.
func (p *PowerEmitter) EmitPwrFlag(targetRef string) (msg string, ok bool, dedup bool) {
	// PWR_FLAG lives in the `power` library. TraceNets special-cases it so its
	// pin does NOT implicitly join a global "PWR_FLAG" rail — it connects only
	// to the local net via its stub wire.
	const libID = "power:PWR_FLAG"
	target, found := sexp.FindPin(p.sch, targetRef)
	if !found {
		return fmt.Sprintf("error: pin %q not found", targetRef), false, false
	}
	if err := p.env.embedLibSymbol(p.sch, libID); err != nil {
		return fmt.Sprintf("error: embed %s: %v", libID, err), false, false
	}
	// A PWR_FLAG connects like a power symbol: body OFFSET from the target pin
	// with a short orthogonal stub between them (never coincident with the
	// target pin itself, which would risk shorting two nets whose pins snap
	// together). Two wrinkles are handled explicitly:
	//   1. The offset is PERPENDICULAR to the pin's outgoing direction, so the
	//      stub never overlaps the pin's own rail wire and survives dedup.
	//   2. PWR_FLAG's connection pin is NOT at the symbol origin (KiCad puts it
	//      at local (0, 1.905)), so after dropping the symbol we read back where
	//      its pin actually landed and shift the body so the pin sits exactly at
	//      the stub endpoint.
	tx, ty := sexp.SnapGrid(target.X), sexp.SnapGrid(target.Y)
	dx, dy := perpOffset(target.Direction, 2.54)
	sx, sy := sexp.SnapGrid(tx+dx), sexp.SnapGrid(ty+dy) // stub endpoint = flag pin
	if p.reg.Has(libID, sx, sy) {
		return fmt.Sprintf("dedup: PWR_FLAG already at (%.2f,%.2f)", sx, sy), true, true
	}
	libDefAngle := sexp.PowerSymbolPinAngle(libSymbolDef(p.sch, libID))
	rot := math.Mod(target.Direction-libDefAngle, 360)
	if rot < 0 {
		rot += 360
	}
	p.reg.Mark(libID, sx, sy)
	ref := nextFlagRef(p.sch)
	pinNums := extractPinNumbers(p.sch, libID, 1)
	flag := sexp.NewSymbolInstance(libID, ref, "PWR_FLAG", "",
		sx, sy, rot, 1, pinNums, p.sch.UUID(), false, false, nil)
	// The flag exists for ERC, not for the reader: its ~10 mm of text is
	// exactly the kind of clutter a hand-drawn schematic never shows.
	sexp.HidePropertyText(flag, "Value")
	p.sch.AddSymbol(flag)
	// Shift the body so the flag's real pin lands on the stub endpoint (sx,sy).
	for _, s := range sexp.ReadSymbols(p.sch) {
		if s.Reference == ref && len(s.Pins) > 0 {
			ddx := sx - s.Pins[0].X
			ddy := sy - s.Pins[0].Y
			if ddx != 0 || ddy != 0 {
				// Do NOT re-snap: the pin sits at a non-grid local offset, so
				// snapping the body would push the pin back off the endpoint.
				p.sch.MoveSymbol(ref, s.X+ddx, s.Y+ddy)
			}
			break
		}
	}
	p.sch.AddWire(sexp.NewWire(tx, ty, sx, sy))
	return fmt.Sprintf("placed %s (PWR_FLAG) at (%.2f,%.2f)", ref, sx, sy), true, false
}

// bestFlagSpot returns the candidate pin ("REF.pin") whose flag position has
// the widest clearance to every other symbol body. PWR_FLAG carries ~10 mm of
// text, so it must land where there is room — beside the first pin found it
// routinely overprints a neighbour (decoupling farms especially). Candidates
// must be sorted: ties resolve to the lexicographically first, keeping the
// choice deterministic.
// textReserve is how much room a symbol's reference+value block needs just
// outside its body: fieldMargin plus two lines of text.
const textReserve = 5.4

func bestFlagSpot(sch *sexp.Schematic, syms []sexp.SchematicSymbol, cands []string) string {
	best, bestScore := "", -1.0
	for _, c := range cands {
		pin, ok := sexp.FindPin(sch, c)
		if !ok {
			continue
		}
		dx, dy := perpOffset(pin.Direction, 2.54)
		fx, fy := pin.X+dx, pin.Y+dy
		ref, _, _ := strings.Cut(c, ".")
		score := math.MaxFloat64
		for _, s := range syms {
			if s.Reference == ref {
				continue
			}
			// Measure clearance to the space the part will OCCUPY, not just to
			// its outline: every symbol's reference and value land just outside
			// its body, and the flag is placed before any of that text exists.
			// Scoring against the bare body makes the gap between two
			// capacitors look like the most open space on the sheet — which is
			// exactly where both neighbours' labels have to go, and where a
			// flag blocks a whole decoupling row from labelling itself
			// consistently.
			x1, y1, x2, y2 := metrics.BodyBBox(s)
			x1, y1 = x1-textReserve, y1-textReserve
			x2, y2 = x2+textReserve, y2+textReserve
			ddx := math.Max(0, math.Max(x1-fx, fx-x2))
			ddy := math.Max(0, math.Max(y1-fy, fy-y2))
			if d := math.Hypot(ddx, ddy); d < score {
				score = d
			}
		}
		if score > bestScore {
			bestScore, best = score, c
		}
	}
	return best
}

// perpOffset returns a (dx,dy) offset of length d perpendicular to the pin's
// outgoing direction, off the axis the pin's own wire occupies. Horizontal
// pins get a downward offset. Vertical (or unknown) pins get a LEFTWARD one:
// KiCad puts reference/value text to the right of vertical two-pin parts, so
// going right overprints the neighbour's text (seen on decoupling farms).
func perpOffset(dir, d float64) (dx, dy float64) {
	switch int(math.Round(dir)) % 360 {
	case 0, 180:
		return 0, d
	default:
		return -d, 0
	}
}

// nextFlagRef returns the next free #FLGNN reference designator in `sch`.
func nextFlagRef(sch *sexp.Schematic) string {
	max := 0
	for _, s := range sexp.ReadSymbols(sch) {
		if !strings.HasPrefix(s.Reference, "#FLG") {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(s.Reference, "#FLG%d", &n); err == nil && n > max {
			max = n
		}
	}
	return fmt.Sprintf("#FLG%02d", max+1)
}

// nextPwrRef returns the next free #PWRNN reference designator in `sch`.
func nextPwrRef(sch *sexp.Schematic) string {
	max := 0
	for _, s := range sexp.ReadSymbols(sch) {
		if !strings.HasPrefix(s.Reference, "#PWR") {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(s.Reference, "#PWR%d", &n); err == nil && n > max {
			max = n
		}
	}
	return fmt.Sprintf("#PWR%02d", max+1)
}

func approxEq(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 0.05
}

func distance(a, b [2]float64) float64 {
	dx := a[0] - b[0]
	dy := a[1] - b[1]
	if dx < 0 {
		dx = -dx
	}
	if dy < 0 {
		dy = -dy
	}
	return dx + dy
}

// powerBodyBox is the rectangle a power symbol's graphic occupies once placed:
// measured at 5.08 mm square, centred on its connection pin.
func powerBodyBox(x, y float64) [4]float64 {
	const half = 2.54
	return [4]float64{x - half, y - half, x + half, y + half}
}

// boxesTouch reports overlap OR a shared edge. A shared edge matters here:
// two power symbols exactly flush read as one glyph on paper.
func boxesTouch(a, b [4]float64) bool {
	const eps = 0.01
	return a[0] <= b[2]+eps && b[0] <= a[2]+eps &&
		a[1] <= b[3]+eps && b[1] <= a[3]+eps
}

// isPowerLib reports whether a lib_id names a power-library symbol.
func isPowerLib(libID string) bool {
	return strings.HasPrefix(libID, "power:") || libID == "Device:PWR_FLAG"
}
