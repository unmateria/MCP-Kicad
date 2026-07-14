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
	PlacedRefs     []string
	RoleRefs       map[string]string // role → allocated reference designator
	LabelsAdded    int
	WiresAdded     int
	JunctionsAdded int
	Notes          []string
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
	res := StampResult{RoleRefs: map[string]string{}}
	roleToRef := map[string]string{}
	used := existingRefSet(sch)

	// Snap the anchor to the connection grid ONCE. Template rel_x/rel_y are
	// authored on the 1.27 mm grid, so (snapped anchor + rel) is always on grid
	// and the later per-node snap in NewWire/NewSymbolInstance is a no-op. This
	// keeps wire endpoints exactly on their pins regardless of the raw anchor.
	ax := sexp.SnapGrid(opts.Anchor[0])
	ay := sexp.SnapGrid(opts.Anchor[1])

	for _, c := range tpl.Components {
		var ref string
		switch {
		case c.SameRefAs != "":
			// Reuse the reference already allocated to another role (a further
			// unit of the same multi-unit symbol).
			r, ok := roleToRef[c.SameRefAs]
			if !ok {
				return res, fmt.Errorf("same_ref_as %q refers to an unknown/earlier role", c.SameRefAs)
			}
			ref = r
		case opts.RefMap[c.Role] != "":
			ref = opts.RefMap[c.Role]
			used[ref] = true
		default:
			ref = nextRefFor(c.LibID, used)
			used[ref] = true
		}
		if c.Role != "" {
			roleToRef[c.Role] = ref
			res.RoleRefs[c.Role] = ref
		}

		if opts.EmbedFunc != nil {
			if err := opts.EmbedFunc(c.LibID); err != nil {
				return res, fmt.Errorf("embed %s: %w", c.LibID, err)
			}
		}
		x := ax + c.RelX
		y := ay + c.RelY
		unit := c.Unit
		if unit < 1 {
			unit = 1
		}
		libDef := findLibDef(sch, c.LibID)
		var pinNums []string
		if libDef != nil {
			pinNums = sexp.ExtractPinNumbers(libDef, unit)
		}
		// Power symbols (power:*) are virtual: no BOM/board presence, hidden
		// reference. Every other symbol is a real part.
		isPower := strings.HasPrefix(c.LibID, "power:")
		inBom, onBoard := !isPower, !isPower
		sch.AddSymbol(sexp.NewSymbolInstance(c.LibID, ref, c.Value, "",
			x, y, c.Rotation, unit, pinNums, sch.UUID(), inBom, onBoard, libDef))
		res.PlacedRefs = append(res.PlacedRefs, ref)
	}

	// Baked geometry: wires, junctions and labels copied verbatim, translated
	// by the stamp anchor.
	for _, w := range tpl.Wires {
		sch.AddWire(sexp.NewWire(ax+w.X1, ay+w.Y1, ax+w.X2, ay+w.Y2))
		res.WiresAdded++
	}
	for _, j := range tpl.Junctions {
		sch.AddJunction(sexp.NewJunction(ax+j.X, ay+j.Y))
		res.JunctionsAdded++
	}
	for _, l := range tpl.Labels {
		sch.AddLabel(sexp.NewNetLabel(l.Name, ax+l.X, ay+l.Y, l.Rotation))
		res.LabelsAdded++
	}

	// External pin labels requested by the caller (renames/extra connections).
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

// findLibDef returns the embedded lib_symbols definition for libID, or nil.
func findLibDef(sch *sexp.Schematic, libID string) *sexp.Node {
	ls := sch.LibSymbols()
	if ls == nil {
		return nil
	}
	for _, child := range ls.Children {
		if child.Head() == "symbol" && sexp.StringValue(child, 1) == libID {
			return child
		}
	}
	return nil
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
	case strings.HasPrefix(libID, "power:"):
		return "#PWR"
	case strings.HasPrefix(libID, "Device:Crystal"):
		return "Y"
	case strings.HasPrefix(libID, "Device:LED"):
		return "D"
	case strings.HasPrefix(libID, "Device:R"):
		return "R"
	case strings.HasPrefix(libID, "Device:CP"), strings.HasPrefix(libID, "Device:C"):
		return "C"
	case strings.HasPrefix(libID, "Device:L"):
		return "L"
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
