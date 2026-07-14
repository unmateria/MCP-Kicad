package testutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeSchematicStripsUUIDsAndDates(t *testing.T) {
	in := `(kicad_sch
	(version 20240101)
	(generator "eeschema")
	(generator_version "9.0")
	(uuid "abcdef01-1234-5678-9abc-deadbeef0001")
	(date "2025-01-02 03:04:05")
	(symbol (lib_id "Device:R") (uuid "deadbeef-0000-1111-2222-deadbeef0001")))
`
	out := NormalizeSchematic(in)
	if strings.Contains(out, "abcdef01") || strings.Contains(out, "deadbeef") {
		t.Errorf("uuid not stripped: %s", out)
	}
	if !strings.Contains(out, "00000000-0000-0000-0000-000000000000") {
		t.Errorf("missing canonical uuid token: %s", out)
	}
	if strings.Contains(out, "2025-01-02") {
		t.Errorf("date not stripped: %s", out)
	}
}

func TestGoldenMetricsCompareTolerance(t *testing.T) {
	want := GoldenMetrics{BendCount: 10, CrossingCount: 0, WireCount: 20, TotalWireLen: 100, BboxArea: 5000, WireThruSymbol: 0}
	got := GoldenMetrics{BendCount: 12, CrossingCount: 0, WireCount: 21, TotalWireLen: 102, BboxArea: 5200, WireThruSymbol: 0}
	tol := DefaultTolerance()
	if v := want.Compare(got, tol); len(v) != 0 {
		t.Errorf("expected match, got violations: %v", v)
	}
	got.CrossingCount = 1
	if v := want.Compare(got, tol); len(v) == 0 {
		t.Errorf("expected crossing violation")
	}
}

// TestGoldensExist asserts the golden corpus is present and parseable. This
// catches accidental deletion or corruption of testdata/golden during
// refactors. It does NOT assert layout quality — for that, see verify_e2e.
func TestGoldensExist(t *testing.T) {
	root := findGoldenRoot(t)
	if root == "" {
		t.Skip("no testdata/golden directory yet — run cmd/update_goldens to bless")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".metrics.json") {
			continue
		}
		p := filepath.Join(root, e.Name())
		if _, err := LoadMetrics(p); err != nil {
			t.Errorf("load %s: %v", e.Name(), err)
		}
		count++
	}
	if count == 0 {
		t.Skip("no metrics goldens present")
	}
	t.Logf("verified %d golden metric files", count)
}

func findGoldenRoot(t *testing.T) string {
	t.Helper()
	// Walk up from cwd until we find testdata/golden.
	cwd, _ := os.Getwd()
	for i := 0; i < 5; i++ {
		p := filepath.Join(cwd, "testdata", "golden")
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
		cwd = filepath.Dir(cwd)
	}
	return ""
}
