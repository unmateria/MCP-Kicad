package tools

import (
	"bytes"
	"image/png"
	"mcp-kicad/internal/config"
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
		cli = config.DetectKicadCLI()
		if cli == "" {
			t.Skip("kicad-cli not available; skipping render test")
		}
	}

	schPath := filepath.Join("testdata", "render_fixture.kicad_sch")

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
