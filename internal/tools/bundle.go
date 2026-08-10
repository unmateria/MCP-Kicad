package tools

import (
	"fmt"
	"sort"
	"strings"

	"mcp-kicad/internal/compile"
	"mcp-kicad/internal/place2/gate"
	"mcp-kicad/internal/place2/metrics"
	"mcp-kicad/internal/sexp"
)

// bundleUniformity makes the nets fanning out of one CONNECTOR read the same
// way. Four control lines leaving J1 came out as one wired (a long lasso
// around the sheet) and three labeled, because the first route fit and walled
// the rest in — and no human draws one line of a bundle with wire and its
// twins with tags. When the labeled members of a connector's bundle equal or
// outnumber the wired ones, the wired minority is demoted to labels too.
//
// Only connectors anchor a bundle. An MCU also shares many 2-pin nets, but
// its fan-outs go everywhere on the sheet and are not one physical thing; a
// connector's pins are.
//
// Wires can only be taken away here, never added, so this must run BEFORE
// the gate like everything that changes geometry.
func bundleUniformity(sch *sexp.Schematic, d *compile.Design) []string {
	libOf := map[string]string{}
	for _, s := range sexp.ReadSymbols(sch) {
		libOf[s.Reference] = s.LibID
	}

	// Which nets own at least one wire, by tracing the finished sheet.
	netOf := sexp.TracePointNets(sch)
	wired := map[string]bool{}
	for _, w := range sch.Wires() {
		ax, ay, _, _, ok := metrics.WireCoords(w)
		if !ok {
			continue
		}
		if n := netOf[[2]float64{sexp.Round2(ax), sexp.Round2(ay)}]; n != "" {
			wired[n] = true
		}
	}

	// Bundle members: declared 2-pin, non-power nets touching the connector.
	members := map[string][]string{} // connector ref -> net names
	netNames := make([]string, 0, len(d.Nets))
	for name := range d.Nets {
		netNames = append(netNames, name)
	}
	sort.Strings(netNames)
	for _, name := range netNames {
		pins := d.Nets[name]
		if len(pins) != 2 || netNameToPowerLibID(name) != "" {
			continue
		}
		if _, isPwr := d.PowerNets[name]; isPwr {
			continue
		}
		for _, pin := range pins {
			ref, _, ok := strings.Cut(pin, ".")
			if !ok {
				continue
			}
			if strings.HasPrefix(libOf[ref], "Connector") {
				members[ref] = append(members[ref], name)
			}
		}
	}

	refs := make([]string, 0, len(members))
	for ref := range members {
		refs = append(refs, ref)
	}
	sort.Strings(refs)

	var demoted []string
	for _, ref := range refs {
		nets := members[ref]
		if len(nets) < 2 {
			continue
		}
		var w, l []string
		for _, n := range nets {
			if wired[n] {
				w = append(w, n)
			} else {
				l = append(l, n)
			}
		}
		if len(w) == 0 || len(l) < len(w) {
			continue // all wired, or wires are the majority: leave it
		}
		for _, n := range w {
			gate.Demote(sch, n)
			demoted = append(demoted, fmt.Sprintf("%s (bundle of %s: %d labeled vs %d wired)", n, ref, len(l), len(w)))
		}
	}
	return demoted
}
