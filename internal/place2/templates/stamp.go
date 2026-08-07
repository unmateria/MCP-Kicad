package templates

import (
	"fmt"
	"sort"
	"strings"

	"mcp-kicad/internal/sexp"
)

// StampOptions controls how a template is materialised into a schematic.
type StampOptions struct {
	Anchor    [2]float64               // top-left of the stamp in schematic mm
	RefMap    map[string]string        // role → desired reference (else auto-allocated)
	PinMap    map[string]string        // role-qualified pin (e.g. "R_SDA.1") → external net name
	EmbedFunc func(libID string) error // resolves Device:R / Device:C / etc. (call site responsibility)
	// PowerLibFor maps a net name to its canonical power symbol lib_id ("" if
	// none). When set, a PinMap entry whose template-internal net is driven by
	// a baked power:* symbol swaps that symbol to the mapped rail instead of
	// stacking a second net name onto the same wire.
	PowerLibFor func(netName string) string
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

	// Decide up front how each PinMap entry is realised: swapping the power
	// symbol that names the internal net, renaming the baked label of the
	// external pin, or (fallback) dropping an extra label at the pin. Keys are
	// sorted — map iteration order must never reach the schematic.
	pinKeys := make([]string, 0, len(opts.PinMap))
	for k := range opts.PinMap {
		pinKeys = append(pinKeys, k)
	}
	sort.Strings(pinKeys)
	swapLib := map[string]string{}     // power role → replacement lib_id
	renameLabel := map[string]string{} // baked label name → external net name
	realised := map[string]bool{}      // rolePin handled by swap/rename
	for _, rolePin := range pinKeys {
		netName := opts.PinMap[rolePin]
		if opts.PowerLibFor != nil {
			if lib := opts.PowerLibFor(netName); lib != "" {
				if role := powerRoleOnNet(tpl, rolePin); role != "" {
					swapLib[role] = lib
					realised[rolePin] = true
					continue
				}
			}
		}
		for _, ep := range tpl.ExternalPins {
			if ep.From == rolePin && hasBakedLabel(tpl, ep.Label) {
				renameLabel[ep.Label] = netName
				realised[rolePin] = true
				break
			}
		}
	}

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

		libID, value := c.LibID, c.Value
		if nl, ok := swapLib[c.Role]; ok && c.Role != "" {
			libID, value = nl, strings.TrimPrefix(nl, "power:")
		}
		if opts.EmbedFunc != nil {
			if err := opts.EmbedFunc(libID); err != nil {
				return res, fmt.Errorf("embed %s: %w", libID, err)
			}
		}
		x := ax + c.RelX
		y := ay + c.RelY
		unit := c.Unit
		if unit < 1 {
			unit = 1
		}
		libDef := findLibDef(sch, libID)
		var pinNums []string
		if libDef != nil {
			pinNums = sexp.ExtractPinNumbers(libDef, unit)
		}
		// Power symbols (power:*) are virtual: no BOM/board presence, hidden
		// reference. Every other symbol is a real part.
		isPower := strings.HasPrefix(libID, "power:")
		inBom, onBoard := !isPower, !isPower
		sch.AddSymbol(sexp.NewSymbolInstance(libID, ref, value, "",
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
		name := l.Name
		if nn, ok := renameLabel[name]; ok {
			name = nn
		}
		sch.AddLabel(sexp.NewNetLabel(name, ax+l.X, ay+l.Y, l.Rotation))
		res.LabelsAdded++
	}

	// External pin labels requested by the caller and not already realised by
	// a power-symbol swap or a baked-label rename.
	for _, rolePin := range pinKeys {
		if realised[rolePin] {
			continue
		}
		netName := opts.PinMap[rolePin]
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

// powerRoleOnNet returns the role of a power:* component sharing a
// template-internal net with rolePin, or "" when that net has none.
func powerRoleOnNet(tpl Template, rolePin string) string {
	for _, n := range tpl.Nets {
		onNet := false
		for _, p := range n.Pins {
			if p == rolePin {
				onNet = true
				break
			}
		}
		if !onNet {
			continue
		}
		for _, p := range n.Pins {
			role, _ := splitRolePin(p)
			for _, c := range tpl.Components {
				if c.Role == role && strings.HasPrefix(c.LibID, "power:") {
					return role
				}
			}
		}
		return ""
	}
	return ""
}

// hasBakedLabel reports whether the template's baked geometry includes a
// label with the given name.
func hasBakedLabel(tpl Template, name string) bool {
	for _, l := range tpl.Labels {
		if l.Name == name {
			return true
		}
	}
	return false
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
