package kicadcli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// BoardReport is a DRC run with schematic parity, its three lists kept apart:
// they mean different things. An unconnected item on an unrouted board is
// work left to do; a parity issue means the board does not implement the
// schematic; a violation is a rule the board breaks.
type BoardReport struct {
	Violations  []Violation
	Unconnected []Violation
	Parity      []Violation
}

// BoardDRC runs `kicad-cli pcb drc --schematic-parity --severity-all` and
// reads the JSON report from the -o file (kicad-cli never prints it).
func (r *Runner) BoardDRC(pcbPath string) (*BoardReport, error) {
	tmp, err := os.CreateTemp("", "kicad-drc-*.json")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	_, stderr, runErr := r.run("pcb", "drc", "--format", "json", "--schematic-parity",
		"--severity-all", "-o", tmpPath, pcbPath)
	data, readErr := os.ReadFile(tmpPath)
	if readErr != nil || len(data) == 0 {
		return nil, fmt.Errorf("kicad-cli pcb drc wrote no report: %v %s", runErr, strings.TrimSpace(stderr))
	}
	var doc struct {
		Violations  []violationJSON `json:"violations"`
		Unconnected []violationJSON `json:"unconnected_items"`
		Parity      []violationJSON `json:"schematic_parity"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("kicad-cli pcb drc report: %w", err)
	}
	conv := func(in []violationJSON) []Violation {
		out := make([]Violation, len(in))
		for i, v := range in {
			out[i] = Violation{Type: v.Type, Description: v.Description, Severity: v.Severity}
		}
		return out
	}
	return &BoardReport{conv(doc.Violations), conv(doc.Unconnected), conv(doc.Parity)}, nil
}

// Run exposes a raw kicad-cli invocation for subcommands without a typed
// wrapper (netlist export, board upgrade). Returns combined error text.
func (r *Runner) Run(args ...string) error {
	_, stderr, err := r.run(args...)
	if err != nil {
		return fmt.Errorf("kicad-cli %s: %w %s", strings.Join(args[:min(2, len(args))], " "), err, strings.TrimSpace(stderr))
	}
	return nil
}
