// Package kicadcli wraps kicad-cli.exe subprocess calls for ERC/DRC validation.
package kicadcli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Runner invokes kicad-cli for design validation.
type Runner struct {
	CLIPath string
}

// New creates a Runner with the given kicad-cli path.
func New(cliPath string) *Runner {
	return &Runner{CLIPath: cliPath}
}

// ERCResult holds the structured output of an ERC run.
type ERCResult struct {
	Violations []Violation
	Stderr     string // raw stderr from kicad-cli (non-empty means structural error)
}

// ERC runs Electrical Rules Check on a .kicad_sch file.
func (r *Runner) ERC(schematicPath string) (*ERCResult, error) {
	violations, stderr, runErr := r.runReport("sch", "erc", schematicPath)
	res := &ERCResult{
		Violations: violations,
		Stderr:     strings.TrimSpace(stderr),
	}
	if runErr != nil && res.Stderr != "" {
		// Hard execution failure (not just violations).
		return res, fmt.Errorf("kicad-cli: %s", res.Stderr)
	}
	return res, nil
}

// DRCResult holds the structured output of a DRC run.
type DRCResult struct {
	Violations []Violation
	Stderr     string
}

// DRC runs Design Rules Check on a .kicad_pcb file.
func (r *Runner) DRC(pcbPath string) (*DRCResult, error) {
	violations, stderr, runErr := r.runReport("pcb", "drc", pcbPath)
	res := &DRCResult{
		Violations: violations,
		Stderr:     strings.TrimSpace(stderr),
	}
	if runErr != nil && res.Stderr != "" {
		return res, fmt.Errorf("kicad-cli: %s", res.Stderr)
	}
	return res, nil
}

// ParseERCOutput is kept for backward compatibility with validate.go.
func ParseERCOutput(output string) []string {
	vs := parseViolations(output)
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.Description
	}
	return out
}

// ParseDRCOutput is kept for backward compatibility with validate.go.
func ParseDRCOutput(output string) []string {
	vs := parseViolations(output)
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.Description
	}
	return out
}

// reportDoc mirrors the JSON report kicad-cli writes for "sch erc --format json"
// and "pcb drc --format json". ERC nests violations per-sheet under "sheets";
// DRC (and some report variants) may place "violations" at the top level.
// Both shapes are read so callers get every violation regardless of which
// command produced the report.
type reportDoc struct {
	Sheets []struct {
		Violations []violationJSON `json:"violations"`
	} `json:"sheets"`
	Violations []violationJSON `json:"violations"`
}

type violationJSON struct {
	Type        string `json:"type"`
	Description string `json:"description"`
	Severity    string `json:"severity"`
}

// parseViolations extracts Violation structs from a kicad-cli JSON report
// (the contents of the -o report file, not kicad-cli's stdout — kicad-cli
// only prints a one-line human summary to stdout and writes the full
// structured report exclusively to the -o file). Malformed input yields a
// nil slice rather than panicking.
func parseViolations(output string) []Violation {
	var doc reportDoc
	if err := json.Unmarshal([]byte(output), &doc); err != nil {
		return nil
	}

	var violations []Violation
	for _, v := range doc.Violations {
		violations = append(violations, Violation{Type: v.Type, Description: v.Description, Severity: v.Severity})
	}
	for _, sheet := range doc.Sheets {
		for _, v := range sheet.Violations {
			violations = append(violations, Violation{Type: v.Type, Description: v.Description, Severity: v.Severity})
		}
	}
	return violations
}

// runReport runs `kicad-cli <group> <cmd> --format json -o <tmpfile> <path>`
// and parses the JSON report kicad-cli writes to that file. kicad-cli never
// writes the structured report to stdout — only a summary line like
// "Found 13 violations" — so the report file is the only source of truth.
func (r *Runner) runReport(group, cmd, path string) (violations []Violation, stderr string, err error) {
	tmp, tmpErr := os.CreateTemp("", "kicad-"+cmd+"-*.json")
	if tmpErr != nil {
		return nil, "", tmpErr
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	_, stderrOut, runErr := r.run(group, cmd, "--format", "json", "-o", tmpPath, path)

	data, readErr := os.ReadFile(tmpPath)
	if readErr != nil || len(data) == 0 {
		// kicad-cli failed before writing a report (e.g. malformed schematic).
		return nil, stderrOut, runErr
	}

	return parseViolations(string(data)), stderrOut, runErr
}

func (r *Runner) run(args ...string) (stdout, stderr string, err error) {
	cmd := exec.Command(r.CLIPath, args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return outBuf.String(), errBuf.String(), err
}
