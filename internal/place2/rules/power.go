// Package rules applies the universal "human" schematic conventions on top
// of a layout proposal:
//
//	1. Vcc / +5V / +12V power symbols sit ABOVE their target pin.
//	2. GND / VEE / VSS power symbols sit BELOW their target pin.
//	3. Signal flow runs left → right (input nets pushed leftmost,
//	   output nets pushed rightmost).
//	4. Symmetric 2-pin parts (R/C/L) auto-rotate to align with their
//	   dominant connection axis.
//	5. Op-amps face their output (triangle base on the input side).
//	6. Diodes/LEDs orient anode → cathode along signal flow.
//	7. Power rails are emitted horizontally when ≥ 4 same-rail symbols share
//	   a column (handled by rules.go::ApplyBusAlignment).
//
// Rules operate on a position+rotation map (place2.PlacementResult-shaped).
// They are idempotent — applying the same rule twice is a no-op.
package rules

import (
	"strings"

	"mcp-kicad/internal/sexp"
)

// PinDelta returns the (dx, dy) offset that goes one step in the pin's
// outgoing direction in SCREEN coordinates (Y-down).
func pinDelta(dir float64) (dx, dy float64) {
	switch int(dir) % 360 {
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

// ApplyPowerRails adjusts the position of every placed power symbol so that
// positive supplies sit ABOVE their target pin and ground/negative supplies
// sit BELOW. The vertical offset is two grid units (2.54 mm) — KiCad's
// standard distance for a power label flag.
//
// `positions` is keyed by reference (or REF#unit for multi-unit ICs) and
// maps to (x, y) in mm. It is mutated in place. The function never moves
// non-power symbols.
//
// `syms` must reflect the post-layout snapshot: each power symbol's Pins[0]
// gives the attach point we orient against. When pin info is missing the
// power symbol is left where it is.
func ApplyPowerRails(syms []sexp.SchematicSymbol, positions map[string][2]float64) int {
	const offset = 2.54
	moved := 0
	for _, s := range syms {
		if !strings.HasPrefix(s.LibID, "power:") || len(s.Pins) == 0 {
			continue
		}
		partName := strings.TrimPrefix(s.LibID, "power:")
		key := keyFor(s.Reference, s.Unit)
		// Anchor is the pin position itself; we translate the symbol so its
		// pin lands at the existing anchor. Power symbols have a single pin
		// so the placement is fully determined by partName + anchor.
		pin := s.Pins[0]
		var newX, newY float64
		switch {
		case isPositiveRail(partName):
			newX, newY = pin.X, pin.Y-offset
		case isGroundRail(partName):
			newX, newY = pin.X, pin.Y+offset
		default:
			continue
		}
		positions[key] = [2]float64{sexp.SnapGrid(newX), sexp.SnapGrid(newY)}
		moved++
	}
	return moved
}

// ApplyBusAlignment guarantees that all power symbols sharing the same rail
// name (e.g. every #PWR_+5V) end up at the same Y coordinate when they are
// placed near the top of the page, and the same X spacing along that
// horizontal bus. Activated when ≥ minBus power symbols share a rail.
//
// Returns the number of power symbols moved.
func ApplyBusAlignment(syms []sexp.SchematicSymbol, positions map[string][2]float64, minBus int) int {
	if minBus < 2 {
		minBus = 4
	}
	groups := make(map[string][]sexp.SchematicSymbol)
	for _, s := range syms {
		if !strings.HasPrefix(s.LibID, "power:") {
			continue
		}
		partName := strings.TrimPrefix(s.LibID, "power:")
		groups[partName] = append(groups[partName], s)
	}
	moved := 0
	for partName, group := range groups {
		if len(group) < minBus {
			continue
		}
		// Compute the canonical Y as the AVG of the first pass (already
		// adjusted by ApplyPowerRails).
		var sumY float64
		for _, s := range group {
			pos, ok := positions[keyFor(s.Reference, s.Unit)]
			if !ok {
				continue
			}
			sumY += pos[1]
		}
		canonY := sexp.SnapGrid(sumY / float64(len(group)))
		positiveRail := isPositiveRail(partName)
		for _, s := range group {
			key := keyFor(s.Reference, s.Unit)
			pos, ok := positions[key]
			if !ok {
				continue
			}
			// Force the same Y for all symbols of this rail. X stays so that
			// the symbols sit above their original target pin.
			if pos[1] != canonY {
				positions[key] = [2]float64{pos[0], canonY}
				moved++
			}
		}
		_ = positiveRail
	}
	return moved
}

// isPositiveRail mirrors the cluster package's positive-supply detection
// without taking a dependency on it (keeps the import graph one-way).
func isPositiveRail(name string) bool {
	n := strings.ToUpper(strings.TrimSpace(name))
	switch n {
	case "VCC", "VDD", "VAA", "AVCC", "AVDD", "VBUS":
		return true
	}
	if strings.HasPrefix(n, "+") {
		return true
	}
	if strings.HasPrefix(n, "VCC") || strings.HasPrefix(n, "VDD") {
		return true
	}
	return false
}

func isGroundRail(name string) bool {
	n := strings.ToUpper(strings.TrimSpace(name))
	switch n {
	case "GND", "GND1", "GND2", "GNDA", "GNDD", "GNDPWR", "GNDREF",
		"GNDS", "EARTH", "0V", "VSS", "VEE", "-12V", "-5V":
		return true
	}
	if strings.HasPrefix(n, "-") {
		return true
	}
	return false
}

// keyFor mirrors place2.symKey for multi-unit ICs without forcing the
// rules package to depend on place2 (rules feed into place2, not the other
// way around).
func keyFor(ref string, unit int) string {
	if unit <= 1 {
		return ref
	}
	return ref + "#" + itoa(unit)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	if n < 0 {
		return "-" + itoa(-n)
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
