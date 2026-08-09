package easyeda

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mcp-kicad/internal/config"
	"mcp-kicad/internal/fplib"
	"mcp-kicad/internal/sexp"
	"mcp-kicad/internal/symlib"
)

// load reads a recorded API response. The suite must not depend on the
// network, and these files are the measurements the converter was written
// against — re-record them with:
//
//	curl -A "<a browser UA>" \
//	  "https://easyeda.com/api/products/C115450/components?version=6.4.19.5" \
//	  -o testdata/C115450.json
func load(t *testing.T, id string) *Component {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Success bool            `json:"success"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}
	var c Component
	if err := json.Unmarshal(env.Result, &c); err != nil {
		t.Fatal(err)
	}
	return &c
}

func TestConvert_Optocoupler(t *testing.T) {
	got, err := Convert(load(t, "C115450"))
	if err != nil {
		t.Fatal(err)
	}
	if got.MPN != "LTV-217-B-G" {
		t.Errorf("MPN = %q", got.MPN)
	}
	if got.Manufacturer == "" {
		t.Error("expected a manufacturer")
	}
	if !strings.HasPrefix(got.Package, "SOP-4") {
		t.Errorf("package = %q", got.Package)
	}
	if got.LCSC != "C115450" {
		t.Errorf("LCSC = %q", got.LCSC)
	}

	lib, err := symlib.Parse(got.SymbolLib)
	if err != nil {
		t.Fatalf("the converted symbol does not parse: %v\n%s", err, got.SymbolLib)
	}
	sym, ok := lib.Get(got.SymbolName)
	if !ok {
		t.Fatalf("library holds %v", lib.Names())
	}
	pins := symlib.PinNumbers(sym)
	if len(pins) != 4 {
		t.Fatalf("expected 4 pins, got %v", pins)
	}

	fp, err := fplib.Parse(got.Footprint)
	if err != nil {
		t.Fatalf("the converted footprint does not parse: %v\n%s", err, got.Footprint)
	}
	if pads := fp.PadNumbers(); len(pads) != 4 {
		t.Errorf("expected 4 pads, got %v", pads)
	}
}

// The geometry has to be right, not merely present. SOP-4_L4.4-W2.8-P1.27-LS7.0
// states its own dimensions: 1.27 mm pitch and a 7.0 mm lead span. If the scale
// constant or the origin were wrong, this is where it shows.
func TestConvert_FootprintGeometryMatchesThePackageName(t *testing.T) {
	got, err := Convert(load(t, "C115450"))
	if err != nil {
		t.Fatal(err)
	}
	pads := padCentres(t, got.Footprint)
	if len(pads) != 4 {
		t.Fatalf("expected 4 pads, got %d", len(pads))
	}

	// Lead span: the horizontal distance between the two pad columns.
	minX, maxX := math.Inf(1), math.Inf(-1)
	for _, p := range pads {
		minX = math.Min(minX, p.x)
		maxX = math.Max(maxX, p.x)
	}
	if span := maxX - minX; math.Abs(span-7.0) > 0.05 {
		t.Errorf("lead span = %.3f mm, the package name says 7.0", span)
	}

	// Pitch: the vertical distance between adjacent pads in one column.
	var col []float64
	for _, p := range pads {
		if math.Abs(p.x-minX) < 0.01 {
			col = append(col, p.y)
		}
	}
	if len(col) != 2 {
		t.Fatalf("expected 2 pads in the left column, got %d", len(col))
	}
	if pitch := math.Abs(col[0] - col[1]); math.Abs(pitch-1.27) > 0.02 {
		t.Errorf("pitch = %.3f mm, the package name says 1.27", pitch)
	}

	// The footprint must be centred on its own origin, not floating off at the
	// EasyEDA document's absolute coordinates.
	for _, p := range pads {
		if math.Abs(p.x) > 10 || math.Abs(p.y) > 10 {
			t.Errorf("pad at (%.2f, %.2f) is nowhere near the origin — the document offset was not removed", p.x, p.y)
		}
	}
}

// SSOP-8_L4.4-W3.5-P0.65: a finer pitch, to catch a scale error that a 1.27 mm
// part would round past.
func TestConvert_FinePitchPackage(t *testing.T) {
	got, err := Convert(load(t, "C7429"))
	if err != nil {
		t.Fatal(err)
	}
	pads := padCentres(t, got.Footprint)
	if len(pads) != 8 {
		t.Fatalf("expected 8 pads, got %d", len(pads))
	}
	var row []float64
	for _, p := range pads {
		if p.y < 0 {
			row = append(row, p.x)
		}
	}
	if len(row) < 2 {
		t.Fatalf("expected a row of pads, got %v", row)
	}
	best := math.Inf(1)
	for i := range row {
		for j := i + 1; j < len(row); j++ {
			if d := math.Abs(row[i] - row[j]); d > 0.01 && d < best {
				best = d
			}
		}
	}
	if math.Abs(best-0.65) > 0.02 {
		t.Errorf("closest pad spacing = %.3f mm, the package name says 0.65", best)
	}
}

// A TO-220 has real drilled holes. A converter that treated every pad as SMD
// would produce a part that cannot be assembled and looks fine on screen.
func TestConvert_ThroughHolePackage(t *testing.T) {
	got, err := Convert(load(t, "C111887"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(got.Footprint)
	if !strings.Contains(body, "thru_hole") {
		t.Errorf("a TO-220 must have through-hole pads:\n%s", body)
	}
	if !strings.Contains(body, "(drill ") {
		t.Errorf("through-hole pads must carry a drill:\n%s", body)
	}
	if !strings.Contains(body, "(attr through_hole)") {
		t.Error("the footprint should be marked through_hole")
	}
	fp, err := fplib.Parse(got.Footprint)
	if err != nil {
		t.Fatal(err)
	}
	if pads := fp.PadNumbers(); len(pads) != 3 {
		t.Errorf("expected 3 pads, got %v", pads)
	}
}

// Pins must come back with the direction EasyEDA drew, not the one its
// rotation field claims: the two disagree, and trusting the field mirrors
// every symbol.
func TestConvert_PinDirectionsComeFromTheDrawnStem(t *testing.T) {
	got, err := Convert(load(t, "C6186")) // AMS1117-3.3: 3 pins left, 1 right
	if err != nil {
		t.Fatal(err)
	}
	lib, err := symlib.Parse(got.SymbolLib)
	if err != nil {
		t.Fatal(err)
	}
	sym, _ := lib.Get(got.SymbolName)

	angles := map[string]int{}
	var walk func(n *sexp.Node)
	walk = func(n *sexp.Node) {
		for _, c := range n.Children {
			if !c.IsList() {
				continue
			}
			if c.Head() == "pin" {
				num := ""
				if nn := sexp.FindList(c, "number"); nn != nil {
					num = sexp.StringValue(nn, 1)
				}
				if at := sexp.FindList(c, "at"); at != nil {
					angles[num] = int(atof(sexp.AtomValue(at, 3)))
				}
				continue
			}
			walk(c)
		}
	}
	walk(sym)

	if len(angles) != 4 {
		t.Fatalf("expected 4 pins, got %v", angles)
	}
	// EasyEDA's record says rotation 180 for pins 1-3 and 0 for pin 4; the
	// drawn stems say the opposite. KiCad's angle is the direction from the
	// connection point towards the body, so a left-hand pin is 0.
	for _, n := range []string{"1", "2", "3"} {
		if angles[n] != 0 {
			t.Errorf("pin %s angle = %d, want 0 (left side, stem running right)", n, angles[n])
		}
	}
	if angles["4"] != 180 {
		t.Errorf("pin 4 angle = %d, want 180 (right side, stem running left)", angles["4"])
	}
}

// The post-condition every converted part has to pass before it can be
// installed: KiCad reads it back.
func TestConvert_KicadAcceptsTheOutput(t *testing.T) {
	cli := config.DetectKicadCLI()
	if cli == "" {
		t.Skip("kicad-cli not installed")
	}
	for _, id := range []string{"C115450", "C6186", "C7429", "C111887"} {
		t.Run(id, func(t *testing.T) {
			got, err := Convert(load(t, id))
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()

			symPath := filepath.Join(dir, "Conv.kicad_sym")
			if err := os.WriteFile(symPath, got.SymbolLib, 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(cli, "sym", "upgrade", "--output", filepath.Join(dir, "up.kicad_sym"), "--force", symPath)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("KiCad rejected the converted symbol: %v\n%s\n---\n%s", err, out, got.SymbolLib)
			}

			if len(got.Footprint) == 0 {
				return
			}
			pretty := filepath.Join(dir, "Conv.pretty")
			if err := os.MkdirAll(pretty, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(pretty, "fp.kicad_mod"), got.Footprint, 0o644); err != nil {
				t.Fatal(err)
			}
			cmd = exec.Command(cli, "fp", "upgrade", "--output", filepath.Join(dir, "up.pretty"), "--force", pretty)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("KiCad rejected the converted footprint: %v\n%s\n---\n%s", err, out, got.Footprint)
			}
		})
	}
}

func TestClient_Fetch(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "C115450.json"))
	if err != nil {
		t.Fatal(err)
	}
	var gotUA, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotPath = r.URL.RequestURI()
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c := NewClient(srv.Client())
	c.BaseURL = srv.URL
	comp, err := c.Fetch(context.Background(), "c115450")
	if err != nil {
		t.Fatal(err)
	}
	if comp.MPN() != "LTV-217-B-G" {
		t.Errorf("MPN = %q", comp.MPN())
	}
	if !strings.Contains(gotUA, "Mozilla") {
		t.Errorf("the API refuses a non-browser agent; got %q", gotUA)
	}
	if !strings.Contains(gotPath, "C115450") {
		t.Errorf("the part number must be upper-cased in the URL, got %q", gotPath)
	}
	if !strings.Contains(gotPath, "version=") {
		t.Errorf("the version parameter is required or packageDetail comes back missing, got %q", gotPath)
	}
}

func TestClient_FetchReportsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":false,"code":404,"message":"not found"}`))
	}))
	defer srv.Close()
	c := NewClient(srv.Client())
	c.BaseURL = srv.URL
	if _, err := c.Fetch(context.Background(), "C1"); err == nil {
		t.Error("expected an error when the API reports failure")
	}
}

