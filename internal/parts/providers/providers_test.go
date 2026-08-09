package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcp-kicad/internal/fplib"
	"mcp-kicad/internal/parts"
	"mcp-kicad/internal/symlib"
)

const testSymbolLib = `(kicad_symbol_lib
	(version 20241209)
	(generator "test")
	(symbol "LTV-217-B-G"
		(property "Reference" "U" (at 0 0 0))
		(property "Value" "LTV-217-B-G" (at 0 0 0))
		(property "Footprint" "PCM_TEST:SOP-4_4.4x2.6mm_P1.27mm" (at 0 0 0))
		(property "Datasheet" "https://example.invalid/ltv217.pdf" (at 0 0 0))
		(property "Description" "Phototransistor optocoupler SOP-4" (at 0 0 0))
		(property "LCSC" "C115450" (at 0 0 0))
		(symbol "LTV-217-B-G_1_1"
			(pin passive line (at -7.62 2.54 0) (length 2.54)
				(name "A" (effects (font (size 1.27 1.27))))
				(number "1" (effects (font (size 1.27 1.27)))))
			(pin passive line (at -7.62 -2.54 0) (length 2.54)
				(name "K" (effects (font (size 1.27 1.27))))
				(number "2" (effects (font (size 1.27 1.27)))))
			(pin passive line (at 7.62 -2.54 180) (length 2.54)
				(name "E" (effects (font (size 1.27 1.27))))
				(number "3" (effects (font (size 1.27 1.27)))))
			(pin passive line (at 7.62 2.54 180) (length 2.54)
				(name "C" (effects (font (size 1.27 1.27))))
				(number "4" (effects (font (size 1.27 1.27)))))
		)
	)
	(symbol "BASE_OPTO"
		(property "Reference" "U" (at 0 0 0))
		(property "Value" "BASE_OPTO" (at 0 0 0))
		(property "Description" "Generic optocoupler base" (at 0 0 0))
		(symbol "BASE_OPTO_1_1"
			(pin passive line (at -7.62 0 0) (length 2.54)
				(name "A" (effects (font (size 1.27 1.27))))
				(number "1" (effects (font (size 1.27 1.27)))))
		)
	)
	(symbol "DERIVED-OPTO"
		(extends "BASE_OPTO")
		(property "Value" "DERIVED-OPTO" (at 0 0 0))
		(property "Footprint" "PCM_TEST:SOP-4_4.4x2.6mm_P1.27mm" (at 0 0 0))
	)
)`

const testFootprint = `(footprint "SOP-4_4.4x2.6mm_P1.27mm"
	(version 20240108)
	(generator "test")
	(layer "F.Cu")
	(descr "SOP-4 optocoupler")
	(property "Reference" "REF**" (at 0 0 0) (layer "F.SilkS"))
	(property "Value" "SOP-4_4.4x2.6mm_P1.27mm" (at 0 0 0) (layer "F.Fab"))
	(pad "1" smd rect (at -2.2 -1.27) (size 1 0.6) (layers "F.Cu"))
	(pad "2" smd rect (at -2.2 1.27) (size 1 0.6) (layers "F.Cu"))
	(pad "3" smd rect (at 2.2 1.27) (size 1 0.6) (layers "F.Cu"))
	(pad "4" smd rect (at 2.2 -1.27) (size 1 0.6) (layers "F.Cu"))
	(model "${KISYS3DMOD}/SOP-4_4.4x2.6mm_P1.27mm.step"
		(offset (xyz 0 0 0))
		(scale (xyz 1 1 1))
		(rotate (xyz 0 0 0))
	)
)`

// fakeRepo serves a repository the way GitHub does: a recursive tree listing
// and raw files. The suite must not depend on the network, so every provider
// test runs against this instead of the real thing.
func fakeRepo(t *testing.T) (*httptest.Server, map[string]int) {
	t.Helper()
	files := map[string]string{
		"symbols/Test-Opto.kicad_sym":                              testSymbolLib,
		"footprints/Test.pretty/SOP-4_4.4x2.6mm_P1.27mm.kicad_mod": testFootprint,
		"3dmodels/SOP-4_4.4x2.6mm_P1.27mm.step":                    "ISO-10303-21;\nfake step\nEND-ISO-10303-21;",
		"README.md":                                                "ignored",
	}
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits[r.URL.Path]++
		if r.URL.Path == "/tree" {
			type node struct {
				Path string `json:"path"`
				Type string `json:"type"`
			}
			var out struct {
				Tree []node `json:"tree"`
			}
			for p := range files {
				out.Tree = append(out.Tree, node{Path: p, Type: "blob"})
			}
			_ = json.NewEncoder(w).Encode(out)
			return
		}
		body, ok := files[strings.TrimPrefix(r.URL.Path, "/raw/")]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, hits
}

