// Package testutil provides golden-file regression utilities for the MCP-KiCad
// pipeline. The schematic normalizer strips volatile fields (UUIDs, generator
// versions, timestamps) so that two functionally identical layouts compare
// equal regardless of when they were produced.
package testutil

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"mcp-kicad/internal/place2/metrics"
	"mcp-kicad/internal/sexp"
)

// MetricsTolerance controls how close two metrics snapshots must be to count
// as a golden match. Set per-field; values mean absolute (counts) or relative
// (lengths/areas) tolerance depending on the field.
type MetricsTolerance struct {
	BendsAbs        int     // |Δ bends| ≤
	CrossingsAbs    int     // exact match required by default (0)
	JunctionsAbs    int
	WireCountAbs    int
	WireLenRelative float64 // 0.02 = ±2%
	BboxRelative    float64
	WireThruExact   bool // 0 always
}

// DefaultTolerance — generous on bend / wire counts (the optimizer can shuffle
// them), strict on crossings and wires-through-symbol (those are bugs).
func DefaultTolerance() MetricsTolerance {
	return MetricsTolerance{
		BendsAbs:        4,
		CrossingsAbs:    0,
		JunctionsAbs:    4,
		WireCountAbs:    6,
		WireLenRelative: 0.05,
		BboxRelative:    0.10,
		WireThruExact:   true,
	}
}

var (
	uuidLine      = regexp.MustCompile(`(?m)^\s*\(uuid\s+"[0-9a-fA-F-]+"\s*\)\s*$`)
	pathUUID      = regexp.MustCompile(`/[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	rawUUID       = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	generatorLine = regexp.MustCompile(`(?m)^\s*\(generator(_version)?\s+"[^"]*"\s*\)\s*$`)
	dateLine      = regexp.MustCompile(`(?m)^\s*\(date\s+"[^"]*"\s*\)\s*$`)
)

// NormalizeSchematic returns a textual form of the schematic suitable for
// diffing across runs. It strips UUIDs, generator version strings and dates,
// then re-emits the AST (canonical writer ordering).
func NormalizeSchematic(raw string) string {
	s := raw
	s = uuidLine.ReplaceAllString(s, "(uuid \"00000000-0000-0000-0000-000000000000\")")
	s = pathUUID.ReplaceAllString(s, "/00000000-0000-0000-0000-000000000000")
	s = rawUUID.ReplaceAllString(s, "00000000-0000-0000-0000-000000000000")
	s = generatorLine.ReplaceAllString(s, "(generator \"mcp-kicad\")")
	s = dateLine.ReplaceAllString(s, "(date \"\")")
	return s
}

// NormalizeFile reads a .kicad_sch from disk and returns the normalized form.
func NormalizeFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return NormalizeSchematic(string(b)), nil
}

// CompareSchematic checks `actual` against the golden file at `goldenPath`.
// Both are normalized first. Returns a non-empty diff string when they differ
// (truncated to the first 30 differing lines for readability).
func CompareSchematic(actualRaw string, goldenPath string) (string, error) {
	want, err := NormalizeFile(goldenPath)
	if err != nil {
		return "", fmt.Errorf("read golden: %w", err)
	}
	got := NormalizeSchematic(actualRaw)
	if want == got {
		return "", nil
	}
	return diffLines(want, got, 30), nil
}

func diffLines(a, b string, max int) string {
	la := strings.Split(a, "\n")
	lb := strings.Split(b, "\n")
	var sb strings.Builder
	n := len(la)
	if len(lb) > n {
		n = len(lb)
	}
	diffs := 0
	for i := 0; i < n && diffs < max; i++ {
		var x, y string
		if i < len(la) {
			x = la[i]
		}
		if i < len(lb) {
			y = lb[i]
		}
		if x != y {
			fmt.Fprintf(&sb, "L%d\n  - %s\n  + %s\n", i+1, x, y)
			diffs++
		}
	}
	if diffs == 0 {
		return ""
	}
	return sb.String()
}

// GoldenMetrics is the persisted form of a metrics snapshot.
type GoldenMetrics struct {
	SymbolCount    int     `json:"symbol_count"`
	NetCount       int     `json:"net_count"`
	WireCount      int     `json:"wire_count"`
	BendCount      int     `json:"bend_count"`
	CrossingCount  int     `json:"crossing_count"`
	JunctionCount  int     `json:"junction_count"`
	WireThruSymbol int     `json:"wire_thru_symbol"`
	TotalWireLen   float64 `json:"total_wire_len"`
	BboxArea       float64 `json:"bbox_area"`
}

