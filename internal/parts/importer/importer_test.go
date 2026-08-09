package importer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcp-kicad/internal/config"
	"mcp-kicad/internal/fplib"
	"mcp-kicad/internal/parts"
	"mcp-kicad/internal/parts/providers"
	"mcp-kicad/internal/symlib"
)

const goodSymbol = `(kicad_symbol_lib
	(version 20241209)
	(generator "test")
	(symbol "ltv-217-b-g"
		(exclude_from_sim no)
		(in_bom yes)
		(on_board yes)
		(property "Reference" "U" (at 0 5.08 0))
		(property "Value" "ltv-217-b-g" (at 0 -5.08 0))
		(property "Footprint" "PCM_TEST:SOP-4" (at 0 0 0))
		(property "Datasheet" "https://example.invalid/ltv217.pdf" (at 0 0 0))
		(property "Description" "Phototransistor optocoupler" (at 0 0 0))
		(symbol "ltv-217-b-g_1_1"
			(rectangle (start -5.08 3.81) (end 5.08 -3.81)
				(stroke (width 0.254) (type default))
				(fill (type none)))
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
)`

// pinlessSymbol is the failure the post-condition exists for: it parses, it is
// a perfectly well-formed library, and nothing can ever connect to it.
const pinlessSymbol = `(kicad_symbol_lib
	(version 20241209)
	(generator "test")
	(symbol "EMPTY-PART"
		(property "Reference" "U" (at 0 0 0))
		(property "Value" "EMPTY-PART" (at 0 0 0))
		(symbol "EMPTY-PART_1_1"
			(rectangle (start -5.08 3.81) (end 5.08 -3.81)
				(stroke (width 0.254) (type default))
				(fill (type none)))
		)
	)
)`

const testFootprint = `(footprint "SOP-4"
	(version 20240108)
	(generator "test")
	(layer "F.Cu")
	(descr "SOP-4 optocoupler")
	(property "Reference" "REF**" (at 0 0 0) (layer "F.SilkS"))
	(property "Value" "SOP-4" (at 0 0 0) (layer "F.Fab"))
	(attr smd)
	(pad "1" smd rect (at -2.2 -1.27) (size 1 0.6) (layers "F.Cu" "F.Paste" "F.Mask"))
	(pad "2" smd rect (at -2.2 1.27) (size 1 0.6) (layers "F.Cu" "F.Paste" "F.Mask"))
	(pad "3" smd rect (at 2.2 1.27) (size 1 0.6) (layers "F.Cu" "F.Paste" "F.Mask"))
	(pad "4" smd rect (at 2.2 -1.27) (size 1 0.6) (layers "F.Cu" "F.Paste" "F.Mask"))
)`

func testImporter(t *testing.T) *Importer {
	t.Helper()
	return &Importer{
		LibsRoot: t.TempDir(),
		KicadCLI: config.DetectKicadCLI(),
		Now:      func() time.Time { return time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC) },
		// Never true in tests: registering writes into the user's own KiCad
		// configuration, which a test run has no business touching.
		RegisterGlobally: false,
	}
}

func bundle(sym, fp string) *providers.Bundle {
	b := &providers.Bundle{
		Candidate: providers.Candidate{
			Provider:  "testrepo",
			ID:        "lib.kicad_sym#ltv-217-b-g",
			MPN:       "ltv-217-b-g",
			License:   "MIT",
			SourceURL: "https://example.invalid/lib.kicad_sym",
			Datasheet: "https://example.invalid/ltv217.pdf",
		},
		Assets:       map[providers.AssetKind][]byte{providers.Symbol: []byte(sym)},
		FootprintRef: "PCM_TEST:SOP-4",
		SymbolName:   "ltv-217-b-g",
	}
	if fp != "" {
		b.Assets[providers.Footprint] = []byte(fp)
	}
	return b
}

