// Package fplib reads, edits and writes KiCad footprints (.kicad_mod) and the
// .pretty directories that hold them.
//
// Nothing in this repo touched footprints before: the schematic side only ever
// needed the footprint's NAME, as a string to put in a property. Importing a
// part means writing the file itself.
package fplib

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"mcp-kicad/internal/sexp"
)

// FP is a parsed (footprint …) file.
type FP struct {
	root *sexp.Node
}

// Parse reads a .kicad_mod from memory.
func Parse(data []byte) (*FP, error) {
	nodes, err := sexp.Parse(string(data))
	if err != nil {
		return nil, fmt.Errorf("fplib: %w", err)
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("fplib: empty file")
	}
	// KiCad 6 renamed the root from module to footprint and still reads both.
	if h := nodes[0].Head(); h != "footprint" && h != "module" {
		return nil, fmt.Errorf("fplib: root is %q, expected footprint", h)
	}
	return &FP{root: nodes[0]}, nil
}

// Load reads a .kicad_mod from disk.
func Load(path string) (*FP, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("fplib: cannot read %s: %w", path, err)
	}
	fp, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("fplib: %s: %w", path, err)
	}
	return fp, nil
}

// Name returns the footprint name.
func (f *FP) Name() string { return sexp.StringValue(f.root, 1) }

// SetName renames the footprint and syncs the Value property, which KiCad
// keeps equal to the name and shows on the F.Fab layer. Renaming only the
// node leaves every placed instance silk-screened with the old name.
func (f *FP) SetName(name string) {
	old := f.Name()
	if len(f.root.Children) > 1 {
		f.root.Children[1] = sexp.Str(name)
	}
	if v := f.property("Value"); v != nil && len(v.Children) > 2 {
		if sexp.StringValue(v, 2) == old || old == "" {
			v.Children[2] = sexp.Str(name)
		}
	}
}

// Descr returns the footprint description.
func (f *FP) Descr() string {
	if n := sexp.FindList(f.root, "descr"); n != nil {
		return sexp.StringValue(n, 1)
	}
	return ""
}

// PadNumbers returns the distinct pad numbers, sorted. Pads with an empty
// number are mechanical (mounting holes, thermal slugs) and are excluded:
// they have no counterpart on the symbol, and counting them makes every
// symbol-vs-footprint comparison disagree.
func (f *FP) PadNumbers() []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range f.root.Children {
		if c.Head() != "pad" {
			continue
		}
		num := padNumber(c)
		if num == "" || seen[num] {
			continue
		}
		seen[num] = true
		out = append(out, num)
	}
	sort.Strings(out)
	return out
}

// Pad is one pad's identity and placement, in millimetres relative to the
// footprint origin.
type Pad struct {
	Number string
	X, Y   float64
	W, H   float64
	Type   string // "smd", "thru_hole", "np_thru_hole"
	Shape  string // "rect", "oval", "circle", "roundrect", "custom"
}

// Pads returns every pad, in file order. Two independent renderings of the
// same package have to agree on where the copper is, and comparing them is the
// only way to find out whether a converter got its scale right.
func (f *FP) Pads() []Pad {
	var out []Pad
	for _, c := range f.root.Children {
		if c.Head() != "pad" {
			continue
		}
		p := Pad{
			Number: padNumber(c),
			Type:   sexp.AtomValue(c, 2),
			Shape:  sexp.AtomValue(c, 3),
		}
		if at := sexp.FindList(c, "at"); at != nil {
			p.X, p.Y = atof(sexp.AtomValue(at, 1)), atof(sexp.AtomValue(at, 2))
		}
		if size := sexp.FindList(c, "size"); size != nil {
			p.W, p.H = atof(sexp.AtomValue(size, 1)), atof(sexp.AtomValue(size, 2))
		}
		out = append(out, p)
	}
	return out
}

func atof(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return v
}

// MechanicalPads counts pads with no number.
func (f *FP) MechanicalPads() int {
	n := 0
	for _, c := range f.root.Children {
		if c.Head() == "pad" && padNumber(c) == "" {
			n++
		}
	}
	return n
}

// padNumber reads a pad's number. KiCad writes a mechanical pad as (pad "" …),
// a present-but-empty STRING — and Node.Value keeps the quotes for string
// nodes, so reading it as an atom yields the two-character literal `""`
// instead of the empty number it stands for.
func padNumber(pad *sexp.Node) string {
	if len(pad.Children) < 2 {
		return ""
	}
	n := pad.Children[1]
	if n.IsList() {
		return ""
	}
	if n.IsString {
		return sexp.StringValue(pad, 1)
	}
	return n.Value
}

