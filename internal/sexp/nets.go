package sexp

import (
	"fmt"
	"sort"
	"strings"
)

// PinRef identifies a pin on a placed symbol.
type PinRef struct {
	Reference  string
	PinNumber  string
	PinName    string
	Unit       int    // 1-based unit index of the owning instance (0 if unknown)
	Electrical string // KiCad electrical type, propagated from PinInfo
	LibID      string // owning symbol's lib_id (e.g. "power:GND", "Device:R")
}

// String returns the canonical pin reference. Single-unit symbols use
// "REF.name" (e.g. "R1.1", "BT1.+"). Multi-unit symbols disambiguate the
// unit ("U1.1.+", "U1.2.+") because pin names repeat across units.
func (p PinRef) String() string {
	name := p.PinName
	if name == "" || name == "~" {
		name = p.PinNumber
	}
	if p.Unit > 1 {
		// Unit > 1 unambiguously means multi-unit. Unit==1 might be a single-
		// unit symbol or unit 1 of a multi-unit; without IC metadata in PinRef
		// we err on the side of brevity — connect_pins/FindPin accept either form.
		return fmt.Sprintf("%s.%d.%s", p.Reference, p.Unit, name)
	}
	return p.Reference + "." + name
}

// Matches reports whether a declared pin reference designates this pin. It
// accepts both source spellings — "REF.pin" and the unit-qualified
// "REF.unit.pin" — and matches the trailing part against either the pin
// number or the pin name, so "U1.7" and "U1.VCC" both hit the same pin.
// A reference without a unit matches any unit, which is what a single-unit
// symbol needs.
func (p PinRef) Matches(decl string) bool {
	ref, unit, pin := splitRefPinUnit(decl)
	if ref != p.Reference {
		return false
	}
	if unit != 0 && p.Unit != 0 && unit != p.Unit {
		return false
	}
	return pin == p.PinNumber || pin == p.PinName
}

// Net is one electrical net in the schematic.
type Net struct {
	Name     string   // label name, or auto-generated "Net-(ref.pin)"
	Pins     []PinRef // all pins on this net
	Dangling bool     // true when fewer than 2 real component pins
}

// TraceNets builds the complete netlist by following wires and net labels.
// Each placed symbol's pins are assigned to a net by tracing wire endpoints.
// Two net labels with the same name are treated as a single net.
//
// Power symbols (lib_id "power:GND", "power:VCC", "power:+5V"…) are treated
// as implicit global labels: every pin on a `power:XXX` symbol joins the
// same XXX net, mirroring KiCad's own behaviour. This is what makes a
// schematic with three discrete `#PWR_GND` symbols electrically connected
// without any wire between them.
func TraceNets(sch *Schematic) []Net {
	uf, rootLabel := buildNetUF(sch)
	rootPins := pinsByRoot(sch, uf)

	var nets []Net
	for root, pins := range rootPins {
		name := rootLabel[root]
		if name == "" && len(pins) > 0 {
			name = "Net-(" + pins[0].String() + ")"
		}
		nets = append(nets, Net{
			Name:     name,
			Pins:     pins,
			Dangling: len(pins) < 2,
		})
	}
	// Stable output order — rootPins is a map, so without this the net slice
	// order (and thus routing order and power-emission order) would vary
	// run-to-run. Sort by name, then first pin.
	sort.Slice(nets, func(i, j int) bool {
		if nets[i].Name != nets[j].Name {
			return nets[i].Name < nets[j].Name
		}
		var pi, pj string
		if len(nets[i].Pins) > 0 {
			pi = nets[i].Pins[0].String()
		}
		if len(nets[j].Pins) > 0 {
			pj = nets[j].Pins[0].String()
		}
		return pi < pj
	})
	return nets
}

// isRailName reports whether a net name is a recognised power rail (used only
// to give rail names priority when picking a net's canonical name). Kept in
// sync with tools.netNameToPowerLibID / cluster rail matchers.
func isRailName(name string) bool {
	n := strings.ToUpper(strings.TrimSpace(name))
	switch n {
	case "GND", "GND1", "GND2", "GNDA", "GNDD", "GNDPWR", "GNDREF", "GNDS",
		"EARTH", "0V", "VCC", "VDD", "VEE", "VSS", "VAA", "AVCC", "AVDD", "VBUS":
		return true
	}
	if strings.HasPrefix(n, "+") || strings.HasPrefix(n, "-") {
		return true
	}
	return false
}

