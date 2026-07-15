package cluster

import (
	"strings"
)

// rule is one cluster-detection pattern.
type rule struct {
	name   string
	detect func(*detectionContext) []Cluster
}

// rulesByPriority lists patterns highest-priority first. Cluster.Detect
// processes them in order; once a ref is claimed it cannot be re-claimed by
// a lower-priority rule.
var rulesByPriority = []rule{
	{name: "decoupling", detect: detectDecoupling},
	{name: "pullup", detect: detectPullup},
	{name: "lc_filter", detect: detectLCFilter},
	{name: "crystal", detect: detectCrystal},
	{name: "opamp_feedback", detect: detectOpAmpFeedback},
	{name: "bias_compensation", detect: detectBiasCompensation},
	{name: "voltage_divider", detect: detectVoltageDivider},
	{name: "io_connector", detect: detectIOConnector},
	{name: "header", detect: detectHeader},
}

// detectDecoupling looks for IC↔C pairs where both pins of the cap are on a
// power rail (Vcc/+5V/+12V on one side, GND on the other) AND the IC also
// has pins on both rails. Multiple caps anchor to the same IC.
//
// For multi-unit ICs (op-amps with separate signal + power units, MCUs with
// internal banks), the decoupling check is performed PER UNIT — only the
// unit that actually touches the supply rail electrically claims the cap.
// Cluster anchors take the form "REF#unit" so the layout step pulls the cap
// toward the power-bearing unit, not the canonical (often signal-only) unit.
func detectDecoupling(ctx *detectionContext) []Cluster {
	var clusters []Cluster
	for _, ref := range ctx.refOrder {
		sym := ctx.symByRef[ref]
		if !isIC(sym.LibID) {
			continue
		}
		// For each unit individually, check which caps decouple THAT unit.
		units := ctx.unitsByRef[ref]
		if len(units) == 0 {
			units = []int{1}
		}
		for _, unit := range units {
			var caps []string
			seen := make(map[string]bool)
			for _, capRef := range ctx.refOrder {
				capSym := ctx.symByRef[capRef]
				if !isCapacitor(capSym.LibID) || seen[capRef] {
					continue
				}
				if capDecouplesUnit(ctx, ref, unit, capRef) {
					caps = append(caps, capRef)
					seen[capRef] = true
				}
			}
			if len(caps) == 0 {
				continue
			}
			anchor := anchorKey(ctx, ref, unit)
			clusters = append(clusters, Cluster{
				Kind:   "decoupling",
				Refs:   append([]string{anchor}, caps...),
				Anchor: anchor,
			})
		}
	}
	return clusters
}

// capDecouplesUnit reports whether `capRef` is a bypass cap for the SPECIFIC
// unit of `icRef`. The rule:
//
//   - the cap has two pins, one on a supply rail (+12V/Vcc/-12V/Vee) and the
//     other on GND/Earth — that's what makes it a "bypass cap" topologically.
//   - AT LEAST ONE of those rails is on a pin of the IC unit (we don't require
//     both, because op-amps and many ICs have V+ and V- pins but no GND pin —
//     the cap still bypasses for that unit even though the IC doesn't pin to
//     GND directly).
func capDecouplesUnit(ctx *detectionContext, icRef string, unit int, capRef string) bool {
	icRails := railsOfUnit(ctx, icRef, unit)
	if len(icRails) == 0 {
		return false
	}
	capRails := railsOf(ctx, capRef)
	if len(capRails) != 2 {
		return false
	}
	hasGND := false
	hasSupply := false
	sharedWithIC := false
	for r := range capRails {
		if icRails[r] {
			sharedWithIC = true
		}
		switch {
		case isGroundName(r):
			hasGND = true
		case isPositiveSupplyName(r), isNegativeSupplyName(r):
			hasSupply = true
		}
	}
	return hasGND && hasSupply && sharedWithIC
}

