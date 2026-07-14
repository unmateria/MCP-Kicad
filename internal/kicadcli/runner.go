// Package kicadcli wraps kicad-cli.exe subprocess calls for ERC/DRC validation.
package kicadcli

import (
	"bytes"
	"fmt"
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
	stdout, stderr, err := r.run("sch", "erc", "--format", "json", schematicPath)
	res := &ERCResult{
		Violations: parseViolations(stdout),
		Stderr:     strings.TrimSpace(stderr),
	}
	if err != nil && res.Stderr != "" {
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
	stdout, stderr, err := r.run("pcb", "drc", "--format", "json", pcbPath)
	res := &DRCResult{
		Violations: parseViolations(stdout),
		Stderr:     strings.TrimSpace(stderr),
	}
	if err != nil && res.Stderr != "" {
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

// parseViolations extracts Violation structs from kicad-cli JSON output.
// Handles both pretty-printed (multi-line) and compact (single-line) objects.
func parseViolations(output string) []Violation {
	var violations []Violation
	lines := strings.Split(output, "\n")

	var cur Violation
	inViolation := false

	for _, raw := range lines {
		line := strings.TrimSpace(raw)

		// Single-line object: {"description": "...", ...}
		if strings.HasPrefix(line, "{") && strings.HasSuffix(strings.TrimRight(line, ","), "}") {
			var v Violation
			v.Type = jsonFieldOr(line, "type", "")
			v.Description = jsonFieldOr(line, "description", "")
			v.Severity = jsonFieldOr(line, "severity", "")
			if v.Description != "" || v.Type != "" {
				violations = append(violations, v)
			}
			continue
		}

		if line == "{" {
			cur = Violation{}
			inViolation = true
			continue
		}
		if inViolation && (line == "}" || line == "},") {
			if cur.Description != "" || cur.Type != "" {
				violations = append(violations, cur)
			}
			cur = Violation{}
			inViolation = false
			continue
		}
		if !inViolation {
			continue
		}
		cur.Type = jsonFieldOr(line, "type", cur.Type)
		cur.Description = jsonFieldOr(line, "description", cur.Description)
		cur.Severity = jsonFieldOr(line, "severity", cur.Severity)
	}
	return violations
}

// jsonFieldOr returns the string value of a JSON field "key": "value" in line,
// or fallback if the field is not present.
func jsonFieldOr(line, key, fallback string) string {
	prefix := `"` + key + `": "`
	idx := strings.Index(line, prefix)
	if idx < 0 {
		return fallback
	}
	rest := line[idx+len(prefix):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return fallback
	}
	return rest[:end]
}

func (r *Runner) run(args ...string) (stdout, stderr string, err error) {
	cmd := exec.Command(r.CLIPath, args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return outBuf.String(), errBuf.String(), err
}
