// demo_mcu_i2c builds a canonical ATmega328 + I²C pull-ups + decoupling +
// crystal demo through the MCP handlers. Used as a Phase A baseline and
// regression target for clustering rules (decoupling near IC, pull-ups
// adjacent to the I²C net).
package main

import (
	"mcp-kicad/internal/place2/demoharness"
	"mcp-kicad/internal/tools"
)

func main() {
	demoharness.Spec{
		Project:     "demo_mcu_i2c",
		Description: "ATmega328 + I²C pull-ups (4.7k SDA/SCL) + 4× decoupling caps + 16 MHz crystal",
		Symbols: []demoharness.Symbol{
			{LibID: "MCU_Microchip_ATmega:ATmega328-A", Reference: "U1", Value: "ATmega328"},
			{LibID: "Device:R", Reference: "R1", Value: "4.7k"}, // SDA pull-up
			{LibID: "Device:R", Reference: "R2", Value: "4.7k"}, // SCL pull-up
			{LibID: "Device:C", Reference: "C1", Value: "100n"}, // VCC decoupling
			{LibID: "Device:C", Reference: "C2", Value: "100n"}, // AVCC decoupling
			{LibID: "Device:C", Reference: "C3", Value: "100n"}, // VCC decoupling 2
			{LibID: "Device:C", Reference: "C4", Value: "10u"},  // bulk
			{LibID: "Device:Crystal", Reference: "Y1", Value: "16MHz"},
			{LibID: "Device:C", Reference: "C5", Value: "22p"}, // crystal load
			{LibID: "Device:C", Reference: "C6", Value: "22p"},
		},
		Nets: []tools.NetConn{
			// I²C bus + pullups: signal flow is U1 → SDA/SCL → connector outside.
			// ATmega328-A pin names come from its base symbol (ATmega48PV-10A via
			// "extends"): SDA/SCL are not named pins, they are PC4/PC5 (pin numbers
			// 27/28); referenced by number since the pin name is "PC4"/"PC5".
			{Net: "SDA", Pins: []string{"U1.27", "R1.1"}},
			{Net: "SCL", Pins: []string{"U1.28", "R2.1"}},
			// VCC for IC and pull-up tops.
			{Net: "VCC", Pins: []string{"U1.VCC", "U1.AVCC", "R1.2", "R2.2", "C1.1", "C2.1", "C3.1", "C4.1"}},
			// GND.
			{Net: "GND", Pins: []string{"U1.GND", "C1.2", "C2.2", "C3.2", "C4.2", "C5.2", "C6.2"}},
			// Crystal load network. XTAL1/XTAL2 are shared with PB6/PB7, named
			// "XTAL1/PB6" and "XTAL2/PB7" (pin numbers 7/8) in the symbol.
			{Net: "XTAL1", Pins: []string{"U1.7", "Y1.1", "C5.1"}},
			{Net: "XTAL2", Pins: []string{"U1.8", "Y1.2", "C6.1"}},
		},
		PowerRails: []demoharness.PowerRail{
			{LibID: "power:+5V", From: "U1.VCC"},
			{LibID: "power:GND", From: "U1.GND"},
		},
	}.Run()
}
