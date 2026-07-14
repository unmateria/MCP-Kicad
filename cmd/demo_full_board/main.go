// demo_full_board is the stress-test demo combining MCU + I²C pull-ups +
// crystal + linear regulator + LED indicator on the same schematic. Used
// as the headline regression test for the placement+routing redesign.
package main

import (
	"mcp-kicad/internal/place2/demoharness"
	"mcp-kicad/internal/tools"
)

func main() {
	demoharness.Spec{
		Project:     "demo_full_board",
		Description: "ATmega328 + LM7805 + I²C pull-ups + crystal + LED indicator",
		Symbols: []demoharness.Symbol{
			{LibID: "MCU_Microchip_ATmega:ATmega328-AU", Reference: "U1", Value: "ATmega328"},
			{LibID: "Regulator_Linear:LM7805_TO220", Reference: "U2", Value: "LM7805"},
			// I²C pullups
			{LibID: "Device:R", Reference: "R1", Value: "4.7k"},
			{LibID: "Device:R", Reference: "R2", Value: "4.7k"},
			// MCU decoupling
			{LibID: "Device:C", Reference: "C1", Value: "100n"},
			{LibID: "Device:C", Reference: "C2", Value: "100n"},
			{LibID: "Device:C", Reference: "C3", Value: "10u"},
			// Regulator caps
			{LibID: "Device:C", Reference: "C4", Value: "100n"},
			{LibID: "Device:C", Reference: "C5", Value: "10u"},
			{LibID: "Device:C", Reference: "C6", Value: "100n"},
			{LibID: "Device:C", Reference: "C7", Value: "10u"},
			// Crystal
			{LibID: "Device:Crystal", Reference: "Y1", Value: "16MHz"},
			{LibID: "Device:C", Reference: "C8", Value: "22p"},
			{LibID: "Device:C", Reference: "C9", Value: "22p"},
			// Power LED
			{LibID: "Device:LED", Reference: "D1", Value: "GREEN"},
			{LibID: "Device:R", Reference: "R3", Value: "1k"},
		},
		Nets: []tools.NetConn{
			// Regulator section — VIN unregulated, +5V regulated.
			{Net: "VIN", Pins: []string{"C4.1", "C5.1", "U2.VI"}},
			{Net: "+5V", Pins: []string{"U2.VO", "C6.1", "C7.1", "U1.VCC", "U1.AVCC", "C1.1", "C2.1", "C3.1", "R1.2", "R2.2", "R3.1"}},
			// I²C — signal flow MCU outwards (MCU first).
			{Net: "SDA", Pins: []string{"U1.SDA", "R1.1"}},
			{Net: "SCL", Pins: []string{"U1.SCL", "R2.1"}},
			// Crystal.
			{Net: "XTAL1", Pins: []string{"U1.XTAL1", "Y1.1", "C8.1"}},
			{Net: "XTAL2", Pins: []string{"U1.XTAL2", "Y1.2", "C9.1"}},
			// LED indicator — R3 first (drives the LED).
			{Net: "LED_NODE", Pins: []string{"R3.2", "D1.A"}},
			// GND — large fan-out.
			{Net: "GND", Pins: []string{
				"U1.GND", "U2.GND", "C1.2", "C2.2", "C3.2", "C4.2", "C5.2",
				"C6.2", "C7.2", "C8.2", "C9.2", "D1.K",
			}},
		},
		PowerRails: []demoharness.PowerRail{
			{LibID: "power:+5V", From: "U1.VCC"},
			{LibID: "power:GND", From: "U1.GND"},
		},
	}.Run()
}
