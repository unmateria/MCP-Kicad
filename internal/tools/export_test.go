package tools

import (
	"bytes"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestRenderSchematicPNG verifies the helper exported for inline previews.
// Skips when kicad-cli is not on the test machine.
func TestRenderSchematicPNG(t *testing.T) {
	cli, err := exec.LookPath("kicad-cli")
	if err != nil {
		cli = `C:\Program Files\KiCad\9.0\bin\kicad-cli.exe`
		if _, err := os.Stat(cli); err != nil {
			t.Skip("kicad-cli not available; skipping render test")
		}
	}

	repoRoot, _ := filepath.Abs("../..")
	schPath := filepath.Join(repoRoot, "projects", "inv_amp_v2", "inv_amp.kicad_sch")
	if _, err := os.Stat(schPath); err != nil {
		t.Skipf("test schematic %s not present; skipping", schPath)
	}

	tmp, err := os.MkdirTemp("", "render-png-*")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	defer os.RemoveAll(tmp)

	pngBytes, err := RenderSchematicPNG(schPath, cli, tmp)
	if err != nil {
		t.Fatalf("RenderSchematicPNG: %v", err)
	}
	if len(pngBytes) < 1000 {
		t.Fatalf("PNG too small (%d bytes), expected >1000", len(pngBytes))
	}
	if _, err := png.Decode(bytes.NewReader(pngBytes)); err != nil {
		t.Fatalf("decode PNG: %v", err)
	}
}

// TestRenderSchematicPNG_NoKicadCLI verifies that an empty kicadCLI returns
// an error rather than panicking — withInlinePNG depends on this.
func TestRenderSchematicPNG_NoKicadCLI(t *testing.T) {
	if _, err := RenderSchematicPNG("any.kicad_sch", "", ""); err == nil {
		t.Fatal("expected error when kicad-cli is empty")
	}
}