func TestInstall_HappyPath(t *testing.T) {
	im := testImporter(t)
	res, err := im.Install(context.Background(), bundle(goodSymbol, testFootprint))
	if err != nil {
		t.Fatalf("install failed: %v\nchecks: %+v", err, res.Checks)
	}

	if res.LibID != "MCP_Imported:LTV-217-B-G" {
		t.Errorf("lib_id = %q, want the canonical MPN", res.LibID)
	}
	if res.PinCount != 4 {
		t.Errorf("pin count = %d, want 4", res.PinCount)
	}
	if res.FootprintRef != "MCP_Imported:LTV-217-B-G__SOP-4" {
		t.Errorf("footprint ref = %q", res.FootprintRef)
	}

	lib, err := symlib.Load(parts.ImportedSymbolLib(im.LibsRoot))
	if err != nil {
		t.Fatal(err)
	}
	sym, found := lib.Get("LTV-217-B-G")
	if !found {
		t.Fatalf("the installed library holds %v", lib.Names())
	}
	if got := symlib.Property(sym, "Footprint"); got != res.FootprintRef {
		t.Errorf("installed symbol points at %q, want %q", got, res.FootprintRef)
	}
	src := symlib.Property(sym, "MCP_Source")
	for _, want := range []string{"provider=testrepo", "example.invalid", "MIT", "imported=2026-08-09", `original="ltv-217-b-g"`} {
		if !strings.Contains(src, want) {
			t.Errorf("MCP_Source %q is missing %q", src, want)
		}
	}

	fpPath := filepath.Join(parts.ImportedFootprintLib(im.LibsRoot), "LTV-217-B-G__SOP-4.kicad_mod")
	fp, err := fplib.Load(fpPath)
	if err != nil {
		t.Fatalf("footprint not installed: %v", err)
	}
	if fp.Name() != "LTV-217-B-G__SOP-4" {
		t.Errorf("installed footprint is named %q", fp.Name())
	}
	if len(fp.PadNumbers()) != 4 {
		t.Errorf("installed footprint has %d pads", len(fp.PadNumbers()))
	}
}

// The previews have to still be there when Install returns. They used to be
// written into the staging directory, which is deleted on the way out, so the
// caller rendered two identical screenshots of the browser's file-not-found
// page and presented them as the symbol and the footprint.
func TestInstall_PreviewsSurviveInstall(t *testing.T) {
	im := testImporter(t)
	if im.KicadCLI == "" {
		t.Skip("kicad-cli not installed")
	}
	im.PreviewDir = t.TempDir()

	res, err := im.Install(context.Background(), bundle(goodSymbol, testFootprint))
	if err != nil {
		t.Fatalf("%v\nchecks: %+v", err, res.Checks)
	}
	if res.SymbolSVG == "" {
		t.Fatalf("no symbol preview was produced; checks: %+v", res.Checks)
	}
	symInfo, err := os.Stat(res.SymbolSVG)
	if err != nil {
		t.Fatalf("the symbol preview does not survive Install: %v", err)
	}
	if res.FootprintSVG == "" {
		t.Fatalf("no footprint preview was produced; warnings: %v", res.Warnings)
	}
	fpInfo, err := os.Stat(res.FootprintSVG)
	if err != nil {
		t.Fatalf("the footprint preview does not survive Install: %v", err)
	}
	// Two drawings of two different things cannot be the same file.
	if res.SymbolSVG == res.FootprintSVG {
		t.Error("the symbol and footprint previews are the same file")
	}
	if symInfo.Size() == fpInfo.Size() {
		t.Errorf("both previews are %d bytes — they are almost certainly the same image", symInfo.Size())
	}
}

// Without a preview directory the import still succeeds; it just draws nothing.
func TestInstall_WithoutPreviewDir(t *testing.T) {
	im := testImporter(t)
	im.PreviewDir = ""
	res, err := im.Install(context.Background(), bundle(goodSymbol, testFootprint))
	if err != nil {
		t.Fatalf("%v\nchecks: %+v", err, res.Checks)
	}
	if res.SymbolSVG != "" || res.FootprintSVG != "" {
		t.Error("no preview directory means no previews")
	}
}

// The post-condition: a part that fails verification leaves nothing behind.
func TestInstall_PinlessSymbolInstallsNothing(t *testing.T) {
	im := testImporter(t)
	b := bundle(pinlessSymbol, "")
	b.MPN = "EMPTY-PART"

	res, err := im.Install(context.Background(), b)
	if err == nil {
		t.Fatal("a symbol with no pins must be refused")
	}
	if !res.Failed() {
		t.Error("the result must carry a fatal check explaining why")
	}
	if _, statErr := os.Stat(parts.ImportedSymbolLib(im.LibsRoot)); statErr == nil {
		lib, _ := symlib.Load(parts.ImportedSymbolLib(im.LibsRoot))
		if len(lib.Names()) != 0 {
			t.Errorf("a refused part must not be installed, found %v", lib.Names())
		}
	}
}

func TestInstall_GarbageIsRefused(t *testing.T) {
	im := testImporter(t)
	b := bundle("this is not an s-expression at all", "")
	if _, err := im.Install(context.Background(), b); err == nil {
		t.Fatal("unparseable input must be refused")
	}
}

