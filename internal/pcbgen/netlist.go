// Package pcbgen builds a .kicad_pcb from a compiled schematic: every
// footprint instantiated with its pads on the right nets and linked back to
// its symbol, placed without overlaps inside a board outline. Routing is left
// to the user (or a later pass); the ratsnest shows what remains.
//
// The connectivity comes from the netlist KiCad itself exports, never from
// this repository's own tracer: it is the exact data KiCad's "Update PCB from
// Schematic" consumes, so a board built from it agrees with KiCad by
// construction, and `kicad-cli pcb drc --schematic-parity` checks that it did.
package pcbgen

import (
	"fmt"

	"mcp-kicad/internal/sexp"
)

// Component is one part of the netlist.
type Component struct {
	Ref, Value, Footprint string
	// Path is the board footprint's link to its schematic symbol:
	// sheet tstamps + symbol UUID, e.g. "/1f1c7f56-…". It is what makes
	// KiCad treat the footprint as that symbol's instead of a stray part.
	Path      string
	SheetName string
	SheetFile string
	// UUIDs are the schematic symbol UUIDs of every unit, first unit first.
	UUIDs []string
}

// Node is one pin on a net.
type Node struct {
	Ref, Pin, Function, Type string
}

// Net is a named list of pins.
type Net struct {
	Name  string
	Nodes []Node
}

// Netlist is the subset of KiCad's kicadsexpr netlist a board needs.
type Netlist struct {
	Components []Component
	Nets       []Net
}

// ParseNetlist reads the output of `kicad-cli sch export netlist`
// (the default kicadsexpr format).
func ParseNetlist(data string) (*Netlist, error) {
	nodes, err := sexp.Parse(data)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 || nodes[0].Head() != "export" {
		return nil, fmt.Errorf("pcbgen: not a KiCad netlist (root is not export)")
	}
	root := nodes[0]
	nl := &Netlist{}
	if comps := sexp.FindList(root, "components"); comps != nil {
		for _, c := range sexp.FindAllLists(comps, "comp") {
			comp := Component{
				Ref:       listString(c, "ref"),
				Value:     listString(c, "value"),
				Footprint: listString(c, "footprint"),
			}
			sheetTstamps := "/"
			if sp := sexp.FindList(c, "sheetpath"); sp != nil {
				sheetTstamps = listString(sp, "tstamps")
			}
			if ts := sexp.FindList(c, "tstamps"); ts != nil {
				for i := 1; i < len(ts.Children); i++ {
					comp.UUIDs = append(comp.UUIDs, sexp.StringValue(ts, i))
				}
			}
			if len(comp.UUIDs) > 0 {
				comp.Path = sheetTstamps + comp.UUIDs[0]
			}
			for _, p := range sexp.FindAllLists(c, "property") {
				switch listString(p, "name") {
				case "Sheetname":
					comp.SheetName = listString(p, "value")
				case "Sheetfile":
					comp.SheetFile = listString(p, "value")
				}
			}
			nl.Components = append(nl.Components, comp)
		}
	}
	if nets := sexp.FindList(root, "nets"); nets != nil {
		for _, n := range sexp.FindAllLists(nets, "net") {
			net := Net{Name: listString(n, "name")}
			for _, nd := range sexp.FindAllLists(n, "node") {
				net.Nodes = append(net.Nodes, Node{
					Ref:      listString(nd, "ref"),
					Pin:      listString(nd, "pin"),
					Function: listString(nd, "pinfunction"),
					Type:     listString(nd, "pintype"),
				})
			}
			nl.Nets = append(nl.Nets, net)
		}
	}
	return nl, nil
}

// PadNets maps "REF\x00pin" to the net and node that pin is on.
func (nl *Netlist) PadNets() map[string]PadNet {
	out := map[string]PadNet{}
	for _, n := range nl.Nets {
		for _, nd := range n.Nodes {
			out[nd.Ref+"\x00"+nd.Pin] = PadNet{Net: n.Name, Function: nd.Function, Type: nd.Type}
		}
	}
	return out
}

// PadNet is what a pad needs to know about the pin it implements.
type PadNet struct {
	Net, Function, Type string
}

func listString(parent *sexp.Node, name string) string {
	if l := sexp.FindList(parent, name); l != nil {
		return sexp.StringValue(l, 1)
	}
	return ""
}
