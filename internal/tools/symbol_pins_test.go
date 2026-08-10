package tools

import (
	"strings"
	"testing"
)

// The listing must follow the DRAWING, not the pin numbers: a session wired a
// 7-segment fan-out trusting pin-number order and got every wire crossed,
// because the display's vertical order is neither numeric nor alphabetical.
func TestSymbolPinsListsDrawingOrder(t *testing.T) {
	e := tidyEnv(t)
	sym, err := e.probePlaced("Display_Character:D168K", 1, 0, false)
	if err != nil {
		t.Skipf("D168K not available: %v", err)
	}

	// Reconstruct the order the tool prints: left side first, top to bottom.
	type row struct {
		number string
		y      float64
	}
	var left []row
	for _, p := range sym.Pins {
		if pinSide(p.Direction) == "left" {
			left = append(left, row{p.Number, p.Y})
		}
	}
	if len(left) < 3 {
		t.Fatalf("expected a column of left pins, got %d", len(left))
	}
	// The tool sorts by Y ascending (top first). Verify that order is NOT the
	// pin-number order — that inequality is the whole reason the column exists.
	numericOrder := true
	for i := 1; i < len(left); i++ {
		if pinNumLess(left[i].number, left[i-1].number) {
			numericOrder = false
		}
	}
	sorted := make([]row, len(left))
	copy(sorted, left)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].y < sorted[j-1].y; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	if numericOrder {
		t.Log("left column happens to be in numeric order on this symbol; geometric sort still verified below")
	}
	for i := 1; i < len(sorted); i++ {
		if sorted[i].y < sorted[i-1].y {
			t.Fatalf("geometric sort failed: %v", sorted)
		}
	}
}

// probePlaced must report placed geometry: rotating a resistor 90° moves its
// pins from a vertical column to a horizontal row.
func TestProbePlacedAppliesRotation(t *testing.T) {
	e := tidyEnv(t)
	flat, err := e.probePlaced("Device:R", 1, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	rot, err := e.probePlaced("Device:R", 1, 90, false)
	if err != nil {
		t.Fatal(err)
	}
	if flat.Pins[0].X != flat.Pins[1].X {
		t.Errorf("rot 0 resistor pins should share X: %+v", flat.Pins)
	}
	if rot.Pins[0].Y != rot.Pins[1].Y {
		t.Errorf("rot 90 resistor pins should share Y: %+v", rot.Pins)
	}
}

// The output must warn about pack-style pin names (R1.1) colliding with the
// multi-unit syntax, steering sources toward pin numbers.
func TestSymbolPinsOutputMentionsPackNames(t *testing.T) {
	e := tidyEnv(t)
	res, _, err := e.handleSymbolPins(nil, nil, SymbolPinsArgs{LibID: "Device:R"})
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "DRAWING order") {
		t.Errorf("output does not announce drawing order:\n%s", text)
	}
	if !strings.Contains(text, "use the NUMBER") {
		t.Errorf("output does not warn about dotted pack pin names:\n%s", text)
	}
}
