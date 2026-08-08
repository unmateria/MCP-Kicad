package tools

import (
	"fmt"
	"sort"
	"strings"

	"mcp-kicad/internal/compile"
	"mcp-kicad/internal/sexp"
)

// Defect kinds reported by VerifyNetlist.
const (
	// DefectSplit: the pins of one declared net did not all end up on the
	// same electrical net. The schematic implements LESS connectivity than
	// the source asked for.
	DefectSplit = "SPLIT"

	// DefectMerged: two declared nets ended up on one electrical net — a
	// short. Geometry alone cannot catch this: once a wire lands on a foreign
	// pin the two nets ARE one net, so the gate sees a single consistent net
	// and ERC stays quiet between passive pins.
	DefectMerged = "MERGED"
)

// NetDefect is one discrepancy between the netlist the source declares and
// the netlist the emitted schematic actually implements.
type NetDefect struct {
	Kind   string
	Net    string
	Detail string
}

func (d NetDefect) String() string {
	return fmt.Sprintf("[%s] %s: %s", d.Kind, d.Net, d.Detail)
}

// VerifyNetlist checks the compiler's central post-condition: the emitted
// schematic implements exactly the netlist the source declares.
//
// Measured against KiCad 10 (kicad-cli sch export netlist), electrical
// connection requires a wire ENDPOINT or an explicit junction — a wire
// merely crossing a pin tip mid-segment connects nothing, and sexp.TraceNets
// agrees with that on all three lab cases. So the netlist we trace here is
// the netlist KiCad will read.
//
// Two directions are checked, because each catches what the other cannot:
//
//   - completeness — every pin of a declared net shares one traced net;
//   - exclusivity  — no two declared nets share a traced net.
//
// Pins the compiler adds on its own (power symbols, PWR_FLAG, template
// internals) are deliberately NOT flagged as intruders: only pins the source
// itself declared elsewhere count, which is exactly the short worth failing
// on and leaves no room for false positives from stamped substructures.
func VerifyNetlist(sch *sexp.Schematic, d *compile.Design) []NetDefect {
	traced := sexp.TraceNets(sch)

	// locate returns the index of the traced net carrying a declared pin, or
	// -1 when no net claims it (electrically isolated).
	locate := func(decl string) int {
		for i, net := range traced {
			for _, p := range net.Pins {
				if p.Matches(decl) {
					return i
				}
			}
		}
		return -1
	}

	names := make([]string, 0, len(d.Nets))
	for name := range d.Nets {
		names = append(names, name)
	}
	sort.Strings(names)

	var defects []NetDefect
	owner := make(map[int]string) // traced net index -> first declared net seen on it

	for _, name := range names {
		pins := d.Nets[name]
		if len(pins) == 0 {
			continue
		}

		where := make([]int, len(pins))
		for i, decl := range pins {
			where[i] = locate(decl)
		}

		home := majorityNet(where)
		var strays []string
		for i, idx := range where {
			if idx == home {
				continue
			}
			switch {
			case idx < 0:
				strays = append(strays, pins[i]+" (isolated)")
			default:
				strays = append(strays, fmt.Sprintf("%s (on %s)", pins[i], traced[idx].Name))
			}
		}
		if len(strays) > 0 {
			detail := fmt.Sprintf("%d declared pins did not join it: %s", len(strays), strings.Join(strays, ", "))
			if home < 0 {
				detail = "no pin of this net is electrically connected"
			}
			defects = append(defects, NetDefect{Kind: DefectSplit, Net: name, Detail: detail})
		}

		// Exclusivity is checked against EVERY traced net this declared net
		// touches, not just the majority one: when a net is split in half the
		// shorted half is often the minority, and claiming only the majority
		// would let exactly the short we are hunting slip through.
		claimed := make(map[int]bool, len(where))
		for _, idx := range where {
			if idx < 0 || claimed[idx] {
				continue
			}
			claimed[idx] = true
			if prev, taken := owner[idx]; taken {
				defects = append(defects, NetDefect{Kind: DefectMerged, Net: name,
					Detail: fmt.Sprintf("shorted to %s — both sit on traced net %q", prev, traced[idx].Name)})
				continue
			}
			owner[idx] = name
		}
	}
	return defects
}

// majorityNet returns the traced-net index most of the pins landed on, so the
// report blames the strays rather than the majority. Ties break toward the
// lowest index; -1 when every pin is isolated.
func majorityNet(where []int) int {
	count := make(map[int]int, len(where))
	for _, idx := range where {
		if idx >= 0 {
			count[idx]++
		}
	}
	best, bestCount := -1, 0
	for _, idx := range where { // iterate the slice, not the map: determinism
		if idx < 0 {
			continue
		}
		if count[idx] > bestCount || (count[idx] == bestCount && idx < best) {
			best, bestCount = idx, count[idx]
		}
	}
	return best
}