func TestParsePathAndArcMidpoint(t *testing.T) {
	segs := parsePath("M 360 290 h 20")
	if len(segs) != 2 || segs[1].cmd != 'L' {
		t.Fatalf("h shorthand not handled: %+v", segs)
	}
	if segs[1].points[1].X != 380 || segs[1].points[1].Y != 290 {
		t.Errorf("h 20 from (360,290) should end at (380,290), got %+v", segs[1].points[1])
	}

	// A semicircle of radius 5 from (0,0) to (10,0) peaks at (5,±5).
	segs = parsePath("M 0 0 A 5 5 0 0 1 10 0")
	if len(segs) != 2 || segs[1].cmd != 'A' {
		t.Fatalf("arc not parsed: %+v", segs)
	}
	mid, ok := arcMidpoint(segs[1].arc)
	if !ok {
		t.Fatal("arc midpoint not computed")
	}
	if math.Abs(mid.X-5) > 1e-6 || math.Abs(math.Abs(mid.Y)-5) > 1e-6 {
		t.Errorf("midpoint = (%.4f, %.4f), want (5, ±5)", mid.X, mid.Y)
	}
}

type centre struct{ x, y float64 }

func padCentres(t *testing.T, data []byte) []centre {
	t.Helper()
	nodes, err := sexp.Parse(string(data))
	if err != nil {
		t.Fatalf("footprint does not parse: %v", err)
	}
	var out []centre
	for _, c := range nodes[0].Children {
		if c.Head() != "pad" {
			continue
		}
		at := sexp.FindList(c, "at")
		if at == nil {
			continue
		}
		out = append(out, centre{atof(sexp.AtomValue(at, 1)), atof(sexp.AtomValue(at, 2))})
	}
	return out
}
