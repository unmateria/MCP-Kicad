// demo_voltage_regulator builds an LM7805 linear regulator with input/output
// capacitors and a power-on LED indicator. Used to validate decoupling-cluster
// detection (Cin/Cout adjacent to the regulator) and signal flow VIN → VOUT.
package main

import (
	"mcp-kicad/internal/place2/demoharness"
	"mcp-kicad/internal/tools"
)

func main() {
	demoharness.Spec{
		Project:     "demo_voltage_regulator",
		Description: "LM7805 with input/output caps + power-on LED indicator",
		Symbols: []demoharness.Symbol{
			{LibID: "Regulator_Linear:LM7805_TO220", Reference: "U1", Value: "LM7805"},
			{LibID: "Device:C", Reference: "C1", Value: "100n"},  // input bypass
			{LibID: "Device:C", Reference: "C2", Value: "10u"},   // input bulk
			{LibID: "Device:C", Reference: "C3", Value: "100n"},  // output bypass
			{LibID: "Device:C", Reference: "C4", Value: "10u"},   // output bulk
			{LibID: "Device:LED", Reference: "D1", Value: "GREEN"},
			{LibID: "Device:R", Reference: "R1", Value: "1k"},
		},
		Nets: []tools.NetConn{
			// Signal flow: VIN → U1 → VOUT → R1 → D1 → GND
			{Net: "VIN", Pins: []string{"C1.1", "C2.1", "U1.VI"}},
			{Net: "VOUT", Pins: []string{"U1.VO", "C3.1", "C4.1", "R1.1"}},
			{Net: "LED_NODE", Pins: []string{"R1.2", "D1.A"}},
			{Net: "GND", Pins: []string{"U1.GND", "C1.2", "C2.2", "C3.2", "C4.2", "D1.K"}},
		},
		PowerRails: []demoharness.PowerRail{
			{LibID: "power:+5V", From: "U1.VO"},
			{LibID: "power:GND", From: "U1.GND"},
		},
	}.Run()
}
