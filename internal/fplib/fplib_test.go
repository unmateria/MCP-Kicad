package fplib

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mcp-kicad/internal/config"
)

func installedFootprint(t *testing.T, lib, name string) string {
	t.Helper()
	cli := config.DetectKicadCLI()
	if cli == "" {
		t.Skip("kicad-cli not installed")
	}
	root := filepath.Dir(filepath.Dir(cli))
	path := filepath.Join(root, "share", "kicad", "footprints", lib+".pretty", name+".kicad_mod")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("%s not found: %v", path, err)
	}
	return path
}

func TestLoadRealFootprint(t *testing.T) {
	fp, err := Load(installedFootprint(t, "Package_DIP", "DIP-8_W7.62mm"))
	if err != nil {
		t.Fatal(err)
	}
	if fp.Name() != "DIP-8_W7.62mm" {
		t.Errorf("Name() = %q", fp.Name())
	}
	pads := fp.PadNumbers()
	if len(pads) != 8 {
		t.Errorf("DIP-8 must have 8 numbered pads, got %d (%v)", len(pads), pads)
	}
	if fp.Descr() == "" {
		t.Error("expected a description")
	}
	if !strings.Contains(fp.Model(), "DIP-8_W7.62mm") {
		t.Errorf("expected a 3D model reference, got %q", fp.Model())
	}
}

func TestSetNameSyncsValue(t *testing.T) {
	fp, err := Load(installedFootprint(t, "Package_DIP", "DIP-8_W7.62mm"))
	if err != nil {
		t.Fatal(err)
	}
	fp.SetName("NE555P__DIP-8")
	if fp.Name() != "NE555P__DIP-8" {
		t.Errorf("Name() = %q", fp.Name())
	}
	if got := fp.Property("Value"); got != "NE555P__DIP-8" {
		t.Errorf("Value property = %q, want it synced to the new name", got)
	}
}

func TestSetModelReplacesAndRemoves(t *testing.T) {
	fp, err := Load(installedFootprint(t, "Package_DIP", "DIP-8_W7.62mm"))
	if err != nil {
		t.Fatal(err)
	}
	fp.SetModel("${KIPRJMOD}/../3dmodels/NE555P.step")
	if got := fp.Model(); got != "${KIPRJMOD}/../3dmodels/NE555P.step" {
		t.Errorf("Model() = %q", got)
	}
	fp.SetModel("")
	if got := fp.Model(); got != "" {
		t.Errorf("an empty path must drop the model node, got %q", got)
	}
	// And back again from nothing.
	fp.SetModel("/a/b.step")
	if got := fp.Model(); got != "/a/b.step" {
		t.Errorf("re-adding the model failed: %q", got)
	}
}

// Mechanical pads carry no number and have no symbol pin to match. Counting
// them would make every symbol-vs-footprint check disagree on parts that are
// perfectly fine.
func TestPadNumbersExcludesMechanical(t *testing.T) {
	src := `(footprint "X"
		(layer "F.Cu")
		(pad "1" smd rect (at 0 0) (size 1 1) (layers "F.Cu"))
		(pad "2" smd rect (at 2 0) (size 1 1) (layers "F.Cu"))
		(pad "2" smd rect (at 4 0) (size 1 1) (layers "F.Cu"))
		(pad "" np_thru_hole circle (at 6 0) (size 2 2) (drill 2) (layers "*.Cu"))
	)`
	fp, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	pads := fp.PadNumbers()
	if len(pads) != 2 || pads[0] != "1" || pads[1] != "2" {
		t.Errorf("expected distinct numbered pads [1 2], got %v", pads)
	}
	if fp.MechanicalPads() != 1 {
		t.Errorf("expected 1 mechanical pad, got %d", fp.MechanicalPads())
	}
}

// Same post-condition as symlib: KiCad must accept what we write.
func TestRoundTripThroughKicadCLI(t *testing.T) {
	cli := config.DetectKicadCLI()
	if cli == "" {
		t.Skip("kicad-cli not installed")
	}
	fp, err := Load(installedFootprint(t, "Package_DIP", "DIP-8_W7.62mm"))
	if err != nil {
		t.Fatal(err)
	}
	fp.SetName("NE555P__DIP-8_W7.62mm")
	fp.SetModel("${KIPRJMOD}/NE555P.step")
	fp.SetProperty("MCP_Source", "test https://example.invalid MIT 2026-08-09")

	pretty := filepath.Join(t.TempDir(), "MCP_Imported.pretty")
	path, err := fp.SaveInto(pretty)
	if err != nil {
		t.Fatal(err)
	}

	upgraded := filepath.Join(t.TempDir(), "Upgraded.pretty")
	cmd := exec.Command(cli, "fp", "upgrade", "--output", upgraded, "--force", pretty)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("kicad-cli rejected the footprint we wrote (%s): %v\n%s", path, err, out)
	}

	names, err := ListPretty(upgraded)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "NE555P__DIP-8_W7.62mm" {
		t.Fatalf("KiCad wrote back %v", names)
	}
	back, err := Load(filepath.Join(upgraded, names[0]+".kicad_mod"))
	if err != nil {
		t.Fatal(err)
	}
	if got := len(back.PadNumbers()); got != 8 {
		t.Errorf("KiCad read back %d pads, want 8", got)
	}
	if got := back.Property("MCP_Source"); !strings.Contains(got, "example.invalid") {
		t.Errorf("KiCad dropped the MCP_Source property, got %q", got)
	}
	if got := back.Model(); got != "${KIPRJMOD}/NE555P.step" {
		t.Errorf("KiCad read back model %q", got)
	}
}