// capDecouples reports whether `capRef`'s two pins land on rails that the
// `icRef` also touches (one supply + GND, where supply may be positive
// like +5V/Vcc OR negative like -12V/Vee — split-rail amps need their
// negative rail bypassed too).
//
// Kept for backward compatibility with single-unit callers; the multi-unit
// detector uses capDecouplesUnit directly.
func capDecouples(ctx *detectionContext, icRef, capRef string) bool {
	icRails := railsOf(ctx, icRef)
	capRails := railsOf(ctx, capRef)
	if len(capRails) != 2 {
		return false
	}
	hasGND := false
	hasSupply := false
	for r := range capRails {
		if !icRails[r] {
			return false
		}
		switch {
		case isGroundName(r):
			hasGND = true
		case isPositiveSupplyName(r), isNegativeSupplyName(r):
			hasSupply = true
		}
	}
	return hasGND && hasSupply
}

// isNegativeSupplyName matches names like -12V, -5V, VEE.
func isNegativeSupplyName(name string) bool {
	n := strings.ToUpper(strings.TrimSpace(name))
	if n == "VEE" {
		return true
	}
	if strings.HasPrefix(n, "-") {
		return true
	}
	return false
}

// detectPullup matches a pull-up topology: a single resistor whose pin 1 is
// on a positive supply and pin 2 is on a non-supply signal that the IC also
// touches. SDA/SCL pull-ups are the canonical case.
func detectPullup(ctx *detectionContext) []Cluster {
	var clusters []Cluster
	for _, ref := range ctx.refOrder {
		sym := ctx.symByRef[ref]
		if !isResistor(sym.LibID) {
			continue
		}
		rails := railsOf(ctx, ref)
		var supply, signal string
		for r := range rails {
			if isPositiveSupplyName(r) {
				supply = r
			} else if !isGroundName(r) {
				signal = r
			}
		}
		if supply == "" || signal == "" {
			continue
		}
		// Find the IC sharing the signal net with the resistor.
		signalNet := ctx.netByName[signal]
		var owners []string
		for _, p := range signalNet.Pins {
			if p.Reference == ref || ctx.powerLibIDs[p.Reference] {
				continue
			}
			s, ok := ctx.symByRef[p.Reference]
			if !ok {
				continue
			}
			if isIC(s.LibID) {
				owners = append(owners, p.Reference)
			}
		}
		if len(owners) == 0 {
			continue
		}
		clusters = append(clusters, Cluster{
			Kind:   "pullup",
			Refs:   []string{owners[0], ref},
			Anchor: owners[0],
		})
	}
	return clusters
}

// detectLCFilter finds an inductor whose pins are on a switch node and an
// output rail, paired with an output cap on the same rail+GND. Buck/boost
// converters and pi-filters fall out of this naturally.
func detectLCFilter(ctx *detectionContext) []Cluster {
	var clusters []Cluster
	for _, ref := range ctx.refOrder {
		sym := ctx.symByRef[ref]
		if !isInductor(sym.LibID) {
			continue
		}
		rails := railsOf(ctx, ref)
		if len(rails) != 2 {
			continue
		}
		// Find a cap connecting to either of the inductor's nets and GND.
		var partner string
		for _, capRef := range ctx.refOrder {
			capSym := ctx.symByRef[capRef]
			if !isCapacitor(capSym.LibID) {
				continue
			}
			capRails := railsOf(ctx, capRef)
			hasGND := false
			shared := false
			for r := range capRails {
				if isGroundName(r) {
					hasGND = true
				}
				if rails[r] {
					shared = true
				}
			}
			if hasGND && shared {
				partner = capRef
				break
			}
		}
		if partner == "" {
			continue
		}
		clusters = append(clusters, Cluster{
			Kind:   "lc_filter",
			Refs:   []string{ref, partner},
			Anchor: ref,
		})
	}
	return clusters
}

