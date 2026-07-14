// Package elk runs ELK (Eclipse Layout Kernel) over a schematic graph via
// the elkjs Node.js bridge embedded in this package.
//
// ELK is the right tool for our problem domain because:
//
//   - It is the most mature open-source implementation of Sugiyama-with-ports
//     (the algorithm netlistsvg uses to render schematics).
//   - It supports compound nodes, which lets us encode functional clusters
//     (decoupling, pull-ups, crystal+caps) as visual groups.
//   - It outputs deterministic positions when seeded, so test snapshots stay
//     stable run-to-run.
//
// The runtime topology is: Go ↔ stdin/stdout pipes ↔ a tiny Node.js script
// (embed/elk_layout.js) that imports elkjs and calls layout(). When Node is
// not on PATH or elkjs is not installed, callers fall back to the Go-only
// Sugiyama implementation in fallback.go.
package elk

import "strings"

// SymbolSize returns an approximate width × height in mm for a symbol with
// the given lib_id. Used to give ELK realistic node dimensions when laying
// out the graph; without these the kernel assumes square unit nodes and
// over-packs everything.
//
// Sizes are conservative — slightly larger than reality so labels fit. Sizes
// for unknown lib_ids fall through to a sensible default.
func SymbolSize(libID string) (w, h float64) {
	switch {
	case strings.HasPrefix(libID, "MCU_"):
		// 32-pin AVR-class chips are typically 25×40 mm in the schematic.
		return 25.4, 38.1
	case strings.HasPrefix(libID, "Amplifier_Operational:"):
		// KiCad's op-amp triangle is ~15×15 mm per channel (the triangle
		// extends well above and below the pin bbox; underestimating this
		// causes cluster satellites to land inside the body).
		return 15.24, 15.24
	case strings.HasPrefix(libID, "Regulator_Linear:"),
		strings.HasPrefix(libID, "Regulator_Switching:"):
		return 17.78, 17.78
	case strings.HasPrefix(libID, "Connector:"),
		strings.HasPrefix(libID, "Connector_Generic:"):
		return 10.16, 25.4
	case strings.HasPrefix(libID, "Interface_") ||
		strings.HasPrefix(libID, "Memory_") ||
		strings.HasPrefix(libID, "Logic_"):
		return 17.78, 25.4
	}
	// Discrete defaults.
	switch libID {
	case "Device:R", "Device:R_Small", "Device:R_US", "Device:R_Variable":
		return 5.08, 7.62
	case "Device:C", "Device:C_Small", "Device:C_Polarized", "Device:CP":
		return 5.08, 7.62
	case "Device:L", "Device:L_Small":
		return 5.08, 7.62
	case "Device:Crystal":
		return 7.62, 5.08
	case "Device:LED":
		return 7.62, 5.08
	case "Device:Battery_Cell":
		return 7.62, 12.7
	}
	if strings.HasPrefix(libID, "power:") {
		return 2.54, 2.54
	}
	if strings.HasPrefix(libID, "Diode:") {
		return 7.62, 5.08
	}
	return 10.16, 10.16
}
