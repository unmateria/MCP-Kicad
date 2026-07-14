package route2

import "testing"

// 4-pin net with two pairs sharing rows: trunk should be vertical (median X).
func TestBuildSteinerTrunkColinearPair(t *testing.T) {
	pins := []Pin{
		{X: 50, Y: 30, Dir: -1},
		{X: 50, Y: 60, Dir: -1}, // colinear with #1 vertically
		{X: 80, Y: 60, Dir: -1}, // colinear with #2 horizontally
		{X: 80, Y: 30, Dir: -1},
	}
	tr, ok := BuildSteinerTrunk(pins)
	if !ok {
		t.Fatal("expected trunk")
	}
	segs := tr.TrunkSegments()
	if len(segs) < 1 {
		t.Fatal("no segments produced")
	}
	// Total length: trunk 30 + 4 stubs of 15 = 90 (median X=50 or 80; ties → 80 due to sort).
	total := 0.0
	for _, s := range segs {
		total += abs(s[1][0]-s[0][0]) + abs(s[1][1]-s[0][1])
	}
	if total > 100 {
		t.Errorf("total wire %.1f exceeds Steiner bound", total)
	}
}

func TestDetectCollinearGroups(t *testing.T) {
	pins := []Pin{
		{X: 10, Y: 20}, {X: 40, Y: 20}, // share Y
		{X: 100, Y: 50}, {X: 100, Y: 80}, // share X
		{X: 200, Y: 200}, // alone
	}
	groups := DetectCollinearGroups(pins, 1.27)
	gotH, gotV := 0, 0
	for _, g := range groups {
		if g.Orientation == 'H' {
			gotH++
		}
		if g.Orientation == 'V' {
			gotV++
		}
	}
	if gotH < 1 || gotV < 1 {
		t.Errorf("expected at least 1 H and 1 V group; got H=%d V=%d", gotH, gotV)
	}
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
