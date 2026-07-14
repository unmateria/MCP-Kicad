// demo_buck_converter builds a switching regulator skeleton: a generic IC,
// inductor, Schottky diode, and input/output caps. The topology is simplified
// (we use a generic 5-pin IC) but exercises clustering on an LC filter and
// rule-based rotation of D/L components.
package main

import (
	"mcp-kicad/internal/place2/demoharness"
	"mcp-kicad/internal/tools"
)

func main() {
	demoharness.Spec{
		Project:     "demo_buck_converter",
		Description: "Generic buck regulator: IC + L + Schottky + Cin + Cout",
		Symbols: []demoharness.Symbol{
			// Use LM2596 — present in mainstream KiCad libraries.
			{LibID: "Regulator_Switching:LM2596S-5", Reference: "U1", Value: "LM2596-5"},
			{LibID: "Device:L", Reference: "L1", Value: "33uH"},
			{LibID: "Diode:1N5822", Reference: "D1", Value: "1N5822"},
			{LibID: "Device:C", Reference: "C1", Value: "470u"}, // input bulk
			{LibID: "Device:C", Reference: "C2", Value: "100n"}, // input bypass
			{LibID: "Device:C", Reference: "C3", Value: "220u"}, // output bulk
			{LibID: "Device:C", Reference: "C4", Value: "100n"}, // output bypass
		},
		Nets: []tools.NetConn{
			{Net: "VIN", Pins: []string{"C1.1", "C2.1", "U1.VIN"}},
			// Switch node — drives the inductor and clamps to the diode anode.
			{Net: "SW", Pins: []string{"U1.~{ON}/OFF", "L1.1", "D1.K"}},
			{Net: "VOUT", Pins: []string{"L1.2", "C3.1", "C4.1", "U1.FB"}},
			{Net: "GND", Pins: []string{"U1.GND", "C1.2", "C2.2", "C3.2", "C4.2", "D1.A"}},
		},
		PowerRails: []demoharness.PowerRail{
			{LibID: "power:GND", From: "U1.GND"},
		},
	}.Run()
}
