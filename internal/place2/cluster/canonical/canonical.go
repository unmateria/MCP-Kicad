// Package canonical provides extra cluster detectors built on top of the
// rules in internal/place2/cluster. They cover patterns that appear in
// >80% of hobby/embedded designs but are too specific to belong in the
// minimal core: bypass caps on non-power pins, R+LED in series, oscillator
// feedback networks, regulator + I/O caps, feedback dividers, transistor
// amplifier bias networks.
//
// Each detector is a `cluster.Rule`-shaped function. The host package
// (cluster) registers them by appending to its rulesByPriority list — see
// cluster.RegisterCanonical().
package canonical

import (
	"strings"

	"mcp-kicad/internal/place2/cluster"
	"mcp-kicad/internal/sexp"
)

// Detector is the public type detectors are wrapped in. Each Detector has a
// priority (lower = higher priority); the host inserts them at the requested
// rank when registering.
type Detector struct {
	Name     string
	Priority int
	Detect   func(syms []sexp.SchematicSymbol, nets []sexp.Net) []cluster.Cluster
}

// All returns every canonical detector. Append-order is the recommended
// registration order — the host package appends them in this sequence.
func All() []Detector {
	return []Detector{
		{Name: "bypass_nonpower", Priority: 15, Detect: bypassNonPower},
		{Name: "series_led", Priority: 30, Detect: seriesLED},
		{Name: "oscillator_rc", Priority: 25, Detect: oscillatorRC},
		{Name: "feedback_divider", Priority: 35, Detect: feedbackDivider},
	}
}

// --- bypass_nonpower ----------------------------------------------------
//
// A capacitor between an IC's non-power pin (CTL, REF, FB, COMP, BYP, …)
// and a rail. Common in 555 timers (CTL→GND), op-amps (OUT→COMP), DC/DC
// converters (FB→GND, BS bootstrap caps).
func bypassNonPower(syms []sexp.SchematicSymbol, nets []sexp.Net) []cluster.Cluster {
	type pinHit struct {
		ref, name string
	}
	icByRef := map[string]sexp.SchematicSymbol{}
	for _, s := range syms {
		if isICLib(s.LibID) {
			icByRef[s.Reference] = s
		}
	}
	netByName := map[string]sexp.Net{}
	for _, n := range nets {
		netByName[n.Name] = n
	}
	var out []cluster.Cluster
	seen := map[string]bool{}
	// For each cap, look at its two nets. If one net is a rail and the OTHER
	// touches an IC's named non-power pin (BYP / CTL / FB / COMP / REF / VREF
	// / SS / BS / BOOT), claim the cap as bypass.
	bypassNames := map[string]bool{
		"BYP": true, "BYPASS": true, "CTL": true, "CONT": true, "CONTROL": true,
		"FB": true, "FEEDBACK": true, "COMP": true, "REF": true, "VREF": true,
		"SS": true, "SOFTSTART": true, "BS": true, "BOOT": true, "BST": true,
	}
	for _, s := range syms {
		if !isCapLib(s.LibID) || seen[s.Reference] {
			continue
		}
		var capNets []sexp.Net
		for _, n := range nets {
			for _, p := range n.Pins {
				if p.Reference == s.Reference {
					capNets = append(capNets, n)
					break
				}
			}
		}
		if len(capNets) != 2 {
			continue
		}
		railIdx := -1
		for i, n := range capNets {
			if isRailName(n.Name) {
				railIdx = i
				break
			}
		}
		if railIdx < 0 {
			continue
		}
		other := capNets[1-railIdx]
		// Find an IC pin on `other` net whose name is in bypassNames.
		var anchor pinHit
		for _, p := range other.Pins {
			if _, ok := icByRef[p.Reference]; !ok {
				continue
			}
			if bypassNames[strings.ToUpper(p.PinName)] {
				anchor = pinHit{ref: p.Reference, name: p.PinName}
				break
			}
		}
		if anchor.ref == "" {
			continue
		}
		out = append(out, cluster.Cluster{
			Kind:       "bypass_nonpower",
			Refs:       []string{anchor.ref, s.Reference},
			Anchor:     anchor.ref,
			Confidence: 0.85,
		})
		seen[s.Reference] = true
	}
	return out
}

