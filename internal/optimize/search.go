package optimize

import (
	"math"
	"sort"
)

// Candidate is one variation of layout we want to try: per-component (x, y, rotation).
// Indexed by reference designator. Annotations carry arbitrary string keys
// (e.g., "elk.seed", "cluster.pull") that the Materialize callback consumes
// to drive non-positional decisions like alternative layout seeds.
type Candidate struct {
	Positions   map[string][2]float64
	Rotations   map[string]float64
	Annotations map[string]string
}

// Clone returns a deep copy of the candidate so search variants don't mutate
// each other's maps.
func (c Candidate) Clone() Candidate {
	out := Candidate{
		Positions:   make(map[string][2]float64, len(c.Positions)),
		Rotations:   make(map[string]float64, len(c.Rotations)),
		Annotations: make(map[string]string, len(c.Annotations)),
	}
	for k, v := range c.Positions {
		out.Positions[k] = v
	}
	for k, v := range c.Rotations {
		out.Rotations[k] = v
	}
	for k, v := range c.Annotations {
		out.Annotations[k] = v
	}
	return out
}

// Materialize is a callback that turns a Candidate into a routed Layout.
// The caller (handleRelayout) supplies this because routing depends on the
// schematic AST and router, which optimize/ doesn't know about.
type Materialize func(c Candidate) Layout

// Variator generates a new Candidate by mutating the input. Returns false
// when no further variations exist (caller stops). For exhaustive search,
// implementations enumerate; for annealing, they sample.
type Variator interface {
	Next(base Candidate) (Candidate, bool)
	Reset()
}

// Search evaluates up to budget candidates produced by `v`, materializes
// each via `m`, scores it, and returns the lowest-cost layout along with
// its breakdown. `base` is the starting candidate (e.g., PlaceFlow output).
//
// Stops early if budget is exhausted OR if `v.Next` returns false.
func Search(base Candidate, v Variator, m Materialize, budget int) (best Layout, score CostBreakdown, tried int) {
	results, total := SearchTopK(base, v, m, budget, 1)
	if len(results) == 0 {
		return Layout{}, CostBreakdown{}, total
	}
	return results[0].Layout, results[0].Cost, total
}

// Result bundles a Candidate with its materialized layout and cost.
type Result struct {
	Candidate Candidate
	Layout    Layout
	Cost      CostBreakdown
}

// SearchPareto evaluates up to `budget` candidates and returns the Pareto
// frontier on (Crossings, BodyHits, WireLength). A candidate is on the
// frontier when no other candidate is strictly better on every axis.
//
// The caller picks the final winner lexicographically (e.g. minimal
// crossings → minimal label hits → minimal wire length). Use this when the
// scalar Cost.Total mixes axes that should not be traded against each
// other (a 2 mm wire-length saving is never worth a new crossing).
func SearchPareto(base Candidate, v Variator, m Materialize, budget int) ([]Result, int) {
	if budget < 1 {
		budget = 1
	}
	var all []Result
	tried := 0
	push := func(c Candidate, layout Layout, cost CostBreakdown) {
		all = append(all, Result{Candidate: c, Layout: layout, Cost: cost})
	}
	baseLayout := m(base)
	push(base, baseLayout, Cost(baseLayout))
	tried = 1
	v.Reset()
	for tried < budget {
		cand, ok := v.Next(base)
		if !ok {
			break
		}
		tried++
		layout := m(cand)
		push(cand, layout, Cost(layout))
	}
	// Filter to non-dominated.
	front := make([]Result, 0, len(all))
	for i, r := range all {
		dominated := false
		for j, q := range all {
			if i == j {
				continue
			}
			if dominates(q.Cost, r.Cost) {
				dominated = true
				break
			}
		}
		if !dominated {
			front = append(front, r)
		}
	}
	sort.SliceStable(front, func(i, j int) bool {
		if front[i].Cost.WireCrossings != front[j].Cost.WireCrossings {
			return front[i].Cost.WireCrossings < front[j].Cost.WireCrossings
		}
		if front[i].Cost.WireBodyHits != front[j].Cost.WireBodyHits {
			return front[i].Cost.WireBodyHits < front[j].Cost.WireBodyHits
		}
		return front[i].Cost.WireLength < front[j].Cost.WireLength
	})
	return front, tried
}

// dominates reports whether `a` is at least as good as `b` on every axis and
// strictly better on at least one.
func dominates(a, b CostBreakdown) bool {
	better := false
	if a.WireCrossings > b.WireCrossings {
		return false
	}
	if a.WireBodyHits > b.WireBodyHits {
		return false
	}
	if a.BodyOverlaps > b.BodyOverlaps {
		return false
	}
	if a.WireLength > b.WireLength*1.001 { // 0.1% tolerance for float noise
		return false
	}
	if a.WireCrossings < b.WireCrossings || a.WireBodyHits < b.WireBodyHits ||
		a.BodyOverlaps < b.BodyOverlaps || a.WireLength < b.WireLength*0.999 {
		better = true
	}
	return better
}

