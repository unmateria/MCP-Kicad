package tools

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcp-kicad/internal/sexp"
)

// The compiler used to draw a circuit at whatever size filled its page, because
// scoreSheet rewarded covering 42% of the sheet and the search reached that by
// multiplying every spacing by 1.5 or 2.0 until it got there. Nothing in the
// suite noticed: the netlist still verified, the gate still passed, ERC was
// still clean. What it cost was readability — ne555_astable came out spread
// over 29× the area a human needs for the same seven parts.
//
// It was a reader who caught it, by redrawing three of these schematics by hand
// next to the compiler's output. Those redrawings are the fixtures here: each
// file holds BOTH blocks, the compiler's on one side and the human's on the
// other, which is why the human's parts carry their own reference numbers.
//
// The bound is on the drawing's SIZE relative to the hand-drawn one. It is
// deliberately generous — this is a guard against the compiler losing its sense
// of scale again, not a demand that it match a human stroke for stroke.
type humanCase struct {
	design   string   // source in docs/compiler/
	fixture  string   // hand-drawn reference in testdata/human/
	humanRef []string // the references the human's block uses
	maxRatio float64  // most of the human's area the compiler may use
}

var humanCases = []humanCase{
	{"led_18650.design.json", "led_18650.kicad_sch",
		[]string{"BT2", "R2", "D2"}, 1.5},
	{"ne555_astable.design.json", "ne555_astable.kicad_sch",
		[]string{"U2", "R4", "R5", "R6", "C3", "C4", "D2"}, 2.0},
	{"opto_relay_driver.design.json", "opto_relay_driver.kicad_sch",
		[]string{"J3", "J4", "U2", "Q2", "R4", "R5", "R6", "D2", "RLY2"}, 2.0},
}

// symbolArea is the bbox of the given symbols, or of every real part when refs
// is nil. Power symbols are excluded on both sides: they are placed per pin, so
// counting them would compare two different conventions.
func symbolArea(sch *sexp.Schematic, refs []string) float64 {
	want := map[string]bool{}
	for _, r := range refs {
		want[r] = true
	}
	x1, y1 := math.Inf(1), math.Inf(1)
	x2, y2 := math.Inf(-1), math.Inf(-1)
	for _, sym := range sexp.ReadSymbols(sch) {
		if strings.HasPrefix(sym.Reference, "#") {
			continue
		}
		if refs != nil && !want[sym.Reference] {
			continue
		}
		x1, y1 = math.Min(x1, sym.X), math.Min(y1, sym.Y)
		x2, y2 = math.Max(x2, sym.X), math.Max(y2, sym.Y)
	}
	if math.IsInf(x1, 1) {
		return 0
	}
	// A row of parts is one-dimensional; give it a cell of thickness so its
	// area is comparable rather than zero.
	return math.Max(x2-x1, 2.54) * math.Max(y2-y1, 2.54)
}

func TestCompiledDrawingStaysHumanSized(t *testing.T) {
	e := tidyEnv(t)

	for _, tc := range humanCases {
		t.Run(tc.design, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", "human", tc.fixture))
			if err != nil {
				t.Fatal(err)
			}
			ref, err := sexp.ParseSchematic(string(raw))
			if err != nil {
				t.Fatal(err)
			}
			humanArea := symbolArea(ref, tc.humanRef)
			if humanArea == 0 {
				t.Fatalf("fixture %s has none of %v", tc.fixture, tc.humanRef)
			}

			d := loadDesign(t, tc.design)
			sch, _, defects, err := e.buildSchematic(d, buildOpts{})
			if err != nil {
				t.Fatal(err)
			}
			sch, _, _, _ = e.tidy(d, sch, "", defects)

			got := symbolArea(sch, nil)
			ratio := got / humanArea
			t.Logf("compiler %.0f mm², human %.0f mm² (×%.2f)", got, humanArea, ratio)
			if ratio > tc.maxRatio {
				t.Errorf("drawing is ×%.2f the hand-drawn area, limit ×%.2f — "+
					"the placement has lost its sense of scale again",
					ratio, tc.maxRatio)
			}
		})
	}
}
