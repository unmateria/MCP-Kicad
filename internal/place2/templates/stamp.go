package templates

import (
	"fmt"
	"strings"

	"mcp-kicad/internal/sexp"
)

// StampOptions controls how a template is materialised into a schematic.
type StampOptions struct {
	Anchor    [2]float64        // top-left of the stamp in schematic mm
	RefMap    map[string]string // role → desired reference (else auto-allocated)
	PinMap    map[string]string // role-qualified pin (e.g. "R_SDA.1") → external net name
	EmbedFunc func(libID string) error // resolves Device:R / Device:C / etc. (call site responsibility)
}

// StampResult reports what Stamp added.
type StampResult struct {
	PlacedRefs   []string
	LabelsAdded  int
	WiresAdded   int
	Notes        []string
}

// Stamp materialises template `tpl` into `sch`. Components are placed at
// (Anchor + rel_x, Anchor + rel_y); roles get refs from opts.RefMap or the
// next-available designator. External nets named in opts.PinMap are wired by
// dropping a label of the desired net name at each pin's position.
//
// The caller is responsible for supplying EmbedFunc — Stamp does not know
// where global symbol libraries live. Each component's lib_id is embedded
// before placement so generated schematics open cleanly.
func Stamp(sch *sexp.Schematic, tpl Template, opts StampOptions) (StampResult, error) {
	res := StampResult{}
	roleToRef := map[string]string{}
	used := existingRefSet(sch)

	for _, c := range tpl.Components {
		ref := opts.RefMap[c.Role]
		if ref == "" {
			ref = nextRefFor(c.LibID, used)
		}
		used[ref] = true
		roleToRef[c.Role] = ref

		if opts.EmbedFunc != nil {
			if err := opts.EmbedFunc(c.LibID); err != nil {
				return res, fmt.Errorf("embed %s: %w", c.LibID, err)
			}
		}
		x := opts.Anchor[0] + c.RelX
		y := opts.Anchor[1] + c.RelY
		x = sexp.SnapGrid(x)
		y = sexp.SnapGrid(y)
		unit := c.Unit
		if unit < 1 {
			unit = 1
		}
		// Pin numbers are filled in by the caller's embedder via Schematic.LibSymbols
		// once the def is present; pass nil to let NewSymbolInstance look them up.
		ls := sch.LibSymbols()
		var libDef *sexp.Node
		if ls != nil {
			for _, child := range ls.Children {
				if child.Head() == "symbol" && sexp.StringValue(child, 1) == c.LibID {
					libDef = child
					break
				}
			}
		}
		var pinNums []string
		if libDef != nil {
			pinNums = sexp.ExtractPinNumbers(libDef, unit)
		}
		sch.AddSymbol(sexp.NewSymbolInstance(c.LibID, ref, c.Value, "",
			x, y, c.Rotation, unit, pinNums, sch.UUID(), false, false, libDef))
		res.PlacedRefs = append(res.PlacedRefs, ref)
	}

	// External pin labels.
	for rolePin, netName := range opts.PinMap {
		role, pin := splitRolePin(rolePin)
		ref, ok := roleToRef[role]
		if !ok {
			res.Notes = append(res.Notes, fmt.Sprintf("pin_map: role %q not in template", role))
			continue
		}
		x, y, ok := sexp.FindPinPosition(sch, ref+"."+pin)
		if !ok {
			res.Notes = append(res.Notes, fmt.Sprintf("pin_map: pin %s.%s not resolvable", ref, pin))
			continue
		}
		sch.AddLabel(sexp.NewNetLabel(netName, x, y, 0))
		res.LabelsAdded++
	}
	return res, nil
}

func existingRefSet(sch *sexp.Schematic) map[string]bool {
	out := map[string]bool{}
	for _, s := range sexp.ReadSymbols(sch) {
		out[s.Reference] = true
	}
	return out
}

// nextRefFor allocates the next free designator (R1, C5, …) given the lib_id
// prefix family.
func nextRefFor(libID string, used map[string]bool) string {
	prefix := refPrefix(libID)
	for n := 1; n < 10000; n++ {
		ref := fmt.Sprintf("%s%d", prefix, n)
		if !used[ref] {
			return ref
		}
	}
	return prefix + "99"
}

func refPrefix(libID string) string {
	switch {
	case strings.HasPrefix(libID, "Device:R"):
		return "R"
	case strings.HasPrefix(libID, "Device:C"), strings.HasPrefix(libID, "Device:CP"):
		return "C"
	case strings.HasPrefix(libID, "Device:L"):
		return "L"
	case strings.HasPrefix(libID, "Device:LED"):
		return "D"
	case strings.HasPrefix(libID, "Device:D"):
		return "D"
	case strings.HasPrefix(libID, "Amplifier_Operational:"),
		strings.HasPrefix(libID, "MCU_"),
		strings.HasPrefix(libID, "Regulator_"),
		strings.HasPrefix(libID, "Timer:"):
		return "U"
	case strings.HasPrefix(libID, "Connector"):
		return "J"
	case strings.HasPrefix(libID, "Device:Crystal"):
		return "Y"
	}
	return "X"
}

func splitRolePin(rp string) (role, pin string) {
	idx := strings.LastIndex(rp, ".")
	if idx < 0 {
		return rp, ""
	}
	return rp[:idx], rp[idx+1:]
}
