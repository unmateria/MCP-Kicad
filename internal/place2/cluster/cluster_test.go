package cluster

import (
	"testing"

	"mcp-kicad/internal/sexp"
)

func TestDecouplingDetected(t *testing.T) {
	syms := []sexp.SchematicSymbol{
		{Reference: "U1", LibID: "MCU_Microchip_ATmega:ATmega328-AU"},
		{Reference: "C1", LibID: "Device:C"},
		{Reference: "C2", LibID: "Device:C"},
		{Reference: "R1", LibID: "Device:R"},
	}
	nets := []sexp.Net{
		{Name: "VCC", Pins: []sexp.PinRef{
			{Reference: "U1", PinNumber: "1"},
			{Reference: "C1", PinNumber: "1"},
			{Reference: "C2", PinNumber: "1"},
			{Reference: "R1", PinNumber: "2"},
		}},
		{Name: "GND", Pins: []sexp.PinRef{
			{Reference: "U1", PinNumber: "8"},
			{Reference: "C1", PinNumber: "2"},
			{Reference: "C2", PinNumber: "2"},
		}},
		{Name: "SDA", Pins: []sexp.PinRef{
			{Reference: "U1", PinNumber: "27"},
			{Reference: "R1", PinNumber: "1"},
		}},
	}
	cs := Detect(syms, nets)
	var dec, pull *Cluster
	for i := range cs {
		switch cs[i].Kind {
		case "decoupling":
			dec = &cs[i]
		case "pullup":
			pull = &cs[i]
		}
	}
	if dec == nil {
		t.Fatalf("decoupling cluster not detected: %+v", cs)
	}
	if dec.Anchor != "U1" {
		t.Errorf("decoupling anchor = %q, want U1", dec.Anchor)
	}
	if !contains(dec.Refs, "C1") || !contains(dec.Refs, "C2") {
		t.Errorf("decoupling refs missing caps: %v", dec.Refs)
	}
	if pull == nil {
		t.Fatalf("pullup cluster not detected: %+v", cs)
	}
	if pull.Anchor != "U1" {
		t.Errorf("pullup anchor = %q, want U1", pull.Anchor)
	}
	if !contains(pull.Refs, "R1") {
		t.Errorf("pullup missing R1: %v", pull.Refs)
	}
}

func TestVoltageDividerDetected(t *testing.T) {
	syms := []sexp.SchematicSymbol{
		{Reference: "R1", LibID: "Device:R"},
		{Reference: "R2", LibID: "Device:R"},
	}
	nets := []sexp.Net{
		{Name: "VCC", Pins: []sexp.PinRef{
			{Reference: "R1", PinNumber: "1"},
		}},
		{Name: "TAP", Pins: []sexp.PinRef{
			{Reference: "R1", PinNumber: "2"},
			{Reference: "R2", PinNumber: "1"},
		}},
		{Name: "GND", Pins: []sexp.PinRef{
			{Reference: "R2", PinNumber: "2"},
		}},
	}
	cs := Detect(syms, nets)
	var found *Cluster
	for i := range cs {
		if cs[i].Kind == "voltage_divider" {
			found = &cs[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("voltage_divider not detected: %+v", cs)
	}
	if found.Anchor != "R1" {
		t.Errorf("anchor = %q, want R1", found.Anchor)
	}
}

func TestCrystalDetected(t *testing.T) {
	syms := []sexp.SchematicSymbol{
		{Reference: "U1", LibID: "MCU_Microchip_ATmega:ATmega328-AU"},
		{Reference: "Y1", LibID: "Device:Crystal"},
		{Reference: "C1", LibID: "Device:C"},
		{Reference: "C2", LibID: "Device:C"},
	}
	nets := []sexp.Net{
		{Name: "XTAL1", Pins: []sexp.PinRef{
			{Reference: "U1", PinNumber: "9"},
			{Reference: "Y1", PinNumber: "1"},
			{Reference: "C1", PinNumber: "1"},
		}},
		{Name: "XTAL2", Pins: []sexp.PinRef{
			{Reference: "U1", PinNumber: "10"},
			{Reference: "Y1", PinNumber: "2"},
			{Reference: "C2", PinNumber: "1"},
		}},
		{Name: "GND", Pins: []sexp.PinRef{
			{Reference: "C1", PinNumber: "2"},
			{Reference: "C2", PinNumber: "2"},
		}},
	}
	cs := Detect(syms, nets)
	var found *Cluster
	for i := range cs {
		if cs[i].Kind == "crystal" {
			found = &cs[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("crystal cluster not detected: %+v", cs)
	}
	if found.Anchor != "Y1" {
		t.Errorf("anchor = %q, want Y1", found.Anchor)
	}
	if !contains(found.Refs, "C1") || !contains(found.Refs, "C2") {
		t.Errorf("crystal refs missing load caps: %v", found.Refs)
	}
}

func contains(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}
