package tools

import (
	"fmt"
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
