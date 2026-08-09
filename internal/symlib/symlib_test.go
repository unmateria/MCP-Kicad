package symlib

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mcp-kicad/internal/config"
	"mcp-kicad/internal/sexp"
)

// installedLib returns a real KiCad library path, or skips: the CI runner has
// no KiCad, and a fixture copied into the repo would stop being a measurement
// of what KiCad actually writes.
func installedLib(t *testing.T, name string) string {
	t.Helper()
	cli := config.DetectKicadCLI()
	if cli == "" {
		t.Skip("kicad-cli not installed")
	}
	root := filepath.Dir(filepath.Dir(cli))
	path := filepath.Join(root, "share", "kicad", "symbols", name+".kicad_sym")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("%s not found: %v", path, err)
	}
	return path
}

func TestLoadAndNames(t *testing.T) {
	lib, err := Load(installedLib(t, "Device"))
	if err != nil {
		t.Fatal(err)
	}
	names := lib.Names()
	if len(names) < 100 {
		t.Fatalf("Device should hold hundreds of symbols, got %d", len(names))
	}
	if _, ok := lib.Get("R"); !ok {
		t.Error("Device:R must be present")
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Fatalf("Names() must be sorted: %q before %q", names[i-1], names[i])
		}
	}
}

func TestLoad_MissingFileIsEmptyLib(t *testing.T) {
	lib, err := Load(filepath.Join(t.TempDir(), "nope.kicad_sym"))
	if err != nil {
		t.Fatal(err)
	}
	if len(lib.Names()) != 0 {
		t.Errorf("expected an empty library, got %v", lib.Names())
	}
}

// A derived symbol carries no pins of its own. Copying one without flattening
// is the failure mode the whole importer exists to avoid, and it is the
// majority case in KiCad's own libraries.
func TestFlatten_DerivedSymbolGainsPins(t *testing.T) {
	lib, err := Load(installedLib(t, "Timer"))
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := lib.Get("NE555P")
	if !ok {
		t.Skip("Timer:NE555P not in this KiCad version")
	}
	if len(PinNumbers(raw)) != 0 {
		t.Skip("NE555P is not a derived symbol in this KiCad version")
	}

	flat, err := lib.Flatten("NE555P")
	if err != nil {
		t.Fatal(err)
	}
	pins := PinNumbers(flat)
	if len(pins) != 8 {
		t.Errorf("flattened NE555P should have 8 pins, got %d (%v)", len(pins), pins)
	}
	if sexp.FindList(flat, "extends") != nil {
		t.Error("a flattened symbol must not keep its extends reference")
	}
}

func TestRename_RenamesSubUnitsAndValue(t *testing.T) {
	lib, err := Load(installedLib(t, "Device"))
	if err != nil {
		t.Fatal(err)
	}
	sym, err := lib.Flatten("R")
	if err != nil {
		t.Fatal(err)
	}
	before := len(PinNumbers(sym))

	Rename(sym, "RC0603FR-071KL")

	if got := sexp.StringValue(sym, 1); got != "RC0603FR-071KL" {
		t.Errorf("symbol name = %q", got)
	}
	if got := Property(sym, "Value"); got != "RC0603FR-071KL" {
		t.Errorf("Value property = %q, want the new name", got)
	}
	if got := len(PinNumbers(sym)); got != before {
		t.Errorf("renaming lost pins: %d → %d", before, got)
	}
	for _, c := range sym.Children[2:] {
		if c.Head() != "symbol" {
			continue
		}
		if !strings.HasPrefix(sexp.StringValue(c, 1), "RC0603FR-071KL_") {
			t.Errorf("sub-unit %q was not renamed with its parent", sexp.StringValue(c, 1))
		}
	}
}

func TestPutReplacesAndRemove(t *testing.T) {
	lib := New()
	mk := func(name, value string) *sexp.Node {
		s := sexp.List(sexp.Atom("symbol"), sexp.Str(name))
		SetProperty(s, "Value", value)
		return s
	}
	lib.Put(mk("A", "one"))
	lib.Put(mk("A", "two"))
	if names := lib.Names(); len(names) != 1 || names[0] != "A" {
		t.Fatalf("Put must replace by name, got %v", names)
	}
	sym, _ := lib.Get("A")
	if got := Property(sym, "Value"); got != "two" {
		t.Errorf("replacement did not take: Value = %q", got)
	}
	if !lib.Remove("A") || len(lib.Names()) != 0 {
		t.Error("Remove failed")
	}
	if lib.Remove("A") {
		t.Error("removing a missing symbol must report false")
	}
}

// The real post-condition of this package: KiCad has to be able to read back
// what we write. sexp.Write's indentation is our heuristic, not KiCad's, so
// only kicad-cli can settle whether the output is a valid library.
func TestRoundTripThroughKicadCLI(t *testing.T) {
	cli := config.DetectKicadCLI()
	if cli == "" {
		t.Skip("kicad-cli not installed")
	}
	src, err := Load(installedLib(t, "Device"))
	if err != nil {
		t.Fatal(err)
	}
	flat, err := src.Flatten("R")
	if err != nil {
		t.Fatal(err)
	}
	Rename(flat, "TEST_PART")
	SetProperty(flat, "MCP_Source", "test https://example.invalid MIT 2026-08-09")

	out := New()
	out.Put(flat)
	path := filepath.Join(t.TempDir(), "RoundTrip.kicad_sym")
	if err := out.Save(path); err != nil {
		t.Fatal(err)
	}

	upgraded := filepath.Join(t.TempDir(), "Upgraded.kicad_sym")
	cmd := exec.Command(cli, "sym", "upgrade", "--output", upgraded, "--force", path)
	if outBytes, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("kicad-cli rejected the library we wrote: %v\n%s", err, outBytes)
	}

	back, err := Load(upgraded)
	if err != nil {
		t.Fatal(err)
	}
	sym, ok := back.Get("TEST_PART")
	if !ok {
		t.Fatalf("KiCad dropped the symbol; it kept %v", back.Names())
	}
	if got := len(PinNumbers(sym)); got != 2 {
		t.Errorf("KiCad read back %d pins, want 2", got)
	}
	if got := Property(sym, "MCP_Source"); !strings.Contains(got, "example.invalid") {
		t.Errorf("KiCad dropped the MCP_Source property, got %q", got)
	}
}
