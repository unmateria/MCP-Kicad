package elk

import (
	"strings"

	"mcp-kicad/internal/sexp"
)

// Graph is the JSON shape we send to elkjs. It's a tiny subset of the
// full ELK JSON schema, just what we need for placement.
type Graph struct {
	ID            string            `json:"id"`
	LayoutOptions map[string]string `json:"layoutOptions"`
	Children      []Node            `json:"children"`
	Edges         []Edge            `json:"edges"`
}

// Node is one ELK node — either a leaf symbol or a compound cluster.
type Node struct {
	ID            string            `json:"id"`
	Width         float64           `json:"width"`
	Height        float64           `json:"height"`
	Children      []Node            `json:"children,omitempty"`
	LayoutOptions map[string]string `json:"layoutOptions,omitempty"`
	X             float64           `json:"x,omitempty"`
	Y             float64           `json:"y,omitempty"`
}

// Edge is one ELK edge between two node IDs.
type Edge struct {
	ID      string   `json:"id"`
	Sources []string `json:"sources"`
	Targets []string `json:"targets"`
}

// ClusterSpec is the place2.Cluster shape; redeclared here so this package
// stays import-cycle free relative to place2.
type ClusterSpec struct {
	Kind   string
	Refs   []string
	Anchor string
}

// BuildGraph constructs an ELK-compatible graph from a placed-symbol snapshot.
//
//   - Power symbols are filtered out — ELK shouldn't lay out implicit-net
//     symbols; the rules pass handles them after layout.
//   - Cluster anchors become compound parents with their satellites as
//     children. ELK will keep cluster nodes physically close.
//   - Each net produces one edge connecting the first two symbols on it.
//     Multi-pin nets are linearised as (pin0 → pin1, pin1 → pin2, …) so the
//     layered algorithm has a clear flow direction.
//
// The returned Graph carries default layout options that bias placement
// left-to-right with port constraints honoured.
func BuildGraph(syms []sexp.SchematicSymbol, nets []sexp.Net, clusters []ClusterSpec) Graph {
	powerRefs := make(map[string]bool)
	leafByRef := make(map[string]Node)
	for _, s := range syms {
		if strings.HasPrefix(s.LibID, "power:") || s.LibID == "Device:PWR_FLAG" {
			powerRefs[s.Reference] = true
			continue
		}
		w, h := SymbolSize(s.LibID)
		leafByRef[s.Reference] = Node{ID: s.Reference, Width: w, Height: h}
	}

	// Group children by cluster anchor.
	memberOf := make(map[string]string) // ref → cluster anchor (first claim wins)
	for _, c := range clusters {
		for _, r := range c.Refs {
			if _, ok := memberOf[r]; ok {
				continue
			}
			memberOf[r] = c.Anchor
		}
	}

	var topLevel []Node
	parents := make(map[string]Node)
	for _, c := range clusters {
		if _, ok := leafByRef[c.Anchor]; !ok {
			continue
		}
		// A compound parent that contains the anchor and its satellites.
		var children []Node
		for _, r := range c.Refs {
			if leaf, ok := leafByRef[r]; ok {
				children = append(children, leaf)
			}
		}
		if len(children) <= 1 {
			continue
		}
		parent := Node{
			ID:       "cluster_" + c.Anchor,
			Children: children,
			LayoutOptions: map[string]string{
				"elk.algorithm":          "layered",
				"elk.direction":          "RIGHT",
				"elk.padding":            "[top=5, left=5, bottom=5, right=5]",
				"elk.spacing.nodeNode":   "5.08",
			},
		}
		parents[c.Anchor] = parent
	}

	for ref, leaf := range leafByRef {
		if anchor, ok := memberOf[ref]; ok && anchor != ref {
			// Child of a cluster — already inside its parent.
			continue
		}
		if parent, isAnchor := parents[ref]; isAnchor {
			topLevel = append(topLevel, parent)
			continue
		}
		topLevel = append(topLevel, leaf)
	}

	// Edges: linearise each net.
	var edges []Edge
	for i, n := range nets {
		if n.Dangling || len(n.Pins) < 2 {
			continue
		}
		var refs []string
		seen := make(map[string]bool)
		for _, p := range n.Pins {
			if powerRefs[p.Reference] || seen[p.Reference] {
				continue
			}
			seen[p.Reference] = true
			refs = append(refs, p.Reference)
		}
		if len(refs) < 2 {
			continue
		}
		// Translate cluster-internal refs to their compound parent so ELK
		// sees the inter-cluster connectivity at the top level.
		mapID := func(r string) string {
			if anchor, ok := memberOf[r]; ok && anchor != r {
				if _, has := parents[anchor]; has {
					return "cluster_" + anchor
				}
			}
			return r
		}
		for j := 1; j < len(refs); j++ {
			a, b := mapID(refs[j-1]), mapID(refs[j])
			if a == b {
				continue
			}
			edges = append(edges, Edge{
				ID:      "e" + itoa(i) + "_" + itoa(j),
				Sources: []string{a},
				Targets: []string{b},
			})
		}
	}

	return Graph{
		ID: "root",
		LayoutOptions: map[string]string{
			"elk.algorithm":            "layered",
			"elk.direction":            "RIGHT",
			"elk.spacing.nodeNode":     "10.16",
			"elk.layered.spacing.nodeNodeBetweenLayers": "15.24",
			"elk.randomSeed":           "1",
		},
		Children: topLevel,
		Edges:    edges,
	}
}

// ResultPositions takes the ELK-laid-out graph and recovers absolute
// (x, y) positions for every leaf symbol in mm. Compound nodes are
// flattened: each child is offset by its parent's coordinates so the
// final positions are absolute.
func ResultPositions(g Graph, anchorSyms []sexp.SchematicSymbol) map[string][2]float64 {
	const originX = 50.8
	const originY = 50.8
	out := make(map[string][2]float64)
	walk(g.Children, originX, originY, out)

	// ELK gives the top-left of each node bbox; KiCad symbols are anchored at
	// the symbol body centre. Shift by half the size to compensate.
	for ref, pos := range out {
		for _, s := range anchorSyms {
			if s.Reference != ref {
				continue
			}
			w, h := SymbolSize(s.LibID)
			out[ref] = [2]float64{
				sexp.SnapGrid(pos[0] + w/2),
				sexp.SnapGrid(pos[1] + h/2),
			}
			break
		}
	}
	return out
}

func walk(nodes []Node, dx, dy float64, out map[string][2]float64) {
	for _, n := range nodes {
		x := n.X + dx
		y := n.Y + dy
		if len(n.Children) == 0 {
			out[n.ID] = [2]float64{x, y}
			continue
		}
		walk(n.Children, x, y, out)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	if n < 0 {
		return "-" + itoa(-n)
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
