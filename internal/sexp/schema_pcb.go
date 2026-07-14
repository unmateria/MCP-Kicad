package sexp

import "fmt"

// PCB represents a parsed .kicad_pcb file.
type PCB struct {
	root *Node // top-level (kicad_pcb ...) node
}

// ParsePCB parses a .kicad_pcb file content.
func ParsePCB(content string) (*PCB, error) {
	nodes, err := Parse(content)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("sexp: empty PCB file")
	}
	root := nodes[0]
	if root.Head() != "kicad_pcb" {
		return nil, fmt.Errorf("sexp: expected kicad_pcb, got %q", root.Head())
	}
	return &PCB{root: root}, nil
}

// Serialize writes the PCB back to its S-expression text form.
func (p *PCB) Serialize() string {
	return Write([]*Node{p.root})
}

// Root returns the raw AST root node for direct manipulation.
func (p *PCB) Root() *Node { return p.root }

// Version returns the KiCad PCB format version.
func (p *PCB) Version() string {
	n := FindList(p.root, "version")
	if n == nil {
		return ""
	}
	return AtomValue(n, 1)
}

// Footprints returns all (footprint ...) nodes at the top level.
func (p *PCB) Footprints() []*Node {
	return FindAllLists(p.root, "footprint")
}

// GrLines returns all (gr_line ...) nodes (graphic lines, used for Edge.Cuts etc.).
func (p *PCB) GrLines() []*Node {
	return FindAllLists(p.root, "gr_line")
}

// AddFootprint appends a footprint node to the PCB.
func (p *PCB) AddFootprint(fpNode *Node) {
	p.root.Children = append(p.root.Children, fpNode)
}

// AddGrLine appends a graphic line to the PCB.
func (p *PCB) AddGrLine(lineNode *Node) {
	p.root.Children = append(p.root.Children, lineNode)
}

// MoveFootprint sets the position and rotation of the first footprint
// whose reference matches ref.
// Returns an error if no matching footprint is found.
func (p *PCB) MoveFootprint(ref string, x, y, angle float64) error {
	for _, fp := range p.Footprints() {
		if footprintRef(fp) == ref {
			setAt(fp, x, y, angle)
			return nil
		}
	}
	return fmt.Errorf("pcb: footprint %q not found", ref)
}

// footprintRef extracts the reference designator from a footprint node.
func footprintRef(fp *Node) string {
	for _, child := range fp.Children {
		if child.IsList() && child.Head() == "property" {
			// (property "Reference" "R1" ...)
			if len(child.Children) >= 3 && StringValue(child, 1) == "Reference" {
				return StringValue(child, 2)
			}
		}
	}
	return ""
}

// setAt updates or creates the (at x y angle) node inside parent.
func setAt(parent *Node, x, y, angle float64) {
	atNode := FindList(parent, "at")
	newAt := List(
		Atom("at"),
		Atom(fmt.Sprintf("%.6g", x)),
		Atom(fmt.Sprintf("%.6g", y)),
		Atom(fmt.Sprintf("%.6g", angle)),
	)
	if atNode == nil {
		parent.Children = append(parent.Children, newAt)
		return
	}
	// Replace existing at node.
	for i, child := range parent.Children {
		if child.IsList() && child.Head() == "at" {
			parent.Children[i] = newAt
			return
		}
	}
}

// NewEdgeCutsRect creates four gr_line nodes on Edge.Cuts forming a rectangle.
// x, y are the top-left corner; w and h are width and height in mm.
func NewEdgeCutsRect(x, y, w, h float64) []*Node {
	return []*Node{
		newGrLine(x, y, x+w, y),
		newGrLine(x+w, y, x+w, y+h),
		newGrLine(x+w, y+h, x, y+h),
		newGrLine(x, y+h, x, y),
	}
}

func newGrLine(x1, y1, x2, y2 float64) *Node {
	return List(
		Atom("gr_line"),
		List(Atom("start"), Atom(fmt.Sprintf("%.6g", x1)), Atom(fmt.Sprintf("%.6g", y1))),
		List(Atom("end"), Atom(fmt.Sprintf("%.6g", x2)), Atom(fmt.Sprintf("%.6g", y2))),
		List(Atom("stroke"), List(Atom("width"), Atom("0.05")), List(Atom("type"), Atom("default"))),
		List(Atom("layer"), Str("Edge.Cuts")),
	)
}