// detectCrystal pairs a crystal with its two load capacitors. The canonical
// topology is XTAL1/XTAL2 nets each shared between the crystal, one cap, and
// the MCU. We tolerate either net name.
func detectCrystal(ctx *detectionContext) []Cluster {
	var clusters []Cluster
	for _, ref := range ctx.refOrder {
		sym := ctx.symByRef[ref]
		if !isCrystal(sym.LibID) {
			continue
		}
		nets := ctx.netsByRef[ref]
		var caps []string
		var ic string
		seen := make(map[string]bool)
		for _, net := range nets {
			for _, p := range net.Pins {
				if p.Reference == ref || seen[p.Reference] || ctx.powerLibIDs[p.Reference] {
					continue
				}
				seen[p.Reference] = true
				s, ok := ctx.symByRef[p.Reference]
				if !ok {
					continue
				}
				switch {
				case isCapacitor(s.LibID):
					caps = append(caps, p.Reference)
				case isIC(s.LibID):
					if ic == "" {
						ic = p.Reference
					}
				}
			}
		}
		if len(caps) < 2 {
			continue
		}
		members := append([]string{ref}, caps...)
		if ic != "" {
			// Don't claim the IC — it may anchor a decoupling cluster too.
			// Instead just include caps + crystal so layout keeps them tight.
		}
		clusters = append(clusters, Cluster{
			Kind:   "crystal",
			Refs:   members,
			Anchor: ref,
		})
	}
	return clusters
}

// detectOpAmpFeedback looks for resistors connected between an op-amp's
// output and inverting input, plus the input-side resistor. This is the
// classic inverting-amp / non-inverting-amp Rg/Rf topology.
//
// We classify each candidate resistor by WHICH op-amp pin its shared net
// touches:
//   - shared with output AND inverting input → Rf (feedback). Rank: 100.
//   - shared with inverting input only       → Rin (input). Rank: 50.
//   - shared with output only                → Rout (load). Rank: 30.
//   - shared ONLY with non-inverting input   → BIAS — REJECTED. Op-amp bias
//     compensation resistors should not be pulled into the feedback cluster
//     because their physical placement convention is different (they go
//     between V+ and GND directly under the op-amp, not in the feedback loop).
//
// Members are sorted by rank descending so the clusterapply offsets table
// puts Rf (top-loop above the body) first and Rin (left of inverting pin)
// second.
func detectOpAmpFeedback(ctx *detectionContext) []Cluster {
	var clusters []Cluster
	for _, ref := range ctx.refOrder {
		sym := ctx.symByRef[ref]
		if !isOpAmp(sym.LibID) {
			continue
		}
		// Find which signal nets connect to which functional pin of the op-amp.
		// Multi-unit op-amps have one signal unit (with +/-/output) and one
		// power unit (with V+/V-) — so we walk every unit and look for the
		// pin names regardless of which unit they belong to.
		var plusNets, minusNets, outNets map[string]bool
		plusNets = make(map[string]bool)
		minusNets = make(map[string]bool)
		outNets = make(map[string]bool)
		for _, n := range ctx.netsByRef[ref] {
			for _, p := range n.Pins {
				if p.Reference != ref {
					continue
				}
				switch p.PinName {
				case "+":
					plusNets[n.Name] = true
				case "-":
					minusNets[n.Name] = true
				case "~", "":
					// Some symbol libs use "~" or empty for the output pin
					// of a single-output op-amp.
					if p.PinNumber != "" && !strings.HasPrefix(p.PinNumber, "V") {
						outNets[n.Name] = true
					}
				default:
					// Pin "1" / "OUT" — treat as output when not power.
					if isOpAmpOutputPin(p.PinNumber, p.PinName) {
						outNets[n.Name] = true
					}
				}
			}
		}

		type scoredMember struct {
			ref  string
			rank int
		}
		var scored []scoredMember
		for _, resRef := range ctx.refOrder {
			resSym := ctx.symByRef[resRef]
			if !isResistor(resSym.LibID) {
				continue
			}
			rNets := railsOf(ctx, resRef)
			touchesPlus, touchesMinus, touchesOut := false, false, false
			for r := range rNets {
				if plusNets[r] {
					touchesPlus = true
				}
				if minusNets[r] {
					touchesMinus = true
				}
				if outNets[r] {
					touchesOut = true
				}
			}
			rank := 0
			switch {
			case touchesMinus && touchesOut:
				rank = 100 // Rf — feedback
			case touchesMinus:
				rank = 50 // Rin — input
			case touchesOut:
				rank = 30 // Rout / load
			case touchesPlus && !touchesMinus && !touchesOut:
				rank = 0 // BIAS — reject
			}
			if rank > 0 {
				scored = append(scored, scoredMember{resRef, rank})
			}
		}
		if len(scored) < 1 {
			continue
		}
		// Sort by rank descending (insertion sort, stable).
		for i := 1; i < len(scored); i++ {
			for j := i; j > 0; j-- {
				a, b := scored[j-1], scored[j]
				less := a.rank < b.rank || (a.rank == b.rank && a.ref > b.ref)
				if less {
					scored[j-1], scored[j] = b, a
				}
			}
		}
		members := make([]string, len(scored))
		for i, s := range scored {
			members[i] = s.ref
		}
		clusters = append(clusters, Cluster{
			Kind:   "opamp_feedback",
			Refs:   append([]string{ref}, members...),
			Anchor: ref,
		})
	}
	return clusters
}

