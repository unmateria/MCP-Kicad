package baseline

import (
	"os"
	"path/filepath"
	"testing"

	"mcp-kicad/internal/place2/metrics"
	"mcp-kicad/internal/sexp"
)

// TestExistingBaselines walks the projects/ directory and asserts that any
// schematic whose project name matches a Baselines entry stays within the
// recorded ceiling. The test SKIPS when the schematic is missing, so a fresh
// checkout passes — only after a demo is run does the assertion engage.
//
// This is the regression net for Phase A: it locks in the legacy metric
// envelope so subsequent phases can only improve it.
func TestExistingBaselines(t *testing.T) {
	root := projectsRoot(t)

	// Map project keys to the actual on-disk relative paths used by the
	// demos. Existing demos live in their _v2 subdir.
	candidates := map[string][]string{
		"led_18650": {"led_18650_v2/led_18650.kicad_sch"},
		"inv_amp":   {"inv_amp_v2/inv_amp.kicad_sch"},
		// Phase A demos write into projects/<project>/<project>.kicad_sch.
		"demo_mcu_i2c":           {"demo_mcu_i2c/demo_mcu_i2c.kicad_sch"},
		"demo_voltage_regulator": {"demo_voltage_regulator/demo_voltage_regulator.kicad_sch"},
		"demo_buck_converter":    {"demo_buck_converter/demo_buck_converter.kicad_sch"},
		"demo_full_board":        {"demo_full_board/demo_full_board.kicad_sch"},
	}

	for key, base := range Baselines {
		paths, ok := candidates[key]
		if !ok {
			continue
		}
		for _, rel := range paths {
			full := filepath.Join(root, rel)
			data, err := os.ReadFile(full)
			if err != nil {
				t.Logf("skip %s: %v", key, err)
				continue
			}
			sch, err := sexp.ParseSchematic(string(data))
			if err != nil {
				t.Errorf("%s: parse: %v", key, err)
				continue
			}
			m := metrics.Compute(sch)

			if m.SymbolCount < base.SymbolCountMin {
				t.Errorf("%s: SymbolCount=%d < min %d", key, m.SymbolCount, base.SymbolCountMin)
			}
			if m.NetCount < base.NetCountMin {
				t.Errorf("%s: NetCount=%d < min %d", key, m.NetCount, base.NetCountMin)
			}
			if m.BendCount > base.BendCount {
				t.Errorf("%s: BendCount=%d > baseline %d (regression)", key, m.BendCount, base.BendCount)
			}
			if m.CrossingCount > base.CrossingCount {
				t.Errorf("%s: CrossingCount=%d > baseline %d (regression)", key, m.CrossingCount, base.CrossingCount)
			}
			if m.WireThruSymbol > base.WireThruSymbol {
				t.Errorf("%s: WireThruSymbol=%d > baseline %d (regression)", key, m.WireThruSymbol, base.WireThruSymbol)
			}
			if m.TotalWireLen > base.TotalWireLenMaxMM {
				t.Errorf("%s: TotalWireLen=%.1f > baseline %.1f (regression)", key, m.TotalWireLen, base.TotalWireLenMaxMM)
			}
			t.Logf("%s OK: bends=%d crossings=%d thru=%d wirelen=%.1fmm", key, m.BendCount, m.CrossingCount, m.WireThruSymbol, m.TotalWireLen)
		}
	}
}

// projectsRoot finds the projects/ directory by walking up from the test's
// working directory. Returns "" if not found, in which case all sub-tests
// just log skip messages.
func projectsRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for cur := wd; cur != filepath.Dir(cur); cur = filepath.Dir(cur) {
		candidate := filepath.Join(cur, "projects")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	t.Skip("projects/ directory not found from " + wd)
	return ""
}