func testProvider(t *testing.T, srv *httptest.Server, libsRoot string) *repoProvider {
	t.Helper()
	return &repoProvider{
		env: Env{LibsRoot: libsRoot, HTTP: srv.Client()},
		src: repoSource{
			name:         "testrepo",
			description:  "test",
			license:      "MIT",
			homepage:     srv.URL,
			symbolDir:    "symbols/",
			footprintDir: "footprints/",
			listTree: func(ctx context.Context, p *repoProvider, _ []string) ([]string, error) {
				data, err := get(ctx, p.env.Client(), srv.URL+"/tree", nil)
				if err != nil {
					return nil, err
				}
				var payload struct {
					Tree []struct{ Path, Type string } `json:"tree"`
				}
				if err := json.Unmarshal(data, &payload); err != nil {
					return nil, err
				}
				var out []string
				for _, n := range payload.Tree {
					out = append(out, n.Path)
				}
				return out, nil
			},
			rawURL: func(p string) string { return srv.URL + "/raw/" + escapePath(p) },
		},
	}
}

func TestRepoProvider_BuildsIndexAndSearches(t *testing.T) {
	srv, _ := fakeRepo(t)
	root := t.TempDir()
	p := testProvider(t, srv, root)

	cands, err := p.Search(context.Background(), Query{Text: "LTV-217"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) == 0 {
		t.Fatal("expected a hit for LTV-217")
	}
	if cands[0].MPN != "LTV-217-B-G" {
		t.Errorf("best hit = %q, want LTV-217-B-G", cands[0].MPN)
	}
	if cands[0].Package != "SOP-4_4.4x2.6mm_P1.27mm" {
		t.Errorf("package = %q", cands[0].Package)
	}
	if cands[0].Datasheet == "" {
		t.Error("expected the datasheet to survive indexing")
	}

	// The index is on disk and the next search must not need the network.
	if _, err := os.Stat(indexPath(root, "testrepo")); err != nil {
		t.Fatalf("index was not cached: %v", err)
	}
	srv.Close()
	if _, err := p.Search(context.Background(), Query{Text: "LTV-217"}); err != nil {
		t.Errorf("a second search must be served from the cache, got %v", err)
	}
}

func TestRepoProvider_FetchPairsFootprintAndModel(t *testing.T) {
	srv, _ := fakeRepo(t)
	p := testProvider(t, srv, t.TempDir())
	if _, err := p.Search(context.Background(), Query{Text: "LTV"}); err != nil {
		t.Fatal(err)
	}

	b, err := p.Fetch(context.Background(), "symbols/Test-Opto.kicad_sym#LTV-217-B-G")
	if err != nil {
		t.Fatal(err)
	}
	if b.FootprintRef != "PCM_TEST:SOP-4_4.4x2.6mm_P1.27mm" {
		t.Errorf("FootprintRef = %q", b.FootprintRef)
	}
	sym, err := symlib.Parse(b.Assets[Symbol])
	if err != nil {
		t.Fatal(err)
	}
	node, ok := sym.Get("LTV-217-B-G")
	if !ok {
		t.Fatalf("the bundle's library holds %v", sym.Names())
	}
	if got := len(symlib.PinNumbers(node)); got != 4 {
		t.Errorf("symbol came back with %d pins, want 4", got)
	}
	if len(sym.Names()) != 1 {
		t.Errorf("the bundle must hold only the part asked for, got %v", sym.Names())
	}

	fpData, ok := b.Assets[Footprint]
	if !ok {
		t.Fatalf("footprint not paired; notes: %v", b.Notes)
	}
	fp, err := fplib.Parse(fpData)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(fp.PadNumbers()); got != 4 {
		t.Errorf("footprint came back with %d pads, want 4", got)
	}
	if _, ok := b.Assets[Model3D]; !ok {
		t.Errorf("the 3D model the footprint names should have come along; notes: %v", b.Notes)
	}
	if b.Model3DExt != ".step" {
		t.Errorf("Model3DExt = %q, want .step — KiCad picks its loader by extension", b.Model3DExt)
	}
}

// A derived symbol must arrive with the pins it inherits. This is the single
// most common way an imported part turns out to be useless.
func TestRepoProvider_FetchFlattensDerivedSymbol(t *testing.T) {
	srv, _ := fakeRepo(t)
	p := testProvider(t, srv, t.TempDir())
	if _, err := p.Search(context.Background(), Query{Text: "DERIVED"}); err != nil {
		t.Fatal(err)
	}
	b, err := p.Fetch(context.Background(), "symbols/Test-Opto.kicad_sym#DERIVED-OPTO")
	if err != nil {
		t.Fatal(err)
	}
	sym, err := symlib.Parse(b.Assets[Symbol])
	if err != nil {
		t.Fatal(err)
	}
	node, _ := sym.Get("DERIVED-OPTO")
	if got := len(symlib.PinNumbers(node)); got != 1 {
		t.Errorf("derived symbol arrived with %d pins, want the 1 it inherits", got)
	}
}

func TestRepoProvider_UnknownIDIsAnError(t *testing.T) {
	srv, _ := fakeRepo(t)
	p := testProvider(t, srv, t.TempDir())
	if _, err := p.Fetch(context.Background(), "no-hash-here"); err == nil {
		t.Error("expected an error for a malformed id")
	}
	if _, err := p.Fetch(context.Background(), "symbols/Test-Opto.kicad_sym#NOPE"); err == nil {
		t.Error("expected an error for a symbol that is not in the library")
	}
}

func TestInstalledProvider(t *testing.T) {
	root := t.TempDir()
	symDir := parts.SymbolsPath(root)
	if err := os.MkdirAll(symDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(symDir, "MCP_Imported.kicad_sym"), []byte(testSymbolLib), 0o644); err != nil {
		t.Fatal(err)
	}

	p := &installedProvider{env: Env{LibsRoot: root}}
	cands, err := p.Search(context.Background(), Query{Text: "LTV-217-B-G"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) == 0 || cands[0].LibID != "MCP_Imported:LTV-217-B-G" {
		t.Fatalf("expected the imported part to be found, got %+v", cands)
	}
	if !cands[0].Installed {
		t.Error("an installed part must be flagged as such")
	}
	if _, err := p.Fetch(context.Background(), cands[0].ID); err == nil {
		t.Error("importing an already-installed part must be refused with an explanation")
	}
}

func TestScoreRanking(t *testing.T) {
	exact := Score("NE555P", "NE555P", "", "")
	punct := Score("LM2596S-5.0", "LM2596S 5.0", "", "")
	prefix := Score("NE555", "NE555PDR", "", "")
	desc := Score("timer", "XYZ123", "precision timer", "")
	none := Score("nothing", "ABC", "def", "")

	if !(exact > punct && punct > prefix && prefix > desc && desc > 0) {
		t.Errorf("ranking out of order: exact=%d punct=%d prefix=%d desc=%d", exact, punct, prefix, desc)
	}
	if none != 0 {
		t.Errorf("a non-match must score 0, got %d", none)
	}
}

func TestLCSCNumberExtraction(t *testing.T) {
	cases := map[string]string{
		"C115450":               "C115450",
		"c115450":               "C115450",
		"LCSC C115450 in stock": "C115450",
		"(C6186)":               "C6186",
		"NE555P":                "",
		"":                      "",
		"Connector":             "",
	}
	for in, want := range cases {
		if got := lcscNumber(in); got != want {
			t.Errorf("lcscNumber(%q) = %q, want %q", in, got, want)
		}
	}
}

// The LCSC provider must stay silent on a query that is not a part number:
// EasyEDA has no keyword search, and returning a guess would put a wrong part
// at the top of a list the model then imports.
func TestLCSCIgnoresKeywordQueries(t *testing.T) {
	p := &lcscProvider{env: Env{LibsRoot: t.TempDir()}}
	got, err := p.Search(context.Background(), Query{Text: "dual op-amp SOIC-8"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected no candidates for a keyword query, got %+v", got)
	}
}

func TestParseRef(t *testing.T) {
	p, id, err := ParseRef("jlcpcb:symbols/JLCPCB-Optocouplers.kicad_sym#LTV-217-B-G")
	if err != nil {
		t.Fatal(err)
	}
	if p != "jlcpcb" || id != "symbols/JLCPCB-Optocouplers.kicad_sym#LTV-217-B-G" {
		t.Errorf("ParseRef = %q, %q", p, id)
	}
	if _, _, err := ParseRef("nocolon"); err == nil {
		t.Error("expected an error without a colon")
	}
}

// The registry has to be deterministic: a search that ranked the same two
// sources differently between runs is the class of bug this repo already paid
// for once, in the placement code.
func TestAllIsSortedAndComplete(t *testing.T) {
	provs := All(Env{LibsRoot: t.TempDir()})
	if len(provs) < 9 {
		t.Fatalf("expected the built-in sources to be registered, got %d", len(provs))
	}
	for i := 1; i < len(provs); i++ {
		if provs[i-1].Name() >= provs[i].Name() {
			t.Fatalf("All() must be sorted by name: %q before %q", provs[i-1].Name(), provs[i].Name())
		}
	}
	want := map[string]bool{
		"installed": false, "cern": false, "jlcpcb": false, "digikey-lib": false,
		"espressif": false, "sparkfun": false, "lcsc": false,
		"mouser": false, "digikey": false,
	}
	for _, p := range provs {
		if _, ok := want[p.Name()]; ok {
			want[p.Name()] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("provider %q is not registered", name)
		}
	}
}