// SearchTopK returns the top-k lowest-cost results out of up to `budget`
// candidates evaluated. The slice is sorted ascending by Cost.Total so
// results[0] is the best.
//
// Stops early if budget is exhausted OR if `v.Next` returns false.
// Returns (results, totalTried).
func SearchTopK(base Candidate, v Variator, m Materialize, budget, k int) ([]Result, int) {
	if k < 1 {
		k = 1
	}
	if budget < 1 {
		budget = 1
	}
	var results []Result
	tried := 0
	push := func(c Candidate, layout Layout, cost CostBreakdown) {
		results = append(results, Result{Candidate: c, Layout: layout, Cost: cost})
		sort.SliceStable(results, func(i, j int) bool {
			return results[i].Cost.Total < results[j].Cost.Total
		})
		if len(results) > k {
			results = results[:k]
		}
	}
	baseLayout := m(base)
	push(base, baseLayout, Cost(baseLayout))
	tried = 1

	v.Reset()
	for tried < budget {
		cand, ok := v.Next(base)
		if !ok {
			break
		}
		tried++
		layout := m(cand)
		push(cand, layout, Cost(layout))
	}
	return results, tried
}

// RotationVariator enumerates rotation combinations for the listed refs.
// Each ref independently takes one of the supplied options (0/90/180/270 by
// default for symmetric components). For 6 refs × 2 rotation options each,
// that's 64 combinations — tractable for small circuits.
type RotationVariator struct {
	Refs    []string
	Options [][]float64 // per-ref rotation options
	idx     []int       // current index into Options[i]
	done    bool
}

// NewRotationVariator builds a variator over the cartesian product of the
// given rotation options per reference. Each call to Next yields the next
// combination; Reset starts over.
func NewRotationVariator(refs []string, options map[string][]float64) *RotationVariator {
	opts := make([][]float64, len(refs))
	for i, r := range refs {
		o, ok := options[r]
		if !ok || len(o) == 0 {
			opts[i] = []float64{0}
		} else {
			opts[i] = o
		}
	}
	return &RotationVariator{
		Refs:    refs,
		Options: opts,
		idx:     make([]int, len(refs)),
	}
}

// Reset resets the enumeration state.
func (rv *RotationVariator) Reset() {
	for i := range rv.idx {
		rv.idx[i] = 0
	}
	rv.done = false
	// Skip the initial all-zeros combination (that's the base).
	rv.advance()
}

// Next returns the next rotation combination layered on top of `base`.
func (rv *RotationVariator) Next(base Candidate) (Candidate, bool) {
	if rv.done {
		return Candidate{}, false
	}
	out := base.Clone()
	for i, ref := range rv.Refs {
		out.Rotations[ref] = rv.Options[i][rv.idx[i]]
	}
	rv.advance()
	return out, true
}

// advance increments the index vector lexicographically. Sets `done` when
// rolled over.
func (rv *RotationVariator) advance() {
	for i := len(rv.idx) - 1; i >= 0; i-- {
		rv.idx[i]++
		if rv.idx[i] < len(rv.Options[i]) {
			return
		}
		rv.idx[i] = 0
	}
	rv.done = true
}

// SwapVariator generates candidates by swapping pairs of components within
// the same column (same X). Useful for fine-tuning ordering after rotations
// are picked. With N components in a column, generates N×(N-1)/2 swaps.
type SwapVariator struct {
	Refs       []string
	Positions  map[string][2]float64 // captured at construction time
	pairs      [][2]int
	cursor     int
}

// NewSwapVariator collects all pairs of refs that share the same column X
// (within 0.5 mm) and prepares to enumerate swaps. Cross-column swaps are
// excluded — those would be a different topology, not a refinement.
func NewSwapVariator(refs []string, positions map[string][2]float64) *SwapVariator {
	sv := &SwapVariator{Refs: refs, Positions: positions}
	for i := 0; i < len(refs); i++ {
		pi, ok1 := positions[refs[i]]
		if !ok1 {
			continue
		}
		for j := i + 1; j < len(refs); j++ {
			pj, ok2 := positions[refs[j]]
			if !ok2 {
				continue
			}
			if math.Abs(pi[0]-pj[0]) < 0.5 {
				sv.pairs = append(sv.pairs, [2]int{i, j})
			}
		}
	}
	// Stable order so search is deterministic.
	sort.Slice(sv.pairs, func(a, b int) bool {
		if sv.pairs[a][0] != sv.pairs[b][0] {
			return sv.pairs[a][0] < sv.pairs[b][0]
		}
		return sv.pairs[a][1] < sv.pairs[b][1]
	})
	return sv
}

// Reset rewinds the enumeration.
func (sv *SwapVariator) Reset() {
	sv.cursor = 0
}

// Next returns the next swap candidate, or false when exhausted.
func (sv *SwapVariator) Next(base Candidate) (Candidate, bool) {
	if sv.cursor >= len(sv.pairs) {
		return Candidate{}, false
	}
	p := sv.pairs[sv.cursor]
	sv.cursor++
	out := base.Clone()
	a, b := sv.Refs[p[0]], sv.Refs[p[1]]
	out.Positions[a], out.Positions[b] = out.Positions[b], out.Positions[a]
	return out, true
}

// ChainVariator runs multiple variators in sequence — useful for "first try
// rotations, then try swaps on top of the best rotation."
type ChainVariator struct {
	Stages []Variator
	cur    int
}

func NewChainVariator(stages ...Variator) *ChainVariator {
	return &ChainVariator{Stages: stages}
}

func (cv *ChainVariator) Reset() {
	cv.cur = 0
	for _, s := range cv.Stages {
		s.Reset()
	}
}

func (cv *ChainVariator) Next(base Candidate) (Candidate, bool) {
	for cv.cur < len(cv.Stages) {
		c, ok := cv.Stages[cv.cur].Next(base)
		if ok {
			return c, true
		}
		cv.cur++
	}
	return Candidate{}, false
}