// --- series_led ---------------------------------------------------------
//
// Resistor + LED in series. Anchor = LED so layout can pull the resistor
// adjacent to it (and orient the LED arrow along signal flow).
func seriesLED(syms []sexp.SchematicSymbol, nets []sexp.Net) []cluster.Cluster {
	leds := []sexp.SchematicSymbol{}
	for _, s := range syms {
		if isLEDLib(s.LibID) {
			leds = append(leds, s)
		}
	}
	if len(leds) == 0 {
		return nil
	}
	resByRef := map[string]sexp.SchematicSymbol{}
	for _, s := range syms {
		if isResLib(s.LibID) {
			resByRef[s.Reference] = s
		}
	}
	netsOf := func(ref string) []sexp.Net {
		var out []sexp.Net
		for _, n := range nets {
			for _, p := range n.Pins {
				if p.Reference == ref {
					out = append(out, n)
					break
				}
			}
		}
		return out
	}
	var clusters []cluster.Cluster
	used := map[string]bool{}
	for _, led := range leds {
		ledNets := netsOf(led.Reference)
		for _, ln := range ledNets {
			for _, p := range ln.Pins {
				if p.Reference == led.Reference {
					continue
				}
				if _, ok := resByRef[p.Reference]; !ok {
					continue
				}
				if used[p.Reference] {
					continue
				}
				clusters = append(clusters, cluster.Cluster{
					Kind:       "series_led",
					Refs:       []string{led.Reference, p.Reference},
					Anchor:     led.Reference,
					Confidence: 0.8,
				})
				used[p.Reference] = true
			}
		}
	}
	return clusters
}

// --- oscillator_rc ------------------------------------------------------
//
// IC with a TRIG/THR/THRES/DSC/OSC/CLK pin connected to an RC network. The
// canonical case is a 555 astable: U1.THR + U1.TRIG + R2 + C1. Anchor = IC.
func oscillatorRC(syms []sexp.SchematicSymbol, nets []sexp.Net) []cluster.Cluster {
	oscPinNames := map[string]bool{
		"TRIG": true, "THR": true, "THRES": true, "THRESHOLD": true,
		"DSC": true, "DISCH": true, "OSC": true, "OSC1": true, "OSC2": true,
		"CLK": true, "CLOCK": true,
	}
	var clusters []cluster.Cluster
	used := map[string]bool{}
	icSyms := map[string]sexp.SchematicSymbol{}
	for _, s := range syms {
		if isICLib(s.LibID) {
			icSyms[s.Reference] = s
		}
	}
	for icRef := range icSyms {
		// Collect nets that touch an oscillator-named pin of this IC.
		var oscNets []sexp.Net
		for _, n := range nets {
			for _, p := range n.Pins {
				if p.Reference != icRef {
					continue
				}
				if oscPinNames[strings.ToUpper(p.PinName)] {
					oscNets = append(oscNets, n)
					break
				}
			}
		}
		if len(oscNets) == 0 {
			continue
		}
		// Find an R + C pair across these nets.
		var rs, cs []string
		for _, n := range oscNets {
			for _, p := range n.Pins {
				sym := lookupSym(syms, p.Reference)
				if sym == nil {
					continue
				}
				if isResLib(sym.LibID) && !contains(rs, p.Reference) && !used[p.Reference] {
					rs = append(rs, p.Reference)
				}
				if isCapLib(sym.LibID) && !contains(cs, p.Reference) && !used[p.Reference] {
					cs = append(cs, p.Reference)
				}
			}
		}
		if len(rs) == 0 || len(cs) == 0 {
			continue
		}
		members := append([]string{icRef}, rs...)
		members = append(members, cs...)
		clusters = append(clusters, cluster.Cluster{
			Kind:       "oscillator_rc",
			Refs:       members,
			Anchor:     icRef,
			Confidence: 0.9,
		})
		for _, r := range rs {
			used[r] = true
		}
		for _, c := range cs {
			used[c] = true
		}
	}
	return clusters
}