// isOpAmpOutputPin recognises the output pin of a single-output op-amp by
// pin number / name. Pin "1" is the canonical output for KiCad TL07x/NE5532
// /LM358 symbols; "OUT" is the named-pin alias.
func isOpAmpOutputPin(num, name string) bool {
	if name == "OUT" || name == "Y" {
		return true
	}
	if num == "1" || num == "6" || num == "7" {
		// 1: TL07x/NE5532 unit-1 output. 6: dual-opamp unit-2 LM358 output.
		// 7: dual-opamp NE5532 unit-2 output.
		return true
	}
	return false
}

// detectBiasCompensation looks for the canonical op-amp bias compensation
// resistor: a resistor whose one pin is on the op-amp's `+` (non-inverting)
// input net and the other pin is on GND. The resistor's purpose is to match
// the input bias current path of the inverting input — the resistor value
// should equal R1 || R2 from the feedback loop, but topology-wise it's
// always op-amp + ↔ GND.
//
// The cluster is anchored on the op-amp so the resistor sits adjacent to
// the + pin, eliminating the visual gap between bias R3 and the op-amp.
func detectBiasCompensation(ctx *detectionContext) []Cluster {
	var clusters []Cluster
	for _, opRef := range ctx.refOrder {
		opSym := ctx.symByRef[opRef]
		if !isOpAmp(opSym.LibID) {
			continue
		}
		// Find the net touching the op-amp's `+` pin.
		var plusNet string
		for _, n := range ctx.netsByRef[opRef] {
			for _, p := range n.Pins {
				if p.Reference == opRef && p.PinName == "+" {
					plusNet = n.Name
					break
				}
			}
			if plusNet != "" {
				break
			}
		}
		if plusNet == "" {
			continue
		}
		// Walk resistors and check the topology.
		for _, resRef := range ctx.refOrder {
			resSym := ctx.symByRef[resRef]
			if !isResistor(resSym.LibID) {
				continue
			}
			rRails := railsOf(ctx, resRef)
			touchesPlus := rRails[plusNet]
			touchesGND := false
			for r := range rRails {
				if isGroundName(r) {
					touchesGND = true
				}
			}
			if !touchesPlus || !touchesGND {
				continue
			}
			clusters = append(clusters, Cluster{
				Kind:   "bias_compensation",
				Refs:   []string{opRef, resRef},
				Anchor: opRef,
			})
		}
	}
	return clusters
}

// detectIOConnector identifies a single-signal-pin connector and pairs it
// with its sole non-power partner. The cluster anchors on the partner so the
// connector lands adjacent — eliminates the wide horizontal gap PlaceFlow
// leaves between input/output connectors and the rest of the signal flow.
//
// "Single signal pin" means: connectors with exactly one pin on a
// non-supply, non-GND net.
//
// Direction is inferred from the net name: nets named "OUT", "OUTPUT", "VOUT"
// or starting with "OUT_" map to io_output (place RIGHT of partner). Anything
// else is treated as input (place LEFT). This is more reliable than walking
// net.Pins order because TraceNets reorders by union-find iteration.
func detectIOConnector(ctx *detectionContext) []Cluster {
	var clusters []Cluster
	for _, ref := range ctx.refOrder {
		sym := ctx.symByRef[ref]
		if !isConnector(sym.LibID) {
			continue
		}
		var signalNet string
		signalNetCount := 0
		for _, n := range ctx.netsByRef[ref] {
			if isGroundName(n.Name) || isPositiveSupplyName(n.Name) || isNegativeSupplyName(n.Name) {
				continue
			}
			signalNet = n.Name
			signalNetCount++
		}
		if signalNetCount != 1 {
			continue
		}
		net := ctx.netByName[signalNet]
		var partner string
		for _, p := range net.Pins {
			if p.Reference == ref || ctx.powerLibIDs[p.Reference] {
				continue
			}
			partner = p.Reference
			break
		}
		if partner == "" {
			continue
		}
		kind := "io_input"
		if isOutputNetName(signalNet) {
			kind = "io_output"
		}
		clusters = append(clusters, Cluster{
			Kind:   kind,
			Refs:   []string{partner, ref},
			Anchor: partner,
		})
	}
	return clusters
}

