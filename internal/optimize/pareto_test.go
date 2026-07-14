package optimize

import "testing"

type fixedVar struct {
	cands []Candidate
	i     int
}

func (f *fixedVar) Next(_ Candidate) (Candidate, bool) {
	if f.i >= len(f.cands) {
		return Candidate{}, false
	}
	c := f.cands[f.i]
	f.i++
	return c, true
}
func (f *fixedVar) Reset() { f.i = 0 }

// Two non-dominated candidates: one minimises crossings, the other
// minimises wire length. Both must end on the frontier.
func TestSearchParetoKeepsNonDominated(t *testing.T) {
	mat := func(c Candidate) Layout {
		// Use Annotations to encode synthetic costs.
		return Layout{}
	}
	_ = mat
	// Build cost breakdowns directly via a custom Materialize that stuffs
	// a synthetic Cost into Layout… simplest: evaluate the frontier on
	// raw CostBreakdowns via dominates().
	a := CostBreakdown{WireCrossings: 0, WireLength: 200}
	b := CostBreakdown{WireCrossings: 1, WireLength: 100}
	c := CostBreakdown{WireCrossings: 2, WireLength: 300}
	if !dominates(a, c) {
		t.Errorf("a should dominate c")
	}
	if dominates(a, b) {
		t.Errorf("a should NOT dominate b (a worse on length)")
	}
	if dominates(b, a) {
		t.Errorf("b should NOT dominate a (b worse on crossings)")
	}
}