// FromMetrics converts a metrics.Metrics struct to its golden form.
func FromMetrics(m metrics.Metrics) GoldenMetrics {
	return GoldenMetrics{
		SymbolCount:    m.SymbolCount,
		NetCount:       m.NetCount,
		WireCount:      m.WireCount,
		BendCount:      m.BendCount,
		CrossingCount:  m.CrossingCount,
		JunctionCount:  m.JunctionCount,
		WireThruSymbol: m.WireThruSymbol,
		TotalWireLen:   m.TotalWireLen,
		BboxArea:       m.BboxArea,
	}
}

// Compare returns a non-empty list of human-readable violations when `got`
// drifts beyond `tol` from `want`. Empty slice → match.
func (want GoldenMetrics) Compare(got GoldenMetrics, tol MetricsTolerance) []string {
	var v []string
	if abs(want.BendCount-got.BendCount) > tol.BendsAbs {
		v = append(v, fmt.Sprintf("bends: want=%d got=%d Δ=%d > %d",
			want.BendCount, got.BendCount, abs(want.BendCount-got.BendCount), tol.BendsAbs))
	}
	if abs(want.CrossingCount-got.CrossingCount) > tol.CrossingsAbs {
		v = append(v, fmt.Sprintf("crossings: want=%d got=%d", want.CrossingCount, got.CrossingCount))
	}
	if abs(want.JunctionCount-got.JunctionCount) > tol.JunctionsAbs {
		v = append(v, fmt.Sprintf("junctions: want=%d got=%d", want.JunctionCount, got.JunctionCount))
	}
	if abs(want.WireCount-got.WireCount) > tol.WireCountAbs {
		v = append(v, fmt.Sprintf("wire_count: want=%d got=%d", want.WireCount, got.WireCount))
	}
	if tol.WireThruExact && got.WireThruSymbol != want.WireThruSymbol {
		v = append(v, fmt.Sprintf("wires_thru_symbol: want=%d got=%d", want.WireThruSymbol, got.WireThruSymbol))
	}
	if rel(want.TotalWireLen, got.TotalWireLen) > tol.WireLenRelative {
		v = append(v, fmt.Sprintf("total_wire_len: want=%.2f got=%.2f rel=%.3f > %.3f",
			want.TotalWireLen, got.TotalWireLen, rel(want.TotalWireLen, got.TotalWireLen), tol.WireLenRelative))
	}
	if rel(want.BboxArea, got.BboxArea) > tol.BboxRelative {
		v = append(v, fmt.Sprintf("bbox_area: want=%.2f got=%.2f rel=%.3f > %.3f",
			want.BboxArea, got.BboxArea, rel(want.BboxArea, got.BboxArea), tol.BboxRelative))
	}
	return v
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func rel(want, got float64) float64 {
	if want == 0 && got == 0 {
		return 0
	}
	denom := math.Max(math.Abs(want), 1e-9)
	return math.Abs(want-got) / denom
}

// LoadMetrics reads a golden metrics JSON file.
func LoadMetrics(path string) (GoldenMetrics, error) {
	var m GoldenMetrics
	b, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(b, &m)
	return m, err
}

// SaveMetrics writes a golden metrics JSON file (pretty-printed for diffs).
func SaveMetrics(path string, m GoldenMetrics) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// SaveSchematicGolden writes the normalized schematic to disk.
func SaveSchematicGolden(path string, raw string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(NormalizeSchematic(raw)), 0o644)
}

// ComputeFromFile parses a schematic and returns its golden metrics.
func ComputeFromFile(schPath string) (GoldenMetrics, error) {
	b, err := os.ReadFile(schPath)
	if err != nil {
		return GoldenMetrics{}, err
	}
	sch, err := sexp.ParseSchematic(string(b))
	if err != nil {
		return GoldenMetrics{}, err
	}
	return FromMetrics(metrics.Compute(sch)), nil
}

// PowerSymbolGroups counts duplicate #PWR symbols at the same coordinate
// (snapped to 1.27 mm grid) per power lib_id. Returns map libID → count of
// surplus symbols (count - distinct positions).
func PowerSymbolGroups(sch *sexp.Schematic) map[string]int {
	type key struct {
		lib string
		x   int
		y   int
	}
	counts := make(map[key]int)
	totals := make(map[string]int)
	for _, s := range sexp.ReadSymbols(sch) {
		if !strings.HasPrefix(s.LibID, "power:") {
			continue
		}
		k := key{s.LibID, int(s.X * 100), int(s.Y * 100)}
		counts[k]++
		totals[s.LibID]++
	}
	dup := make(map[string]int)
	for k, c := range counts {
		if c > 1 {
			dup[k.lib] += c - 1
		}
	}
	return dup
}

// SortedKeys returns map keys in deterministic order — used to make golden
// metrics output stable across runs.
func SortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
