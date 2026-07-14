package route2

import (
	"testing"

	"mcp-kicad/internal/sexp"
)

func TestAstarPPStraightLine(t *testing.T) {
	// Single seed symbol so the grid sizes correctly. Endpoints sit far from
	// the symbol body so they're not blocked.
	syms := []sexp.SchematicSymbol{{Reference: "A", X: 100, Y: 100}}
	r := New(syms, nil)
	path := r.Route(50, 50, 80, 50)
	if len(path) < 2 {
		t.Fatalf("expected straight path, got %v", path)
	}
	if path[0][1] != path[len(path)-1][1] {
		t.Errorf("path is not horizontal: %v", path)
	}
}

func TestDetectJunctionsT(t *testing.T) {
	// Three segments meeting at (10, 10) — a T on net 1.
	segs := []PathSegment{
		{AX: 0, AY: 10, BX: 10, BY: 10, NetID: 1},  // horizontal in
		{AX: 10, AY: 10, BX: 20, BY: 10, NetID: 1}, // horizontal out
		{AX: 10, AY: 10, BX: 10, BY: 20, NetID: 1}, // vertical drop
	}
	js, crs := DetectJunctions(segs)
	if len(js) != 1 {
		t.Fatalf("expected 1 junction, got %d", len(js))
	}
	if len(crs) != 0 {
		t.Errorf("expected 0 crossings, got %d", len(crs))
	}
	if js[0].X != 10 || js[0].Y != 10 {
		t.Errorf("junction at (%.0f, %.0f), want (10, 10)", js[0].X, js[0].Y)
	}
}

func TestDetectJunctionsCrossing(t *testing.T) {
	// Two segments of different nets crossing at (10, 10).
	segs := []PathSegment{
		{AX: 0, AY: 10, BX: 20, BY: 10, NetID: 1},
		{AX: 10, AY: 0, BX: 10, BY: 20, NetID: 2},
	}
	js, crs := DetectJunctions(segs)
	if len(js) != 0 {
		t.Errorf("expected 0 junctions, got %d", len(js))
	}
	if len(crs) != 1 {
		t.Fatalf("expected 1 crossing, got %d", len(crs))
	}
}