// Model returns the path of the 3D model, or "".
func (f *FP) Model() string {
	if n := sexp.FindList(f.root, "model"); n != nil {
		return sexp.StringValue(n, 1)
	}
	return ""
}

// SetModel points the footprint at a 3D model, replacing any existing
// reference. An empty path removes the model node, which is what a footprint
// imported without a STEP file needs — a dangling reference makes KiCad's 3D
// viewer complain on every open.
func (f *FP) SetModel(path string) {
	for i, c := range f.root.Children {
		if c.Head() == "model" {
			if path == "" {
				f.root.Children = append(f.root.Children[:i], f.root.Children[i+1:]...)
				return
			}
			c.Children[1] = sexp.Str(path)
			return
		}
	}
	if path == "" {
		return
	}
	xyz := func(head string, a, b, c string) *sexp.Node {
		return sexp.List(sexp.Atom(head),
			sexp.List(sexp.Atom("xyz"), sexp.Atom(a), sexp.Atom(b), sexp.Atom(c)))
	}
	f.root.Children = append(f.root.Children, sexp.List(
		sexp.Atom("model"), sexp.Str(path),
		xyz("offset", "0", "0", "0"),
		xyz("scale", "1", "1", "1"),
		xyz("rotate", "0", "0", "0"),
	))
}

// Property returns a footprint property value, or "".
func (f *FP) Property(key string) string {
	if n := f.property(key); n != nil {
		return sexp.StringValue(n, 2)
	}
	return ""
}

// SetProperty sets a footprint property, creating a hidden one when absent.
func (f *FP) SetProperty(key, value string) {
	if n := f.property(key); n != nil && len(n.Children) > 2 {
		n.Children[2] = sexp.Str(value)
		return
	}
	prop := sexp.List(
		sexp.Atom("property"),
		sexp.Str(key),
		sexp.Str(value),
		sexp.List(sexp.Atom("at"), sexp.Atom("0"), sexp.Atom("0"), sexp.Atom("0")),
		sexp.List(sexp.Atom("layer"), sexp.Str("F.Fab")),
		sexp.List(sexp.Atom("hide"), sexp.Atom("yes")),
		sexp.List(sexp.Atom("effects"),
			sexp.List(sexp.Atom("font"),
				sexp.List(sexp.Atom("size"), sexp.Atom("1"), sexp.Atom("1")),
				sexp.List(sexp.Atom("thickness"), sexp.Atom("0.15")))),
	)
	// Properties sit in the header block, after descr/tags and before the
	// first graphic. Children 0 and 1 are the "footprint" atom and the name,
	// which are leaves: appending before them would corrupt the node.
	insertAt := len(f.root.Children)
	for i, c := range f.root.Children {
		if !c.IsList() {
			continue
		}
		switch c.Head() {
		case "version", "generator", "generator_version", "layer", "descr", "tags", "property":
			continue
		}
		insertAt = i
		break
	}
	f.root.Children = append(f.root.Children, nil)
	copy(f.root.Children[insertAt+1:], f.root.Children[insertAt:])
	f.root.Children[insertAt] = prop
}

func (f *FP) property(key string) *sexp.Node {
	for _, c := range f.root.Children {
		if c.Head() == "property" && sexp.StringValue(c, 1) == key {
			return c
		}
	}
	return nil
}

// Bytes serializes the footprint.
func (f *FP) Bytes() []byte {
	return []byte(sexp.Write([]*sexp.Node{f.root}))
}

// Save writes the footprint to path, creating the parent directory.
func (f *FP) Save(path string) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("fplib: cannot create %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, f.Bytes(), 0o644); err != nil {
		return fmt.Errorf("fplib: cannot write %s: %w", path, err)
	}
	return nil
}

// SaveInto writes the footprint into a .pretty directory under its own name,
// returning the path written.
func (f *FP) SaveInto(prettyDir string) (string, error) {
	name := f.Name()
	if name == "" {
		return "", fmt.Errorf("fplib: footprint has no name")
	}
	path := filepath.Join(prettyDir, name+".kicad_mod")
	return path, f.Save(path)
}

// ListPretty returns the footprint names in a .pretty directory, sorted.
func ListPretty(prettyDir string) ([]string, error) {
	entries, err := os.ReadDir(prettyDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("fplib: cannot read %s: %w", prettyDir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".kicad_mod") {
			continue
		}
		out = append(out, strings.TrimSuffix(e.Name(), ".kicad_mod"))
	}
	sort.Strings(out)
	return out, nil
}
