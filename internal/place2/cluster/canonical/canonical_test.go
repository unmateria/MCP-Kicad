package canonical

import (
	"testing"

	"mcp-kicad/internal/place2/cluster"
	"mcp-kicad/internal/sexp"
)

func TestSeriesLEDDetector(t *testing.T) {
	syms := []sexp.SchematicSymbol{
		{Reference: "D1", LibID: "Device:LED"},
		{Reference: "R1", LibID: "Device:R"},
	}
	nets := []sexp.Net{
		{Name: "ANODE", Pins: []sexp.PinRef{{Reference: "R1", PinNumber: "2"}, {Reference: "D1", PinName: "A"}}},
	}
	got := seriesLED(syms, nets)
	if len(got) != 1 || got[0].Anchor != "D1" {
		t.Fatalf("unexpected: %#v", got)
	}
}

func TestBypassNonpowerCTL(t *testing.T) {
	syms := []sexp.SchematicSymbol{
		{Reference: "U1", LibID: "Timer:NE555P"},
		{Reference: "C2", LibID: "Device:C"},
	}
	nets := []sexp.Net{
		{Name: "GND", Pins: []sexp.PinRef{{Reference: "C2", PinNumber: "2"}, {Reference: "U1", PinName: "GND"}}},
		{Name: "CTL", Pins: []sexp.PinRef{{Reference: "C2", PinNumber: "1"}, {Reference: "U1", PinName: "CONT"}}},
	}
	got := bypassNonPower(syms, nets)
	if len(got) != 1 || got[0].Kind != "bypass_nonpower" {
		t.Fatalf("unexpected: %#v", got)
	}
}

func TestRegisterExtraIsCalled(t *testing.T) {
	syms := []sexp.SchematicSymbol{
		{Reference: "D1", LibID: "Device:LED"},
		{Reference: "R1", LibID: "Device:R"},
	}
	nets := []sexp.Net{
		{Name: "A", Pins: []sexp.PinRef{{Reference: "R1", PinNumber: "2"}, {Reference: "D1", PinName: "A"}}},
	}
	cs := cluster.Detect(syms, nets)
	got := false
	for _, c := range cs {
		if c.Kind == "series_led" {
			got = true
			break
		}
	}
	if !got {
		t.Errorf("series_led not detected end-to-end via cluster.Detect; got=%v", cs)
	}
}
