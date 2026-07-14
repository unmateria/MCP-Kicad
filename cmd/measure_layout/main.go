// measure_layout reads a .kicad_sch file and prints layout-quality metrics.
//
// Usage:
//
//	go run ./cmd/measure_layout <schematic.kicad_sch>
//
// Use this CLI to audit the output of demo_*, verify_e2e, or any LLM-generated
// schematic. The metric set is defined in internal/place2/metrics — keep that
// file in sync if new dimensions are added.
package main

import (
	"fmt"
	"os"

	"mcp-kicad/internal/place2/metrics"
	"mcp-kicad/internal/sexp"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: measure_layout <schematic.kicad_sch>")
		os.Exit(2)
	}
	path := os.Args[1]
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read:", err)
		os.Exit(1)
	}
	sch, err := sexp.ParseSchematic(string(data))
	if err != nil {
		fmt.Fprintln(os.Stderr, "parse:", err)
		os.Exit(1)
	}
	m := metrics.Compute(sch)
	fmt.Printf("=== %s ===\n%s", path, m.String())
}
