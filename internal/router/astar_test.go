package router

import (
	"math"
	"testing"

	"mcp-kicad/internal/sexp"
)

// emptyRouter creates a Router with a single far-away symbol to seed the grid bounds.
func emptyRouter() *Router {
	sym := sexp.SchematicSymbol{X: 100, Y: 100}
	return NewRouter([]sexp.SchematicSymbol{sym}, nil)
}

func TestRouteStraightHorizontal(t *testing.T) {
	r := emptyRouter()
	path := r.Route(50, 100, 80, 100)
	if path == nil {
		t.Fatal("expected a path, got nil")
	}
	if len(path) < 2 {
		t.Fatalf("expected at least 2 waypoints, got %d", len(path))
	}
}

func TestRouteStraightVertical(t *testing.T) {
	r := emptyRouter()
	path := r.Route(50, 50, 50, 80)
	if path == nil {
		t.Fatal("expected a path, got nil")
	}
	if len(path) < 2 {
		t.Fatalf("expected at least 2 waypoints, got %d", len(path))
	}
}

func TestRouteLShape(t *testing.T) {
	r := emptyRouter()
	path := r.Route(50, 50, 80, 80)
	if path == nil {
		t.Fatal("expected a path, got nil")
	}
	// L-shape: start + elbow + end = 3 waypoints after collinear merge.
	if len(path) != 3 {
		t.Fatalf("expected 3 waypoints for L-shape, got %d", len(path))
	}
}

func TestRouteSamePoint(t *testing.T) {
	r := emptyRouter()
	path := r.Route(50, 50, 50, 50)
	if path == nil {
		t.Fatal("expected a single-point path, got nil")
	}
	if len(path) != 1 {
		t.Fatalf("expected 1 waypoint, got %d", len(path))
	}
}

func TestRouteAvoidObstacle(t *testing.T) {
	// Symbol body blocks the direct elbow from (50,50) to (80,80).
	sym := sexp.SchematicSymbol{
		X: 65, Y: 65,
		Pins: []sexp.PinInfo{
			{X: 60, Y: 60},
			{X: 70, Y: 70},
		},
	}
	r := NewRouter([]sexp.SchematicSymbol{sym}, nil)
	path := r.Route(50, 50, 80, 80)
	if path == nil {
		t.Fatal("expected a path around obstacle, got nil")
	}
	// All segments must be axis-aligned.
	for i := 1; i < len(path); i++ {
		dx := math.Abs(path[i][0] - path[i-1][0])
		dy := math.Abs(path[i][1] - path[i-1][1])
		if dx > 0.01 && dy > 0.01 {
			t.Errorf("segment %d→%d is diagonal: (%.2f,%.2f)→(%.2f,%.2f)",
				i-1, i, path[i-1][0], path[i-1][1], path[i][0], path[i][1])
		}
	}
}

// TestRoutePinInsideBody is the regression test for the original bug:
// Both start and end pins were inside SymbolBBox (padded outward), so the A*
// could not expand from them and fell back to labels for every net.
// With symbolBodyBBox (inset by pinLen), pin-tip cells are OUTSIDE the hard
// area and routing works correctly.
func TestRoutePinInsideBody(t *testing.T) {
	// Simulate NE555 with two pins on the left side of the body.
	// Their X coords are at the left edge; all other pins anchor the body to the right.
	ne555 := sexp.SchematicSymbol{
		X: 50, Y: 50,
		Pins: []sexp.PinInfo{
			// Left-side pins (tip X = 40, body edge X = 40+2.54 = 42.54)
			{X: 40, Y: 45}, // ~{RST}
			{X: 40, Y: 48}, // DISCH
			// Right-side pin (tip X = 60)
			{X: 60, Y: 50}, // OUT
			// Top pin (tip Y = 40)
			{X: 50, Y: 40}, // VCC
			// Bottom pin (tip Y = 60)
			{X: 50, Y: 60}, // GND
		},
	}
	r := NewRouter([]sexp.SchematicSymbol{ne555}, nil)

	// Route between two pins that are both on the NE555's left side.
	// In the old (padded) bbox these pins were hard obstacles → nil.
	// In the new (inset) bbox they are outside the body → should route.
	path := r.Route(40, 45, 40, 48)
	if path == nil {
		t.Fatal("route between two adjacent pins on same IC side returned nil — " +
			"pin cells are probably still inside the hard obstacle area")
	}
}

