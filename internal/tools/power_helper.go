package tools

import (
	"fmt"
	"math"
	"strings"

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
//   - tools/schematic.go relayout pwrLinks loop
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
// pre-relayout schematic — Emit becomes a no-op and returns dedup=true.
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
	dec := power.Compute(libID, target, libDefAngle, "")

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
	em := e.NewPowerEmitter(sch)
	added := 0
	for _, net := range nets {
		if net.Dangling || len(net.Pins) < 2 {
			continue
		}
		hasPowerIn, hasDriver := false, false
		var passiveTgt, anyTgt string
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
			if anyTgt == "" || s < anyTgt {
				anyTgt = s
			}
			// Prefer a passive 2-pin part (Device:R/C/L/LED): it sits in open
			// space away from the IC power pins, so the flag's stub has clean
			// geometry the gate won't demote.
			if strings.HasPrefix(p.LibID, "Device:") {
				if passiveTgt == "" || s < passiveTgt {
					passiveTgt = s
				}
			}
		}
		if !hasPowerIn || hasDriver {
			continue
		}
		target := passiveTgt
		if target == "" {
			target = anyTgt
		}
		if target == "" {
			target = net.Pins[0].String()
		}
		if _, ok, _ := em.EmitPwrFlag(target); ok {
			added++
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
	p.sch.AddSymbol(sexp.NewSymbolInstance(libID, ref, "PWR_FLAG", "",
		sx, sy, rot, 1, pinNums, p.sch.UUID(), false, false, nil))
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

// perpOffset returns a (dx,dy) offset of length d perpendicular to the pin's
// outgoing direction. Horizontal pins get a downward offset, vertical (or
// unknown) pins a rightward one — off the axis the pin's own wire occupies.
func perpOffset(dir, d float64) (dx, dy float64) {
	switch int(math.Round(dir)) % 360 {
	case 0, 180:
		return 0, d
	default:
		return d, 0
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
