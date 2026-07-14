// update_goldens regenerates the golden snapshots used by verify_e2e and the
// CI golden test. It walks the demo schematics that already exist in
// projects/, normalizes them and writes:
//
//	testdata/golden/<demo>.kicad_sch     normalized schematic
//	testdata/golden/<demo>.metrics.json  metric snapshot
//
// Run this explicitly after a deliberate behaviour change. CI should NEVER
// call this — it would mask regressions.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mcp-kicad/internal/testutil"
)

var demos = map[string]string{
	"led_18650":              "projects/led_18650_v2/led_18650.kicad_sch",
	"ne5532_buf":             "projects/ne5532_buf/ne5532.kicad_sch",
	"demo_mcu_i2c":           "projects/demo_mcu_i2c/demo_mcu_i2c.kicad_sch",
	"demo_voltage_regulator": "projects/demo_voltage_regulator/demo_voltage_regulator.kicad_sch",
	"demo_buck_converter":    "projects/demo_buck_converter/demo_buck_converter.kicad_sch",
	"demo_full_board":        "projects/demo_full_board/demo_full_board.kicad_sch",
	"ne555_astable":          "projects/ne555_astable_v2/ne555.kicad_sch",
	"inv_amp":                "projects/inv_amp/inv_amp.kicad_sch",
}

func main() {
	dest := flag.String("out", "testdata/golden", "output directory")
	only := flag.String("only", "", "comma-separated demo names to update (empty = all)")
	flag.Parse()

	wanted := map[string]bool{}
	if *only != "" {
		for _, n := range strings.Split(*only, ",") {
			wanted[strings.TrimSpace(n)] = true
		}
	}

	updated, skipped, missing := 0, 0, 0
	for name, schPath := range demos {
		if len(wanted) > 0 && !wanted[name] {
			skipped++
			continue
		}
		if _, err := os.Stat(schPath); err != nil {
			fmt.Printf("[skip] %-25s %s (not found)\n", name, schPath)
			missing++
			continue
		}
		raw, err := os.ReadFile(schPath)
		if err != nil {
			fmt.Printf("[ERR ] %s: %v\n", name, err)
			continue
		}
		schDest := filepath.Join(*dest, name+".kicad_sch")
		if err := testutil.SaveSchematicGolden(schDest, string(raw)); err != nil {
			fmt.Printf("[ERR ] %s save sch: %v\n", name, err)
			continue
		}
		gm, err := testutil.ComputeFromFile(schPath)
		if err != nil {
			fmt.Printf("[ERR ] %s metrics: %v\n", name, err)
			continue
		}
		metricsDest := filepath.Join(*dest, name+".metrics.json")
		if err := testutil.SaveMetrics(metricsDest, gm); err != nil {
			fmt.Printf("[ERR ] %s save metrics: %v\n", name, err)
			continue
		}
		fmt.Printf("[OK ] %-25s symbols=%d wires=%d wirelen=%.1fmm crossings=%d\n",
			name, gm.SymbolCount, gm.WireCount, gm.TotalWireLen, gm.CrossingCount)
		updated++
	}
	fmt.Printf("\nupdated=%d skipped=%d missing=%d\n", updated, skipped, missing)
}
