package power

import (
	"math"
	"testing"

	"mcp-kicad/internal/sexp"
)

func TestComputeOffsetsByPinDirection(t *testing.T) {
	cases := []struct {
		name      string
		libID     string
		pinDir    float64
		libAngle  float64
		wantDX    float64
		wantDY    float64
		wantRotF1 float64
	}{
		{"vcc-pin-up", "power:VCC", 90, 270, 0, -PinStubLen, 180},
		{"gnd-pin-down", "power:GND", 270, 90, 0, PinStubLen, 180},
		{"vcc-pin-right", "power:VCC", 0, 270, PinStubLen, 0, 90},
		{"vcc-pin-left", "power:VCC", 180, 270, -PinStubLen, 0, 270},
		{"gnd-no-dir-fallback-down", "power:GND", -1, 90, 0, PinStubLen, math.Mod(-1-90+720, 360)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			px, py := sexp.SnapGrid(100), sexp.SnapGrid(100)
			pin := sexp.PinInfo{X: px, Y: py, Direction: c.pinDir}
			d := Compute(c.libID, pin, c.libAngle, "")
			gotDX := d.X - px
			gotDY := d.Y - py
			if math.Abs(gotDX-c.wantDX) > 0.01 || math.Abs(gotDY-c.wantDY) > 0.01 {
				t.Errorf("offset: got (%.2f,%.2f) want (%.2f,%.2f)", gotDX, gotDY, c.wantDX, c.wantDY)
			}
			if d.PartName != "VCC" && d.PartName != "GND" {
				t.Errorf("partName=%q", d.PartName)
			}
			if math.Abs(math.Mod(d.Rotation-c.wantRotF1+720, 360)) > 0.5 {
				t.Errorf("rotation: got %.1f want %.1f", d.Rotation, c.wantRotF1)
			}
		})
	}
}

func TestRegistryDedup(t *testing.T) {
	r := NewRegistry()
	if !r.Mark("power:GND", 50, 50) {
		t.Fatal("first mark should be fresh")
	}
	if r.Mark("power:GND", 50.05, 50.05) {
		t.Error("near-duplicate within snap should collide")
	}
	if !r.Mark("power:GND", 60, 50) {
		t.Error("different position should be fresh")
	}
	if !r.Mark("power:VCC", 50, 50) {
		t.Error("different lib_id same coords should be fresh")
	}
	if !r.Has("power:GND", 50, 50) {
		t.Error("Has should report previously marked")
	}
}

func TestIsPositiveAndGround(t *testing.T) {
	pos := []string{"VCC", "VDD", "+5V", "+12V", "VBUS", "AVCC"}
	gnd := []string{"GND", "VSS", "VEE", "-12V", "GNDA", "0V"}
	for _, p := range pos {
		if !IsPositive(p) {
			t.Errorf("IsPositive(%q) = false", p)
		}
	}
	for _, g := range gnd {
		if !IsGround(g) {
			t.Errorf("IsGround(%q) = false", g)
		}
	}
}