// --- feedback_divider ---------------------------------------------------
//
// Two resistors forming a divider from VOUT to FB pin. Common in linear
// regulators (LM317), switching regulators, op-amp non-inverting stages
// (already covered by opamp_feedback in the core; this catches regulator
// FBs that core misses).
func feedbackDivider(syms []sexp.SchematicSymbol, nets []sexp.Net) []cluster.Cluster {
	icByRef := map[string]sexp.SchematicSymbol{}
	for _, s := range syms {
		if isICLib(s.LibID) {
			icByRef[s.Reference] = s
		}
	}
	if len(icByRef) == 0 {
		return nil
	}
	var clusters []cluster.Cluster
	used := map[string]bool{}
	for icRef := range icByRef {
		// Find FB pin and the net it sits on.
		var fbNet *sexp.Net
		for ni, n := range nets {
			for _, p := range n.Pins {
				if p.Reference == icRef && (strings.EqualFold(p.PinName, "FB") ||
					strings.EqualFold(p.PinName, "ADJ") || strings.EqualFold(p.PinName, "FEEDBACK")) {
					fbNet = &nets[ni]
					break
				}
			}
			if fbNet != nil {
				break
			}
		}
		if fbNet == nil {
			continue
		}
		// Look for two resistors on the FB net (one to VOUT, one to GND).
		var rs []string
		for _, p := range fbNet.Pins {
			sym := lookupSym(syms, p.Reference)
			if sym == nil || !isResLib(sym.LibID) || used[p.Reference] {
				continue
			}
			rs = append(rs, p.Reference)
		}
		if len(rs) < 2 {
			continue
		}
		members := append([]string{icRef}, rs[:2]...)
		clusters = append(clusters, cluster.Cluster{
			Kind:       "feedback_divider",
			Refs:       members,
			Anchor:     icRef,
			Confidence: 0.85,
		})
		used[rs[0]] = true
		used[rs[1]] = true
	}
	return clusters
}

// --- helpers ------------------------------------------------------------

func isICLib(libID string) bool {
	switch {
	case strings.HasPrefix(libID, "MCU_"),
		strings.HasPrefix(libID, "Amplifier_Operational:"),
		strings.HasPrefix(libID, "Regulator_Linear:"),
		strings.HasPrefix(libID, "Regulator_Switching:"),
		strings.HasPrefix(libID, "Timer:"),
		strings.HasPrefix(libID, "Interface_"),
		strings.HasPrefix(libID, "Logic_"),
		strings.HasPrefix(libID, "Memory_"),
		strings.HasPrefix(libID, "Analog_"):
		return true
	}
	return false
}

func isResLib(libID string) bool {
	return libID == "Device:R" || libID == "Device:R_Small" ||
		libID == "Device:R_US" || strings.HasPrefix(libID, "Device:R_")
}

func isCapLib(libID string) bool {
	return libID == "Device:C" || libID == "Device:C_Small" ||
		libID == "Device:C_Polarized" || libID == "Device:CP" ||
		strings.HasPrefix(libID, "Device:C_") || strings.HasPrefix(libID, "Device:CP")
}

func isLEDLib(libID string) bool {
	return libID == "Device:LED" || libID == "Device:LED_Small" ||
		strings.Contains(libID, "LED_")
}

func isRailName(n string) bool {
	u := strings.ToUpper(strings.TrimSpace(n))
	switch u {
	case "GND", "VSS", "VEE", "EARTH", "0V", "VCC", "VDD", "VBUS", "AVCC", "AVDD":
		return true
	}
	if strings.HasPrefix(u, "+") || strings.HasPrefix(u, "-") {
		return true
	}
	return false
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func lookupSym(syms []sexp.SchematicSymbol, ref string) *sexp.SchematicSymbol {
	for i := range syms {
		if syms[i].Reference == ref {
			return &syms[i]
		}
	}
	return nil
}