// isOutputNetName returns true for net names that conventionally represent
// the output of a circuit: OUT, OUTPUT, VOUT, OUT_x.
func isOutputNetName(name string) bool {
	n := strings.ToUpper(strings.TrimSpace(name))
	switch n {
	case "OUT", "OUTPUT", "VOUT", "OUT1", "OUT2":
		return true
	}
	if strings.HasPrefix(n, "OUT_") {
		return true
	}
	return false
}

// detectVoltageDivider finds two resistors in series between Vcc and GND
// (each on one rail and sharing a third "tap" net). Order: top resistor first.
func detectVoltageDivider(ctx *detectionContext) []Cluster {
	var clusters []Cluster
	resistors := make([]string, 0)
	for _, ref := range ctx.refOrder {
		sym := ctx.symByRef[ref]
		if isResistor(sym.LibID) {
			resistors = append(resistors, ref)
		}
	}
	for i, top := range resistors {
		topRails := railsOf(ctx, top)
		hasVcc := false
		var topTap string
		for r := range topRails {
			if isPositiveSupplyName(r) {
				hasVcc = true
			} else if !isGroundName(r) {
				topTap = r
			}
		}
		if !hasVcc || topTap == "" {
			continue
		}
		for j, bot := range resistors {
			if i == j {
				continue
			}
			botRails := railsOf(ctx, bot)
			hasGND := false
			botTap := ""
			for r := range botRails {
				if isGroundName(r) {
					hasGND = true
				} else if !isPositiveSupplyName(r) {
					botTap = r
				}
			}
			if !hasGND || botTap != topTap {
				continue
			}
			clusters = append(clusters, Cluster{
				Kind:   "voltage_divider",
				Refs:   []string{top, bot},
				Anchor: top,
			})
		}
	}
	return clusters
}

// detectHeader keeps a connector's pins together by collecting every
// component that shares a non-power net with it. Used so the connector and
// the "first hop" of routing wires sit close together.
//
// We REJECT members that are themselves ICs (op-amps, MCUs, regulators) —
// those are anchors of more important clusters (decoupling, opamp_feedback).
// We also reject capacitors that the IC unit they pair with would claim as
// decoupling — otherwise the header rule wins by alphabetical priority of
// the cap reference and pulls them away from where they belong.
//
// This keeps headers as "connector + nearby passives that route to nothing
// else important" rather than "connector + every signal partner".
func detectHeader(ctx *detectionContext) []Cluster {
	var clusters []Cluster
	// Pre-compute decoupling-claim set so we don't drag bypass caps into
	// the header cluster.
	decoupled := make(map[string]bool)
	for _, ref := range ctx.refOrder {
		sym := ctx.symByRef[ref]
		if !isIC(sym.LibID) {
			continue
		}
		units := ctx.unitsByRef[ref]
		if len(units) == 0 {
			units = []int{1}
		}
		for _, unit := range units {
			for _, capRef := range ctx.refOrder {
				capSym := ctx.symByRef[capRef]
				if !isCapacitor(capSym.LibID) {
					continue
				}
				if capDecouplesUnit(ctx, ref, unit, capRef) {
					decoupled[capRef] = true
				}
			}
		}
	}

	for _, ref := range ctx.refOrder {
		sym := ctx.symByRef[ref]
		if !isConnector(sym.LibID) {
			continue
		}
		seen := map[string]bool{ref: true}
		var members []string
		for _, n := range ctx.netsByRef[ref] {
			for _, p := range n.Pins {
				if seen[p.Reference] || ctx.powerLibIDs[p.Reference] {
					continue
				}
				ms, ok := ctx.symByRef[p.Reference]
				if !ok {
					continue
				}
				if isIC(ms.LibID) {
					// Op-amps / MCUs / regulators anchor their own clusters.
					seen[p.Reference] = true
					continue
				}
				if isCapacitor(ms.LibID) && decoupled[p.Reference] {
					seen[p.Reference] = true
					continue
				}
				seen[p.Reference] = true
				members = append(members, p.Reference)
			}
		}
		if len(members) == 0 {
			continue
		}
		clusters = append(clusters, Cluster{
			Kind:   "header",
			Refs:   append([]string{ref}, members...),
			Anchor: ref,
		})
	}
	return clusters
}