// buildNetUF performs the union-find pass shared by TraceNets and
// TracePointNets: it unions wire endpoints, same-name label positions, and
// same-rail power-symbol pins, then returns the resulting union-find
// structure plus a root→label-name map (label name here also covers the
// implicit power-rail name, e.g. "GND").
func buildNetUF(sch *Schematic) (*netUF, map[[2]float64]string) {
	uf := &netUF{parent: make(map[[2]float64][2]float64)}

	// 1. Union each wire's two endpoints.
	for _, wire := range sch.Wires() {
		pts := FindList(wire, "pts")
		if pts == nil {
			continue
		}
		var eps [][2]float64
		for _, xy := range pts.Children {
			if xy.Head() != "xy" {
				continue
			}
			eps = append(eps, netPt(parseF(AtomValue(xy, 1)), parseF(AtomValue(xy, 2))))
		}
		if len(eps) == 2 {
			uf.union(eps[0], eps[1])
		}
	}

	// 2. Collect net labels and union positions of same-name labels.
	byName := make(map[string][][2]float64)
	for _, child := range sch.root.Children {
		if child.Head() != "label" || len(child.Children) < 2 {
			continue
		}
		name := StringValue(child, 1)
		if name == "" {
			name = AtomValue(child, 1)
		}
		atN := FindList(child, "at")
		if atN == nil {
			continue
		}
		p := netPt(parseF(AtomValue(atN, 1)), parseF(AtomValue(atN, 2)))
		uf.find(p) // register in UF even if isolated
		byName[name] = append(byName[name], p)
	}
	for _, positions := range byName {
		for i := 1; i < len(positions); i++ {
			uf.union(positions[0], positions[i])
		}
	}

	// 2b. Treat each power symbol as an implicit label whose name is the
	// part name ("GND", "VCC", "+5V"…). Union the pin positions of every
	// power symbol that shares the same part name.
	powerByPart := make(map[string][][2]float64)
	for _, sym := range ReadSymbols(sch) {
		if !strings.HasPrefix(sym.LibID, "power:") || len(sym.Pins) == 0 {
			continue
		}
		// PWR_FLAG is a power-library symbol but NOT a rail: it must not join
		// every other flag into one global net. Its pin connects to the local
		// net through its stub wire only (unioned in step 1).
		if sym.LibID == "power:PWR_FLAG" {
			continue
		}
		partName := strings.TrimPrefix(sym.LibID, "power:")
		pin := sym.Pins[0]
		p := netPt(pin.X, pin.Y)
		uf.find(p)
		powerByPart[partName] = append(powerByPart[partName], p)
		// Also expose the implicit name to step 3 so the resulting Net.Name
		// is the actual rail name, not "Net-(...)".
		byName[partName] = append(byName[partName], p)
	}
	for _, positions := range powerByPart {
		for i := 1; i < len(positions); i++ {
			uf.union(positions[0], positions[i])
		}
	}

	// 3. Map label name to the root of its net. Iterate names in a stable,
	// priority-ordered sequence so the canonical name a net ends up with does
	// NOT depend on Go's randomised map iteration. When several names collide
	// on the same electrical root (e.g. an explicit "VOUT" label AND an
	// implicit "+5V" power-symbol name on the regulator output), a recognised
	// power-rail name wins; ties break alphabetically. Determinism here is
	// critical: cluster detection keys on rail names, so a flipping name
	// silently changes the whole downstream layout.
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		ri, rj := isRailName(names[i]), isRailName(names[j])
		if ri != rj {
			return ri // rail names first
		}
		return names[i] < names[j]
	})
	rootLabel := make(map[[2]float64]string)
	for _, name := range names {
		positions := byName[name]
		root := uf.find(positions[0])
		if _, exists := rootLabel[root]; !exists {
			rootLabel[root] = name
		}
	}

	return uf, rootLabel
}

