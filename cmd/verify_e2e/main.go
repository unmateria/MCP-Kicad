// verify_e2e is the end-to-end smoke test for the declarative compiler. It
// calls Env.CompileDesign directly (no MCP transport, no Claude Desktop) on the
// canonical .design.json sources and asserts the invariants that used to break
// silently: serialization must not re-quote strings, power symbols must not
// stack, and the gate/routing report must come back error-free.
//
//	go run ./cmd/verify_e2e
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mcp-kicad/internal/config"
	"mcp-kicad/internal/tools"
)

// projects are compiled in order; each name maps to docs/compiler/<name>.design.json.
var projects = []string{"led_18650", "ne5532_buf", "ne555_astable"}

func main() {
	cfg := config.Load("config.ini")
	env := &tools.Env{
		LibsRoot:        cfg.LibsRoot,
		KicadCLI:        cfg.KicadCLI,
		KicadSymbols:    cfg.KicadSymbols,
		KicadFootprints: cfg.KicadFootprints,
		OutputDir:       cfg.OutputDir,
		ConfigPath:      cfg.ConfigPath,
	}

	for _, project := range projects {
		fmt.Printf("\n#### %s ####\n", project)

		designPath := filepath.Join("docs", "compiler", project+".design.json")
		outDir, err := filepath.Abs(filepath.Join("projects", project+"_e2e"))
		if err != nil {
			fail(project, fmt.Sprintf("cannot resolve output dir: %v", err))
		}
		_ = os.RemoveAll(outDir)
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			fail(project, fmt.Sprintf("cannot create output dir: %v", err))
		}
		outPath := filepath.Join(outDir, project+".kicad_sch")

		res, err := env.CompileDesign(designPath, outPath)
		if err != nil {
			fail(project, fmt.Sprintf("compile error: %v", err))
		}
		fmt.Println(res.Report)

		data, err := os.ReadFile(res.SchematicPath)
		if err != nil {
			fail(project, fmt.Sprintf("cannot read generated schematic: %v", err))
		}

		if hasDoubleQuoteBug(string(data)) {
			fail(project, "generated schematic contains the pin-double-quote bug")
		}
		if dups := countDuplicatePower(string(data)); dups > 0 {
			fail(project, fmt.Sprintf("%d duplicate #PWR symbol(s) at the same coordinate", dups))
		}
		if err := checkReport(res.Report); err != nil {
			fail(project, err.Error())
		}

		fmt.Printf("OK %s: no double-quote bug, no duplicate #PWR, gate+routing clean\n", project)
		fmt.Println("   schematic:", res.SchematicPath)
	}

	fmt.Printf("\nPASS: %d design(s) compiled clean\n", len(projects))
}

func fail(project, msg string) {
	fmt.Fprintf(os.Stderr, "\nFAIL %s: %s\n", project, msg)
	os.Exit(1)
}

// checkReport asserts the compile report carries a gate summary and a routing
// summary reporting zero errors. Both lines are produced by CompileDesign; a
// missing line means the pipeline changed shape and this smoke test went blind.
func checkReport(report string) error {
	var gateLine, routingLine string
	for _, line := range strings.Split(report, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "gate:"):
			gateLine = t
		case strings.HasPrefix(t, "routing:"):
			routingLine = t
		}
	}
	if gateLine == "" {
		return fmt.Errorf("compile report has no \"gate:\" line")
	}
	if routingLine == "" {
		return fmt.Errorf("compile report has no \"routing:\" line")
	}
	if !strings.Contains(routingLine, "0 errors") {
		return fmt.Errorf("routing reported errors: %s", routingLine)
	}
	return nil
}

// countDuplicatePower scans the .kicad_sch text and reports how many `power:*`
// symbol instances share the same (lib_id, snapped position) bucket as another.
func countDuplicatePower(content string) int {
	type key struct{ lib, pos string }
	seen := map[key]int{}
	lines := strings.Split(content, "\n")
	var curLib string
	var curAt string
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "(symbol ") {
			curLib, curAt = "", ""
		}
		if strings.HasPrefix(t, "(lib_id \"power:") {
			curLib = t
		}
		if strings.HasPrefix(t, "(at ") && curLib != "" && curAt == "" {
			curAt = t
			seen[key{curLib, curAt}]++
		}
	}
	dup := 0
	for _, c := range seen {
		if c > 1 {
			dup += c - 1
		}
	}
	return dup
}

// hasDoubleQuoteBug returns true if the file contains the legacy bug pattern
// where pin numbers, path UUIDs, labels or uuid values were re-quoted on
// serialization, producing (pin "\"N\"" ...) or (path "/\"<uuid>\"" ...).
// Description fields legitimately contain \" (e.g. `... name \"GND\"`), so a
// blanket strings.Contains check produces false positives.
func hasDoubleQuoteBug(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, `(pin "\"`) ||
			strings.HasPrefix(t, `(path "/\"`) ||
			strings.HasPrefix(t, `(label "\"`) ||
			strings.HasPrefix(t, `(uuid "\"`) {
			return true
		}
	}
	return false
}