// Re-importing the same part replaces it rather than accumulating duplicates,
// and a second part joins it instead of wiping the library.
func TestInstall_MergesIntoOneLibrary(t *testing.T) {
	im := testImporter(t)
	if _, err := im.Install(context.Background(), bundle(goodSymbol, testFootprint)); err != nil {
		t.Fatal(err)
	}
	if _, err := im.Install(context.Background(), bundle(goodSymbol, testFootprint)); err != nil {
		t.Fatal(err)
	}

	second := bundle(strings.ReplaceAll(goodSymbol, "ltv-217-b-g", "second-part"), testFootprint)
	second.MPN = "second-part"
	if _, err := im.Install(context.Background(), second); err != nil {
		t.Fatal(err)
	}

	lib, err := symlib.Load(parts.ImportedSymbolLib(im.LibsRoot))
	if err != nil {
		t.Fatal(err)
	}
	names := lib.Names()
	if len(names) != 2 || names[0] != "LTV-217-B-G" || names[1] != "SECOND-PART" {
		t.Errorf("expected both parts exactly once, got %v", names)
	}
}

// When the source ships no footprint but names one KiCad already has, the
// reference is kept — there is nothing to copy and nothing to invent.
func TestInstall_ResolvesFootprintFromInstalledLibraries(t *testing.T) {
	im := testImporter(t)
	fpDir := filepath.Join(t.TempDir(), "Package_SO.pretty")
	if err := os.MkdirAll(fpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fpDir, "SOIC-8_3.9x4.9mm_P1.27mm.kicad_mod"), []byte(testFootprint), 0o644); err != nil {
		t.Fatal(err)
	}
	im.KicadFootprints = filepath.Dir(fpDir)

	b := bundle(goodSymbol, "")
	b.FootprintRef = "SomeVendorLib:SOIC-8_3.9x4.9mm_P1.27mm"

	res, err := im.Install(context.Background(), b)
	if err != nil {
		t.Fatalf("%v\nchecks: %+v", err, res.Checks)
	}
	if res.FootprintRef != "Package_SO:SOIC-8_3.9x4.9mm_P1.27mm" {
		t.Errorf("footprint ref = %q, want it remapped to the library that actually holds it", res.FootprintRef)
	}
	if res.FootprintPath != "" {
		t.Error("an already-installed footprint must not be copied into the imported library")
	}
}

// An unresolvable footprint is a loud warning, not a substitution.
func TestInstall_UnknownFootprintWarnsAndInstallsSymbol(t *testing.T) {
	im := testImporter(t)
	b := bundle(goodSymbol, "")
	b.FootprintRef = "Nowhere:DOES-NOT-EXIST"

	res, err := im.Install(context.Background(), b)
	if err != nil {
		t.Fatalf("the symbol is still worth installing: %v", err)
	}
	if res.FootprintRef != "" {
		t.Errorf("footprint ref = %q, want it left empty rather than guessed", res.FootprintRef)
	}
	joined := strings.Join(res.Warnings, " ")
	if !strings.Contains(joined, "DOES-NOT-EXIST") {
		t.Errorf("expected a warning naming the missing footprint, got %v", res.Warnings)
	}
}

func TestImportedParts(t *testing.T) {
	im := testImporter(t)
	if _, err := im.Install(context.Background(), bundle(goodSymbol, testFootprint)); err != nil {
		t.Fatal(err)
	}
	list, err := ImportedParts(im.LibsRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected one imported part, got %+v", list)
	}
	if list[0].Pins != 4 || !strings.Contains(list[0].Source, "provider=testrepo") {
		t.Errorf("unexpected row: %+v", list[0])
	}
}

func TestCanonicalMPN(t *testing.T) {
	cases := map[string]string{
		"ne555p":            "NE555P",
		"ESP32-C3-MINI-1":   "ESP32-C3-MINI-1",
		"LM2596S-5.0":       "LM2596S-5.0",
		"Foo / Bar (rev A)": "FOO_BAR_REV_A",
		"  spaced  out  ":   "SPACED_OUT",
		"__weird__":         "WEIRD",
	}
	for in, want := range cases {
		if got := CanonicalMPN(in); got != want {
			t.Errorf("CanonicalMPN(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFootprintName(t *testing.T) {
	cases := []struct{ mpn, pkg, want string }{
		{"NE555P", "DIP-8_W7.62mm", "NE555P__DIP-8_W7.62mm"},
		{"NE555P", "", "NE555P"},
		{"NE555P", "NE555P__DIP-8", "NE555P__DIP-8"},
		{"X", "bad/name:here", "X__bad_name_here"},
	}
	for _, c := range cases {
		if got := FootprintName(c.mpn, c.pkg); got != c.want {
			t.Errorf("FootprintName(%q,%q) = %q, want %q", c.mpn, c.pkg, got, c.want)
		}
	}
}
