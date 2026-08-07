package power

import (
	"math"
	"sort"
	"strings"

	"mcp-kicad/internal/sexp"
)

// Registry tracks which (libID, snapped-position) buckets already have a
// power symbol so subsequent emitters can skip duplicates. It is safe to
// share across tool calls within a single compile pass.
type Registry struct {
	seen map[bucketKey]bool
}

type bucketKey struct {
	lib string
	x   int
	y   int
}

func NewRegistry() *Registry { return &Registry{seen: make(map[bucketKey]bool)} }

// Has reports whether (libID, position) is already occupied. The position is
// snapped to the SnapStep grid so two pins that placed power symbols at the
// "same" coord — even if their floats drifted by a tenth of a mm — collide.
func (r *Registry) Has(libID string, x, y float64) bool {
	if r == nil {
		return false
	}
	return r.seen[r.key(libID, x, y)]
}

// Mark records the placement. Returns true if it is fresh, false if duplicate.
func (r *Registry) Mark(libID string, x, y float64) bool {
	k := r.key(libID, x, y)
	if r.seen[k] {
		return false
	}
	r.seen[k] = true
	return true
}

func (r *Registry) key(libID string, x, y float64) bucketKey {
	return bucketKey{
		lib: libID,
		x:   int(math.Round(x / SnapStep)),
		y:   int(math.Round(y / SnapStep)),
	}
}

// MergePowerSymbols deletes duplicate `power:*` symbols already present in the
// schematic. Two symbols collide when they share the same lib_id and snap to
// the same SnapStep cell. The first encountered survives. Returns the number
// of duplicates removed.
func MergePowerSymbols(sch *sexp.Schematic) int {
	syms := sexp.ReadSymbols(sch)
	type pos struct {
		x, y int
	}
	seen := make(map[string]map[pos]bool)
	var toRemove []string
	for _, s := range syms {
		if !strings.HasPrefix(s.LibID, "power:") {
			continue
		}
		p := pos{int(math.Round(s.X / SnapStep)), int(math.Round(s.Y / SnapStep))}
		m, ok := seen[s.LibID]
		if !ok {
			m = make(map[pos]bool)
			seen[s.LibID] = m
		}
		if m[p] {
			toRemove = append(toRemove, s.Reference)
			continue
		}
		m[p] = true
	}
	if len(toRemove) == 0 {
		return 0
	}
	removed := 0
	for _, ref := range toRemove {
		if removeSymbolByRef(sch, ref) {
			removed++
		}
	}
	return removed
}

// AlignPowerBus snaps every power symbol of `family` (e.g. "+12V") to the
// MEDIAN Y of the group when the group has at least minBus members. The X
// coordinate is left alone — symbols stay above their respective pins. When
// `family` is empty all rail families are aligned.
func AlignPowerBus(sch *sexp.Schematic, family string, minBus int) int {
	if minBus < 2 {
		minBus = 2
	}
	syms := sexp.ReadSymbols(sch)
	groups := make(map[string][]sexp.SchematicSymbol)
	for _, s := range syms {
		if !strings.HasPrefix(s.LibID, "power:") {
			continue
		}
		part := strings.TrimPrefix(s.LibID, "power:")
		if family != "" && !strings.EqualFold(part, family) {
			continue
		}
		groups[part] = append(groups[part], s)
	}
	moved := 0
	for _, group := range groups {
		if len(group) < minBus {
			continue
		}
		ys := make([]float64, len(group))
		for i, s := range group {
			ys[i] = s.Y
		}
		sort.Float64s(ys)
		canonY := sexp.SnapGrid(ys[len(ys)/2])
		for _, s := range group {
			if math.Abs(s.Y-canonY) < 1e-6 {
				continue
			}
			if sch.MoveSymbol(s.Reference, s.X, canonY) > 0 {
				moved++
			}
		}
	}
	return moved
}

// removeSymbolByRef strips one symbol instance from the schematic root by its
// reference designator. Returns true if removed.
func removeSymbolByRef(sch *sexp.Schematic, ref string) bool {
	root := sch.Root()
	for i, child := range root.Children {
		if child.Head() != "symbol" {
			continue
		}
		if symbolReference(child) == ref {
			root.Children = append(root.Children[:i], root.Children[i+1:]...)
			return true
		}
	}
	return false
}

func symbolReference(sym *sexp.Node) string {
	for _, c := range sym.Children {
		if c.Head() != "property" {
			continue
		}
		if len(c.Children) >= 2 {
			name := c.Children[0].Value
			val := c.Children[1].Value
			if name == "Reference" {
				return val
			}
		}
	}
	return ""
}
