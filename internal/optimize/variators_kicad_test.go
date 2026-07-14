package optimize

import (
	"testing"
)

// TestClusterPullToggleVariator emits 2^N candidates excluding the all-on
// base, so for N=2 it yields 3 variations.
func TestClusterPullToggleVariator(t *testing.T) {
	clusters := []ClusterRef{
		{Kind: "decoupling", Anchor: "U1"},
		{Kind: "pullup", Anchor: "U2"},
	}
	v := NewClusterPullToggleVariator(clusters)
	v.Reset()
	base := Candidate{Annotations: map[string]string{}}
	count := 0
	for {
		_, ok := v.Next(base)
		if !ok {
			break
		}
		count++
	}
	// 2^2 = 4 total combinations, minus the all-on base = 3 variations.
	if count != 3 {
		t.Errorf("expected 3 variations, got %d", count)
	}
}

func TestELKSeedVariator(t *testing.T) {
	v := NewELKSeedVariator(4)
	v.Reset()
	base := Candidate{Annotations: map[string]string{}}
	seeds := map[string]bool{}
	for {
		c, ok := v.Next(base)
		if !ok {
			break
		}
		seeds[c.Annotations["elk.seed"]] = true
	}
	if len(seeds) != 3 { // n=4 with cursor starting at 1 → 1,2,3
		t.Errorf("expected 3 distinct seeds, got %d", len(seeds))
	}
}

func TestSearchTopKReturnsSortedResults(t *testing.T) {
	base := Candidate{
		Positions:   map[string][2]float64{"R1": {0, 0}},
		Rotations:   map[string]float64{"R1": 0},
		Annotations: map[string]string{},
	}
	calls := 0
	materialize := func(c Candidate) Layout {
		calls++
		return Layout{} // empty layout → cost 0
	}
	v := NewRotationVariator([]string{"R1"}, map[string][]float64{"R1": {0, 90, 180, 270}})
	results, tried := SearchTopK(base, v, materialize, 8, 3)
	if tried < 1 {
		t.Errorf("expected tried ≥ 1, got %d", tried)
	}
	if len(results) > 3 {
		t.Errorf("expected ≤3 results, got %d", len(results))
	}
	for i := 1; i < len(results); i++ {
		if results[i].Cost.Total < results[i-1].Cost.Total {
			t.Errorf("results not sorted: [%d]=%f < [%d]=%f", i, results[i].Cost.Total, i-1, results[i-1].Cost.Total)
		}
	}
}

func TestMultiUnitAnchorSwapVariator(t *testing.T) {
	v := NewMultiUnitAnchorSwapVariator(map[string][]int{
		"U1": {1, 2},
		"U2": {1},
	})
	v.Reset()
	base := Candidate{Annotations: map[string]string{}}
	count := 0
	for {
		c, ok := v.Next(base)
		if !ok {
			break
		}
		// U1 has 2 units → 1 swap candidate (anchor=2). U2 has 1 unit → none.
		if c.Annotations["anchor.unit.U1"] == "" {
			t.Errorf("expected anchor.unit.U1 annotation, got %v", c.Annotations)
		}
		count++
	}
	if count != 1 {
		t.Errorf("expected 1 swap candidate, got %d", count)
	}
}