// pinsByRoot assigns every component pin to its net root.
func pinsByRoot(sch *Schematic, uf *netUF) map[[2]float64][]PinRef {
	rootPins := make(map[[2]float64][]PinRef)
	for _, sym := range ReadSymbols(sch) {
		for _, pin := range sym.Pins {
			p := netPt(pin.X, pin.Y)
			root := uf.find(p)
			rootPins[root] = append(rootPins[root], PinRef{
				Reference:  sym.Reference,
				PinNumber:  pin.Number,
				PinName:    pin.Name,
				Unit:       sym.Unit,
				Electrical: pin.Electrical,
				LibID:      sym.LibID,
			})
		}
	}
	return rootPins
}

// TracePointNets computes net names with the same union-find pass as
// TraceNets, but returns a lookup from EVERY point that participates in the
// netlist graph — wire endpoints, net label positions, and component pin
// positions — to that point's net name.
//
// Net.Pins (as returned by TraceNets) only lists component pin endpoints,
// which isn't enough to attribute an arbitrary wire SEGMENT to a net: a
// segment's endpoints are often interior waypoints that never touch a real
// pin. The geometric quality gate (internal/place2/gate) uses this function
// to decide which net a given wire segment belongs to, so it can tell a
// same-net crossing (just needs a junction) from a different-net crossing
// (an electrical short drawn as a wire crossing).
//
// Points whose connected component has neither a label nor a component pin
// (a fully floating, disconnected decorative wire) get a synthetic
// per-component name derived from the root coordinate, so they are never
// mistaken for another net.
func TracePointNets(sch *Schematic) map[[2]float64]string {
	uf, rootLabel := buildNetUF(sch)
	rootPins := pinsByRoot(sch, uf)

	nameOfRoot := make(map[[2]float64]string, len(rootPins))
	for root, pins := range rootPins {
		name := rootLabel[root]
		if name == "" && len(pins) > 0 {
			name = "Net-(" + pins[0].String() + ")"
		}
		nameOfRoot[root] = name
	}
	for root, name := range rootLabel {
		if _, ok := nameOfRoot[root]; !ok {
			nameOfRoot[root] = name
		}
	}

	result := make(map[[2]float64]string)
	assign := func(p [2]float64) {
		root := uf.find(p)
		name, ok := nameOfRoot[root]
		if !ok {
			name = fmt.Sprintf("Unnamed-(%.2f,%.2f)", root[0], root[1])
		}
		result[p] = name
	}

	for _, wire := range sch.Wires() {
		pts := FindList(wire, "pts")
		if pts == nil {
			continue
		}
		for _, xy := range pts.Children {
			if xy.Head() != "xy" {
				continue
			}
			assign(netPt(parseF(AtomValue(xy, 1)), parseF(AtomValue(xy, 2))))
		}
	}
	for _, child := range sch.root.Children {
		if child.Head() != "label" {
			continue
		}
		atN := FindList(child, "at")
		if atN == nil {
			continue
		}
		assign(netPt(parseF(AtomValue(atN, 1)), parseF(AtomValue(atN, 2))))
	}
	for _, sym := range ReadSymbols(sch) {
		for _, pin := range sym.Pins {
			assign(netPt(pin.X, pin.Y))
		}
	}
	return result
}

// netPt returns a position key rounded to 2 decimal places (consistent with
// the round2 precision used throughout the sexp package).
func netPt(x, y float64) [2]float64 {
	return [2]float64{round2(x), round2(y)}
}

// --- union-find over 2D positions ---

type netUF struct {
	parent map[[2]float64][2]float64
}

func (u *netUF) find(p [2]float64) [2]float64 {
	if _, ok := u.parent[p]; !ok {
		u.parent[p] = p
		return p
	}
	if u.parent[p] == p {
		return p
	}
	root := u.find(u.parent[p])
	u.parent[p] = root // path compression
	return root
}

func (u *netUF) union(a, b [2]float64) {
	ra, rb := u.find(a), u.find(b)
	if ra != rb {
		u.parent[rb] = ra
	}
}
