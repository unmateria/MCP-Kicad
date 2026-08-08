package textplace

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"mcp-kicad/internal/sexp"
)

// Collision is one piece of text left overlapping something after the pass
// has run.
type Collision struct {
	Text string  // the string that is hard to read
	With string  // what it sits on: "body U1", "wire", "label SDA", "text C2"…
	Area float64 // overlapping area, mm²

	// Intrinsic marks a collision with the text's OWN symbol — its pin
	// numbers or its body. No amount of `cells` between parts will move it,
	// because the crowding is inside the package: a 6-pin opto-coupler with a
	// long net name beside it has nowhere else to put either. Worth saying,
	// because a session spent two recompiles tuning spacing against one.
	Intrinsic bool

	// NeedMM is the extra clearance that would clear this overlap: the depth
	// the two rectangles interpenetrate along the cheaper axis to separate.
	// Reported in grid cells by String, because cells are what the author
	// actually types.
	//
	// "Move something" without a quantity is an invitation to guess: three
	// separate sessions tuned spacing by trial and error, one of them through
	// 3, 6, 12 and 26 cells. Zero when Intrinsic, where no separation helps.
	//
	// It is a LOCAL figure and the wording says so. Verified by taking the
	// advice on the counter: the named labels were indeed saved, and moving
	// the parts introduced two collisions elsewhere. Separating one pair
	// re-crowds its neighbours, so this answers "what would clear this one",
	// never "what makes the sheet better".
	NeedMM float64
}

// NeedCells is NeedMM rounded up to whole 2.54 mm grid cells.
func (c Collision) NeedCells() int {
	if c.Intrinsic || c.NeedMM <= 0 {
		return 0
	}
	return int(math.Ceil(c.NeedMM/2.54 - 0.001))
}

func (c Collision) String() string {
	s := fmt.Sprintf("%q over %s (%.2f mm2)", c.Text, strings.Replace(c.With, "wire:", "wire ", 1), c.Area)
	switch {
	case c.Intrinsic:
		s += " [intrinsic to the symbol — spacing will not fix this]"
	case c.NeedCells() > 0:
		s += fmt.Sprintf(" [%d more cell(s) between these two clears THIS one]", c.NeedCells())
	}
	return s
}

// penetration is how deep two overlapping rectangles interpenetrate along the
// axis that is cheaper to separate — the distance one has to move for the
// overlap to vanish.
func penetration(a, b box) float64 {
	dx := math.Min(a.x2, b.x2) - math.Max(a.x1, b.x1)
	dy := math.Min(a.y2, b.y2) - math.Max(a.y1, b.y1)
	if dx <= 0 || dy <= 0 {
		return 0
	}
	return math.Min(dx, dy)
}

// Collisions reports the text overlaps a finished schematic still carries.
//
// Autoplace does not promise zero overlap — it moves each block to the
// LOWEST-overlap position available, and on a dense sheet the best spot still
// touches something. Nothing measured what was left, so text quality could
// only be judged by eye, one render at a time. This is the objective number:
// call it after Autoplace to see what the reader will actually squint at.
//
// Everything is compared against everything: Reference/Value blocks, net
// labels, symbol bodies, pins, wires and no-connect markers. Output is sorted
// by descending area then text, so the worst offender reads first and the
// list is stable run to run.
func Collisions(sch *sexp.Schematic) []Collision {
	syms := sexp.ReadSymbols(sch)
	insts := instanceNodes(sch)
	if len(insts) != len(syms) {
		return nil
	}

	obs, names, labels := buildScene(sch, syms)

	// Reference/Value blocks are not part of the scene buildScene returns —
	// Autoplace appends them as it places them — so add them here to make
	// text-on-text overlap visible, remembering each one's index so a block
	// never scores against itself.
	type textItem struct {
		text   string
		box    box
		obsIdx int
	}
	var items []textItem
	for _, i := range symbolOrder(syms) {
		lines := visibleFields(insts[i])
		if len(lines) == 0 {
			continue
		}
		b, ok := blockBounds(lines, syms[i].Rotation)
		if !ok {
			continue
		}
		label := syms[i].Reference
		items = append(items, textItem{text: label, box: b, obsIdx: len(obs)})
		obs = append(obs, b)
		names = append(names, "text "+label)
	}

	var out []Collision
	// Two text items overlapping is ONE collision, but both are scored, so
	// each pair would otherwise be reported twice — once from each side.
	seen := make(map[[2]int]bool)

	// anchor != nil marks a net label, which sits ON its pin and its wire by
	// construction: those two overlaps are what "attached here" looks like,
	// not something a reader struggles with. Everything whose rectangle
	// contains the anchor point is therefore excused — and only that.
	report := func(text string, b box, skip int, anchor *[2]float64, owner string) {
		for j, o := range obs {
			if j == skip {
				continue
			}
			if anchor != nil && o.contains(anchor[0], anchor[1]) {
				continue
			}
			// A net label lying along the wire it names is the convention, not
			// a collision — KiCad draws them that way. Only a stranger's wire
			// under a label misleads the reader.
			if anchor != nil && names[j] == "wire:"+text {
				continue
			}
			if area := b.overlap(o); area > eps {
				pair := [2]int{skip, j}
				if pair[0] > pair[1] {
					pair[0], pair[1] = pair[1], pair[0]
				}
				if seen[pair] {
					continue
				}
				seen[pair] = true
				intrinsic := owner != "" && belongsTo(names[j], owner)
				need := 0.0
				if !intrinsic {
					need = penetration(b, o)
				}
				out = append(out, Collision{
					Text: text, With: names[j], Area: area,
					Intrinsic: intrinsic, NeedMM: need,
				})
			}
		}
	}

	for _, it := range items {
		report(it.text, it.box, it.obsIdx, nil, it.text) // a field block is owned by its own reference
	}
	for _, i := range labelOrder(labels) {
		l := labels[i]
		report(l.name, labelBox(l.name, l.x, l.y, l.rot, l.justifyRight), l.obsIdx,
			&[2]float64{l.x, l.y}, symbolAtPoint(syms, l.x, l.y))
	}

	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Area != out[b].Area {
			return out[a].Area > out[b].Area
		}
		if out[a].Text != out[b].Text {
			return out[a].Text < out[b].Text
		}
		return out[a].With < out[b].With
	})
	return out
}

// belongsTo reports whether an obstacle name refers to the given symbol —
// "body U1", "pin U1.3", "pin number U1.3", "text U1".
func belongsTo(obstacle, ref string) bool {
	fields := strings.Fields(obstacle)
	if len(fields) == 0 {
		return false
	}
	last := fields[len(fields)-1]
	if i := strings.IndexByte(last, '.'); i >= 0 {
		last = last[:i]
	}
	return last == ref
}

// symbolAtPoint returns the reference of the symbol owning a pin at (x, y),
// which is the symbol a net label anchored there belongs to.
func symbolAtPoint(syms []sexp.SchematicSymbol, x, y float64) string {
	for _, s := range syms {
		for _, p := range s.Pins {
			if math.Abs(p.X-x) < eps && math.Abs(p.Y-y) < eps {
				return s.Reference
			}
		}
	}
	return ""
}