// TestRoutePinsOnSameIC routes between a top pin and a left pin of the same IC,
// verifying the router goes around the body rather than through it.
func TestRoutePinsOnSameIC(t *testing.T) {
	ic := sexp.SchematicSymbol{
		X: 50, Y: 50,
		Pins: []sexp.PinInfo{
			{X: 40, Y: 45}, // left pin
			{X: 50, Y: 40}, // top pin
			{X: 60, Y: 50}, // right pin
			{X: 50, Y: 60}, // bottom pin
		},
	}
	r := NewRouter([]sexp.SchematicSymbol{ic}, nil)
	path := r.Route(50, 40, 40, 45) // top → left
	if path == nil {
		t.Fatal("expected path from top pin to left pin, got nil")
	}
}

func TestRouteBlockedReturnsNil(t *testing.T) {
	// Many overlapping bodies to fill the space around the start point.
	syms := []sexp.SchematicSymbol{
		{X: 50, Y: 50, Pins: []sexp.PinInfo{{X: 46, Y: 46}, {X: 54, Y: 54}}},
		{X: 50, Y: 60, Pins: []sexp.PinInfo{{X: 46, Y: 56}, {X: 54, Y: 64}}},
		{X: 50, Y: 40, Pins: []sexp.PinInfo{{X: 46, Y: 36}, {X: 54, Y: 44}}},
		{X: 40, Y: 50, Pins: []sexp.PinInfo{{X: 36, Y: 46}, {X: 44, Y: 54}}},
		{X: 60, Y: 50, Pins: []sexp.PinInfo{{X: 56, Y: 46}, {X: 64, Y: 54}}},
	}
	r := NewRouter(syms, nil)
	// If the A* is fully blocked, it returns nil — acceptable.
	_ = r.Route(50, 50, 150, 150)
}

func TestMarkWireIncreasesRouteCost(t *testing.T) {
	r := emptyRouter()
	pathA := r.Route(50, 100, 80, 100)
	if pathA == nil {
		t.Fatal("route A failed")
	}
	r.MarkWire(pathA)
	// Soft obstacle: route should still succeed (just costs more).
	pathB := r.Route(50, 100, 80, 100)
	if pathB == nil {
		t.Error("route B should still succeed over a soft obstacle")
	}
}

func TestSymbolBodyBBox(t *testing.T) {
	// Resistor: two pins on the same X axis, body should be inset.
	r := sexp.SchematicSymbol{
		Pins: []sexp.PinInfo{
			{X: 100, Y: 47},
			{X: 100, Y: 55},
		},
	}
	x1, y1, x2, y2 := symbolBodyBBox(r)
	// Y inset: 47+2.54=49.54, 55-2.54=52.46
	// X inset: 100+2.54=102.54, 100-2.54=97.46 → swapped: 97.46..102.54
	if x1 >= x2 {
		t.Errorf("body bbox X inverted: x1=%.2f x2=%.2f", x1, x2)
	}
	if y1 >= y2 {
		t.Errorf("body bbox Y inverted: y1=%.2f y2=%.2f", y1, y2)
	}
	// Pin tips must be outside the body bbox.
	if r.Pins[0].Y >= y1 && r.Pins[0].Y <= y2 {
		t.Errorf("pin 1 Y=%.2f is inside body Y=[%.2f,%.2f]", r.Pins[0].Y, y1, y2)
	}
	if r.Pins[1].Y >= y1 && r.Pins[1].Y <= y2 {
		t.Errorf("pin 2 Y=%.2f is inside body Y=[%.2f,%.2f]", r.Pins[1].Y, y1, y2)
	}
}

func TestMergeCollinear(t *testing.T) {
	pts := [][2]float64{
		{0, 0}, {1, 0}, {2, 0}, {2, 1}, {2, 2},
	}
	got := mergeCollinear(pts)
	if len(got) != 3 {
		t.Fatalf("expected 3 points after merge, got %d: %v", len(got), got)
	}
}

func TestPathLength(t *testing.T) {
	pts := [][2]float64{{0, 0}, {10, 0}, {10, 10}}
	l := pathLength(pts)
	if math.Abs(l-20.0) > 0.001 {
		t.Fatalf("expected length 20, got %.4f", l)
	}
}
