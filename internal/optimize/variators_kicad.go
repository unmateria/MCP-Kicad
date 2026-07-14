package optimize

import (
	"sort"
	"strconv"
)

// ClusterRef is the minimal cluster info needed by KiCad-specific variators.
// Defined here to avoid an import cycle with internal/place2.
type ClusterRef struct {
	Kind   string
	Anchor string
	Refs   []string
}

// NewClusterPullToggleVariator produces 2^N candidates where each cluster is
// either pulled to its anchor or left untouched. For >6 clusters the variator
// caps at the first 6 to keep the search tractable (64 combinations).
//
// The variator does not move symbols itself — it sets the annotation
// "cluster.pull.<anchor>" to "on" or "off" so the Materialize callback can
// decide whether to invoke ApplyClusterPull on each cluster individually.
func NewClusterPullToggleVariator(clusters []ClusterRef) Variator {
	const maxClusters = 6
	cs := clusters
	if len(cs) > maxClusters {
		cs = cs[:maxClusters]
	}
	// Stable order for reproducibility.
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].Anchor < cs[j].Anchor })
	return &clusterToggleVariator{clusters: cs}
}

type clusterToggleVariator struct {
	clusters []ClusterRef
	cursor   int
	done     bool
}

func (v *clusterToggleVariator) Reset() {
	v.cursor = 1 // skip 0 = base (all-on)
	v.done = false
}

func (v *clusterToggleVariator) Next(base Candidate) (Candidate, bool) {
	if v.done {
		return Candidate{}, false
	}
	max := 1 << len(v.clusters)
	if v.cursor >= max {
		v.done = true
		return Candidate{}, false
	}
	out := base.Clone()
	for i, c := range v.clusters {
		key := "cluster.pull." + c.Anchor
		if v.cursor&(1<<i) != 0 {
			out.Annotations[key] = "off"
		} else {
			out.Annotations[key] = "on"
		}
	}
	v.cursor++
	return out, true
}

// NewELKSeedVariator emits N candidates each with a different "elk.seed"
// annotation. The Materialize callback reads the seed and passes it as the
// org.eclipse.elk.randomSeed option when invoking elkjs. Different seeds
// yield genuinely different topologies in dense layouts.
func NewELKSeedVariator(nSeeds int) Variator {
	if nSeeds < 1 {
		nSeeds = 1
	}
	return &elkSeedVariator{n: nSeeds}
}

type elkSeedVariator struct {
	n      int
	cursor int
}

func (v *elkSeedVariator) Reset()        { v.cursor = 1 } // 0 = base; start from seed 1
func (v *elkSeedVariator) Next(base Candidate) (Candidate, bool) {
	if v.cursor >= v.n {
		return Candidate{}, false
	}
	out := base.Clone()
	out.Annotations["elk.seed"] = strconv.Itoa(v.cursor)
	v.cursor++
	return out, true
}

// NewOpampOffsetVariator produces candidates that toggle the opamp_feedback
// cluster topology between the canonical "Rf top, Rin left" and the mirrored
// "Rf bottom, Rin right" arrangement. Useful when the surrounding layout
// makes the canonical orientation cause crossings.
//
// For a circuit with M opamp_feedback clusters this yields up to 2^M
// candidates (capped at 4 = 16 candidates for tractability).
func NewOpampOffsetVariator(clusters []ClusterRef) Variator {
	const maxClusters = 4
	var opamps []ClusterRef
	for _, c := range clusters {
		if c.Kind == "opamp_feedback" {
			opamps = append(opamps, c)
			if len(opamps) >= maxClusters {
				break
			}
		}
	}
	sort.SliceStable(opamps, func(i, j int) bool { return opamps[i].Anchor < opamps[j].Anchor })
	return &opampOffsetVariator{opamps: opamps}
}

type opampOffsetVariator struct {
	opamps []ClusterRef
	cursor int
	done   bool
}

func (v *opampOffsetVariator) Reset() {
	v.cursor = 1 // skip 0 = canonical
	v.done = false
}

func (v *opampOffsetVariator) Next(base Candidate) (Candidate, bool) {
	if v.done || len(v.opamps) == 0 {
		v.done = true
		return Candidate{}, false
	}
	max := 1 << len(v.opamps)
	if v.cursor >= max {
		v.done = true
		return Candidate{}, false
	}
	out := base.Clone()
	for i, c := range v.opamps {
		key := "opamp.topology." + c.Anchor
		if v.cursor&(1<<i) != 0 {
			out.Annotations[key] = "mirrored"
		} else {
			out.Annotations[key] = "canonical"
		}
	}
	v.cursor++
	return out, true
}

// NewMultiUnitAnchorSwapVariator generates candidates where the anchor unit
// of multi-unit ICs is swapped. For an op-amp with 2 units, the variator
// emits one candidate using unit 1 as the anchor and one using unit 2 — this
// matters when the LLM placed unit 2 on a power pin and the canonical anchor
// (unit 1) ends up at an awkward angle.
//
// The swap is encoded as the annotation "anchor.unit.<ref>"="<unit>".
func NewMultiUnitAnchorSwapVariator(unitsByRef map[string][]int) Variator {
	type candidate struct {
		ref  string
		unit int
	}
	var slots []candidate
	for ref, units := range unitsByRef {
		if len(units) <= 1 {
			continue
		}
		for _, u := range units[1:] { // skip unit[0] which is the canonical anchor
			slots = append(slots, candidate{ref, u})
		}
	}
	sort.SliceStable(slots, func(i, j int) bool {
		if slots[i].ref != slots[j].ref {
			return slots[i].ref < slots[j].ref
		}
		return slots[i].unit < slots[j].unit
	})
	out := make([]ClusterRef, len(slots))
	for i, s := range slots {
		out[i] = ClusterRef{Kind: "multiunit", Anchor: s.ref, Refs: []string{strconv.Itoa(s.unit)}}
	}
	return &multiUnitAnchorVariator{slots: out}
}

type multiUnitAnchorVariator struct {
	slots  []ClusterRef
	cursor int
}

func (v *multiUnitAnchorVariator) Reset() { v.cursor = 0 }
func (v *multiUnitAnchorVariator) Next(base Candidate) (Candidate, bool) {
	if v.cursor >= len(v.slots) {
		return Candidate{}, false
	}
	slot := v.slots[v.cursor]
	v.cursor++
	out := base.Clone()
	out.Annotations["anchor.unit."+slot.Anchor] = slot.Refs[0]
	return out, true
}
