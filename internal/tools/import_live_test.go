package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcp-kicad/internal/config"
	"mcp-kicad/internal/parts"
	"mcp-kicad/internal/parts/importer"
)

// TestLiveImportAndCompile is the end-to-end proof that the importer is worth
// anything: find a part that is NOT in KiCad, install it, and compile a
// schematic that uses it with the netlist verified.
//
// Off by default — the suite runs on a CI machine with no network and no
// KiCad. Run it with:
//
//	MCP_KICAD_LIVE=1 go test ./internal/tools/ -run TestLiveImportAndCompile -v
func TestLiveImportAndCompile(t *testing.T) {
	cases := []struct {
		name   string
		query  string
		source string
	}{
		// A part fetched as a ready-made KiCad file.
		{"repo", "ESP32-C3-MINI-1", "espressif"},
		// The same journey through the EasyEDA converter, where the symbol and
		// footprint are built from JSON rather than downloaded.
		{"converted", "C115450", "lcsc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { liveImportAndCompile(t, tc.query, tc.source) })
	}
}

func liveImportAndCompile(t *testing.T, query, source string) {
	if os.Getenv("MCP_KICAD_LIVE") == "" {
		t.Skip("set MCP_KICAD_LIVE=1 to run the network end-to-end test")
	}
	cli := config.DetectKicadCLI()
	if cli == "" {
		t.Skip("kicad-cli not installed")
	}
	cfg := config.Load("../../config.ini")

	libsRoot := t.TempDir()
	if reuse := os.Getenv("MCP_KICAD_LIVE_CACHE"); reuse != "" {
		libsRoot = reuse
	}
	outDir := t.TempDir()
	env := &Env{
		LibsRoot:        libsRoot,
		KicadCLI:        cli,
		KicadSymbols:    cfg.KicadSymbols,
		KicadFootprints: cfg.KicadFootprints,
		OutputDir:       outDir,
	}
	ctx := context.Background()

	// 2. find_part
	found, _, err := env.handleFindPart(ctx, nil, findPartInput{Query: query, Sources: []string{source}, Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	findText := resultText(t, found)
	t.Log("\n" + findText)
	ref := firstRefIn(findText)
	if ref == "" {
		t.Fatalf("find_part returned no importable ref:\n%s", findText)
	}

	// 3. import_part
	// RegisterGlobally is on in the real handler; here we go through the
	// importer directly so the test never writes into the user's KiCad config.
	im := &importer.Importer{
		LibsRoot:        env.LibsRoot,
		KicadCLI:        env.KicadCLI,
		KicadSymbols:    env.KicadSymbols,
		KicadFootprints: env.KicadFootprints,
		ProviderEnv:     env.providerEnv(),
	}
	result, err := im.Import(ctx, ref)
	if err != nil {
		t.Fatalf("import failed: %v\nchecks: %+v", err, result.Checks)
	}
	for _, c := range result.Checks {
		t.Logf("  [%v] %-28s %s", c.OK, c.Name, c.Detail)
	}
	t.Logf("imported %s: %d pins, footprint %q", result.LibID, result.PinCount, result.FootprintRef)
	if result.PinCount == 0 {
		t.Fatal("imported symbol has no pins")
	}

	// 4. The compiler must see it through the symbol search path, with no
	// further configuration. This is the whole point of Phase 0.
	pins, err := pinNamesOf(env, result.LibID)
	if err != nil {
		t.Fatalf("the compiler cannot resolve the imported symbol: %v", err)
	}
	if len(pins) < 2 {
		t.Fatalf("expected a multi-pin part, resolved %v", pins)
	}
	t.Logf("compiler resolves %d pins, first two: %v", len(pins), pins[:2])

	// 5. A minimal design that uses it, compiled with the netlist verified.
	design := map[string]any{
		"version":     1,
		"project":     "imported_part_e2e",
		"description": "smoke test for an imported part",
		"sheet":       "auto",
		"blocks": []any{map[string]any{
			"name": "main",
			"symbols": []any{
				map[string]any{"ref": "U1", "lib": result.LibID, "value": result.MPN},
				map[string]any{"ref": "R1", "lib": "Device:R", "value": "10k", "rot": 90,
					"place": map[string]any{"pin": "1", "at": "U1." + pins[0], "dir": "left", "cells": 6}},
			},
		}},
		"nets": map[string]any{
			"NET1": []string{"U1." + pins[0], "R1.1"},
		},
	}
	srcPath := filepath.Join(outDir, "imported_part_e2e.design.json")
	data, _ := json.MarshalIndent(design, "", "  ")
	if err := os.WriteFile(srcPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := env.CompileDesign(srcPath, filepath.Join(outDir, "imported_part_e2e.kicad_sch"))
	if res != nil {
		t.Log("\n" + res.Report)
	}
	if err != nil {
		t.Fatalf("compiling a design that uses the imported part failed: %v", err)
	}
	if !strings.Contains(res.Report, "verified") {
		t.Errorf("the compile report does not say the netlist was verified:\n%s", res.Report)
	}
	t.Logf("schematic: %s", res.SchematicPath)

	// 6. And it is listed as imported, with its provenance.
	list, err := importer.ImportedParts(env.LibsRoot)
	if err != nil {
		t.Fatal(err)
	}
	var seen bool
	for _, p := range list {
		if p.LibID == result.LibID {
			seen = true
			if !strings.Contains(p.Source, "provider="+source) {
				t.Errorf("provenance not recorded: %q", p.Source)
			}
		}
	}
	if !seen {
		t.Errorf("%s is not listed among the imported parts: %+v", result.LibID, list)
	}
	t.Logf("library: %s", parts.ImportedSymbolLib(env.LibsRoot))
}

// firstRefIn pulls the first "ref:" line out of a find_part report.
func firstRefIn(report string) string {
	for _, line := range strings.Split(report, "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, "ref:"); ok {
			return strings.TrimSpace(after)
		}
	}
	return ""
}

// pinNamesOf resolves a lib_id through the same geometry probe the compiler
// uses, and returns the pin NAMES a .design.json would refer to.
func pinNamesOf(env *Env, libID string) ([]string, error) {
	g, err := env.newLibGeom()
	if err != nil {
		return nil, err
	}
	sym, err := g.instance(libID, 1)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, p := range sym.Pins {
		if p.Name != "" && p.Name != "~" {
			names = append(names, p.Name)
			continue
		}
		names = append(names, p.Number)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no pins")
	}
	return names, nil
}