// railsOf returns the set of nets a ref is connected to (aggregated across
// all units).
func railsOf(ctx *detectionContext, ref string) map[string]bool {
	out := make(map[string]bool)
	for _, n := range ctx.netsByRef[ref] {
		out[n.Name] = true
	}
	return out
}

// railsOfUnit returns the set of nets the specified unit of `ref` is on.
// For single-unit symbols this matches railsOf. For multi-unit ICs (NE5532,
// LM358) it lets decoupling detection ask "what nets does the power unit
// touch?" rather than aggregating across all units.
func railsOfUnit(ctx *detectionContext, ref string, unit int) map[string]bool {
	if unit <= 0 {
		unit = 1
	}
	out := make(map[string]bool)
	for _, n := range ctx.netsByRefUnit[unitKey(ref, unit)] {
		out[n.Name] = true
	}
	return out
}

// --- lib_id matchers ---------------------------------------------------

func isResistor(libID string) bool {
	switch libID {
	case "Device:R", "Device:R_Small", "Device:R_US", "Device:R_Variable",
		"Device:R_Pack02", "Device:R_Pack04", "Device:R_Pack08":
		return true
	}
	return false
}

func isCapacitor(libID string) bool {
	switch libID {
	case "Device:C", "Device:C_Small", "Device:C_Polarized",
		"Device:C_Polarized_Small", "Device:CP", "Device:CP_Small":
		return true
	}
	return false
}

func isInductor(libID string) bool {
	switch libID {
	case "Device:L", "Device:L_Small", "Device:L_Core_Iron", "Device:L_Coupled":
		return true
	}
	return false
}

func isCrystal(libID string) bool {
	return strings.Contains(libID, "Crystal")
}

func isOpAmp(libID string) bool {
	return strings.HasPrefix(libID, "Amplifier_Operational:")
}

func isConnector(libID string) bool {
	return strings.HasPrefix(libID, "Connector:") ||
		strings.HasPrefix(libID, "Connector_Generic:")
}

// isIC: anything with multi-pin behaviour we recognise as a "main IC"
// for the purposes of decoupling clustering.
func isIC(libID string) bool {
	switch {
	case strings.HasPrefix(libID, "MCU_"):
		return true
	case strings.HasPrefix(libID, "Amplifier_Operational:"):
		return true
	case strings.HasPrefix(libID, "Regulator_Linear:"),
		strings.HasPrefix(libID, "Regulator_Switching:"):
		return true
	case strings.HasPrefix(libID, "Interface_"):
		return true
	case strings.HasPrefix(libID, "Logic_"):
		return true
	case strings.HasPrefix(libID, "Memory_"):
		return true
	}
	return false
}

// --- net name matchers -------------------------------------------------

func isGroundName(name string) bool {
	n := strings.ToUpper(strings.TrimSpace(name))
	switch n {
	case "GND", "GND1", "GND2", "GNDA", "GNDD", "GNDPWR", "GNDREF",
		"GNDS", "EARTH", "0V", "VSS", "VEE":
		return true
	}
	return false
}

// isPositiveSupplyName matches names like VCC, VDD, +5V, +12V, +3V3, +5VA…
// as well as bare numeric supplies. False for plain V (too generic).
func isPositiveSupplyName(name string) bool {
	n := strings.ToUpper(strings.TrimSpace(name))
	if n == "" {
		return false
	}
	switch n {
	case "VCC", "VDD", "VAA", "AVCC", "AVDD", "VBUS":
		return true
	}
	if strings.HasPrefix(n, "+") {
		return true
	}
	if strings.HasPrefix(n, "VCC") || strings.HasPrefix(n, "VDD") {
		return true
	}
	return false
}
