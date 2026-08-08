// Package power centralizes placement of KiCad `power:*` symbols (VCC, GND,
// +5V, ±12V, …). Three previously-divergent code paths in tools/schematic.go
// and tools/netlist.go converge here so that:
//
//  1. There is exactly one #PWR symbol per (rail, snapped position).
//  2. The symbol sits one grid step (2.54 mm) away from its target pin in
//     the pin's outgoing direction, leaving a visible stub wire.
//  3. Symbols sharing the same rail and a similar Y can be aligned into a
//     horizontal bus.
//
// The package is pure: it computes placements and reports duplicates but does
// not embed library symbol definitions or write to the AST itself. Call sites
// translate Decisions into NewSymbolInstance + AddWire pairs.
package power

import (
	"math"
	"strings"

	"mcp-kicad/internal/sexp"
)

// PinStubLen is the wire stub length between a power symbol and its target
// pin, in millimetres. One KiCad grid step.
const PinStubLen = 2.54

// SnapStep is the grid used for dedup. Two power symbols whose target pins
// snap to the same (libID, x, y) bucket are considered duplicates.
const SnapStep = 1.27

// Decision describes one power-symbol placement to be emitted by the caller.
type Decision struct {
	LibID    string  // e.g. "power:GND"
	PartName string  // e.g. "GND"
	Ref      string  // e.g. "#PWR03"
	TargetX  float64 // pin tip the symbol attaches to (also stub start)
	TargetY  float64
	X        float64 // symbol body position (also pin tip of the power symbol)
	Y        float64
	Rotation float64
	StubFrom [2]float64 // wire endpoint at the target pin
	StubTo   [2]float64 // wire endpoint at the power symbol pin
}

// AnchorOffset returns the (dx, dy) the power symbol body should sit at
// relative to its target pin, in screen coordinates. Positive supplies sit
// in the direction of the pin's OUTGOING wire; if the pin has no resolvable
// direction we fall back to the rail-family default (positive → above,
// ground → below).
func AnchorOffset(partName string, dir float64) (dx, dy float64) {
	pinDx, pinDy := dirDelta(dir)
	if pinDx == 0 && pinDy == 0 {
		switch {
		case isPositiveRail(partName):
			return 0, -PinStubLen
		case isGroundRail(partName):
			return 0, PinStubLen
		default:
			return 0, -PinStubLen
		}
	}
	return pinDx * PinStubLen, pinDy * PinStubLen
}

// Compute builds a Decision for a target pin. The required rotation makes the
// power symbol's pin face back toward the target.
func Compute(libID string, target sexp.PinInfo, libDefPinAngle float64, ref string) Decision {
	return ComputeClear(libID, target, libDefPinAngle, ref, nil, nil)
}

// maxStubSteps bounds how far a power symbol may back away from its pin
// looking for free space. Beyond three grid steps the stub reads as a wire in
// its own right rather than as the symbol's own tail.
const maxStubSteps = 3

// ComputeClear is Compute with somewhere to go when the spot is taken, and it
// distinguishes two very different kinds of "taken".
//
// `blocked` means placing here would be WRONG: another net's pin is there, and
// touching it grounds that net with no wire to show for it. Never acceptable.
//
// `ugly` means placing here would be UNREADABLE but correct: the body would sit
// flush against another symbol, so a GND triangle ends up drawn against a VCC
// arrow and reads as one connected thing.
//
// Keeping them apart matters. When they were one predicate, a crowded pin had
// every candidate rejected for being ugly, the search fell through to its
// last resort — and the last resort landed on a foreign pin, turning a
// cosmetic problem into four shorted nets. Now the search degrades in the
// right order: clean, then merely ugly, then (only if there is no choice) the
// nearest spot, where VerifyNetlist will catch what geometry could not avoid.
func ComputeClear(libID string, target sexp.PinInfo, libDefPinAngle float64, ref string,
	blocked, ugly func(x, y float64) bool) Decision {

	partName := strings.TrimPrefix(libID, "power:")
	dx, dy := AnchorOffset(partName, target.Direction)

	bx := sexp.SnapGrid(target.X + dx)
	by := sexp.SnapGrid(target.Y + dy)

	isBlocked := func(x, y float64) bool { return blocked != nil && blocked(x, y) }
	isUgly := func(x, y float64) bool { return ugly != nil && ugly(x, y) }

	if blocked != nil || ugly != nil {
		chosen := false
		// Pass 1: a spot that is neither wrong nor ugly.
		for step := 1; step <= maxStubSteps && !chosen; step++ {
			cx := sexp.SnapGrid(target.X + dx*float64(step))
			cy := sexp.SnapGrid(target.Y + dy*float64(step))
			if !isBlocked(cx, cy) && !isUgly(cx, cy) {
				bx, by, chosen = cx, cy, true
			}
		}
		// Pass 2: correctness first — accept ugly rather than short a net.
		for step := 1; step <= maxStubSteps && !chosen; step++ {
			cx := sexp.SnapGrid(target.X + dx*float64(step))
			cy := sexp.SnapGrid(target.Y + dy*float64(step))
			if !isBlocked(cx, cy) {
				bx, by, chosen = cx, cy, true
			}
		}
	}
	// rot = (target outgoing direction) − (power pin local angle), mod 360
	r := math.Mod(target.Direction-libDefPinAngle, 360)
	if r < 0 {
		r += 360
	}
	return Decision{
		LibID:    libID,
		PartName: partName,
		Ref:      ref,
		TargetX:  target.X,
		TargetY:  target.Y,
		X:        bx,
		Y:        by,
		Rotation: r,
		StubFrom: [2]float64{target.X, target.Y},
		StubTo:   [2]float64{bx, by},
	}
}

// dirDelta mirrors PinInfo.DirDelta but takes the angle directly so the
// caller can use it before resolving a PinInfo.
func dirDelta(dir float64) (dx, dy float64) {
	switch int(math.Round(dir)) % 360 {
	case 0:
		return 1, 0
	case 90:
		return 0, -1
	case 180:
		return -1, 0
	case 270:
		return 0, 1
	}
	return 0, 0
}

// isPositiveRail / isGroundRail mirror rules.power without taking the dep.
func isPositiveRail(name string) bool {
	n := strings.ToUpper(strings.TrimSpace(name))
	switch n {
	case "VCC", "VDD", "VAA", "AVCC", "AVDD", "VBUS":
		return true
	}
	if strings.HasPrefix(n, "+") || strings.HasPrefix(n, "VCC") || strings.HasPrefix(n, "VDD") {
		return true
	}
	return false
}

func isGroundRail(name string) bool {
	n := strings.ToUpper(strings.TrimSpace(name))
	switch n {
	case "GND", "GND1", "GND2", "GNDA", "GNDD", "GNDPWR", "GNDREF",
		"GNDS", "EARTH", "0V", "VSS", "VEE":
		return true
	}
	if strings.HasPrefix(n, "-") {
		return true
	}
	return false
}

// IsPositive / IsGround are exported helpers for call sites.
func IsPositive(partName string) bool { return isPositiveRail(partName) }
func IsGround(partName string) bool   { return isGroundRail(partName) }
