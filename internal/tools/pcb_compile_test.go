package tools

import (
	"path/filepath"
	"strings"
	"testing"

	"mcp-kicad/internal/config"
)

// The board post-condition, end to end on a real KiCad: the schematic
// compiles, the board builds, and KiCad's own DRC with --schematic-parity
// finds it implements the schematic with nothing overlapping.
func TestCompilePCB(t *testing.T) {
	cli := config.DetectKicadCLI()
	if cli == "" {
		t.Skip("KiCad not installed")
	}
	e := tidyEnv(t)
	e.KicadCLI = cli
	dir := t.TempDir()
	sch := filepath.Join(dir, "ne555_astable.kicad_sch")
	if _, err := e.CompileDesign(filepath.Join("..", "..", "docs", "compiler", "ne555_astable.design.json"), sch); err != nil {
		t.Fatal(err)
	}
	res, err := e.CompilePCB(sch, false, true)
	if err != nil {
		t.Fatalf("%v\n%v", err, res)
	}
	for _, want := range []string{"9 footprints", "parity: verified", "DRC: clean"} {
		if !strings.Contains(res.Report, want) {
			t.Errorf("report lacks %q:\n%s", want, res.Report)
		}
	}
	if _, err := e.CompilePCB(sch, false, true); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("an existing board was overwritten without overwrite: %v", err)
	}
}
