// Package symlib reads, edits and writes KiCad symbol libraries (.kicad_sym).
//
// The repo already parses .kicad_sym for one purpose — pulling a symbol OUT to
// embed it in a schematic. Importing a part needs the other direction: putting
// a symbol IN, under a name we chose, without disturbing what the file already
// holds. Everything here goes through internal/sexp; there is no text editing
// of KiCad files anywhere in this package.
package symlib

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mcp-kicad/internal/sexp"
)

// Lib is a parsed (kicad_symbol_lib …) file.
type Lib struct {
	root *sexp.Node
}

// Version is the file format version stamped into libraries this package
// creates. It matches what KiCad 9 and 10 write; `kicad-cli sym upgrade`
// rewrites it to whatever the installed KiCad prefers, which is exactly why
// the importer runs that step.
const Version = "20241209"

// New returns an empty library.
func New() *Lib {
	return &Lib{root: sexp.List(
		sexp.Atom("kicad_symbol_lib"),
		sexp.List(sexp.Atom("version"), sexp.Atom(Version)),
		sexp.List(sexp.Atom("generator"), sexp.Str("mcp-kicad")),
	)}
}

// Parse reads a .kicad_sym from memory.
func Parse(data []byte) (*Lib, error) {
	nodes, err := sexp.Parse(string(data))
	if err != nil {
		return nil, fmt.Errorf("symlib: %w", err)
	}
	if len(nodes) == 0 || nodes[0].Head() != "kicad_symbol_lib" {
		return nil, fmt.Errorf("symlib: not a kicad_symbol_lib")
	}
	return &Lib{root: nodes[0]}, nil
}

// Load reads a .kicad_sym from disk. A missing file yields an empty library,
// because that is the state the imported library starts in.
func Load(path string) (*Lib, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return New(), nil
		}
		return nil, fmt.Errorf("symlib: cannot read %s: %w", path, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return New(), nil
	}
	lib, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("symlib: %s: %w", path, err)
	}
	return lib, nil
}

// Names returns the top-level symbol names, sorted. Sub-units (Name_N_M) live
// inside their parent node and are not listed.
func (l *Lib) Names() []string {
	var out []string
	for _, c := range l.root.Children[1:] {
		if c.Head() == "symbol" {
			out = append(out, sexp.StringValue(c, 1))
		}
	}
	sort.Strings(out)
	return out
}

// Get returns the symbol node named name.
func (l *Lib) Get(name string) (*sexp.Node, bool) {
	for _, c := range l.root.Children[1:] {
		if c.Head() == "symbol" && sexp.StringValue(c, 1) == name {
			return c, true
		}
	}
	return nil, false
}

// Symbols returns every top-level symbol node, in file order.
func (l *Lib) Symbols() []*sexp.Node {
	var out []*sexp.Node
	for _, c := range l.root.Children[1:] {
		if c.Head() == "symbol" {
			out = append(out, c)
		}
	}
	return out
}

// Flatten returns a self-contained copy of the named symbol with its
// (extends …) chain resolved. Copying a derived symbol without this step
// produces a symbol with no pins.
func (l *Lib) Flatten(name string) (*sexp.Node, error) {
	return sexp.FlattenLibSymbol(l.root.Children[1:], name)
}

// Put inserts sym, replacing any symbol of the same name. Re-importing a part
// must overwrite rather than accumulate duplicates KiCad would then refuse.
func (l *Lib) Put(sym *sexp.Node) {
	name := sexp.StringValue(sym, 1)
	for i, c := range l.root.Children {
		if c.Head() == "symbol" && sexp.StringValue(c, 1) == name {
			l.root.Children[i] = sym
			return
		}
	}
	l.root.Children = append(l.root.Children, sym)
}

// Remove deletes the named symbol. Reports whether it was there.
func (l *Lib) Remove(name string) bool {
	for i, c := range l.root.Children {
		if c.Head() == "symbol" && sexp.StringValue(c, 1) == name {
			l.root.Children = append(l.root.Children[:i], l.root.Children[i+1:]...)
			return true
		}
	}
	return false
}

// Rename renames a symbol node in place. Sub-units are named "<parent>_N_M"
// and KiCad matches them to their parent BY THAT PREFIX, so renaming only the
// parent silently orphans every graphic and pin it owns.
func Rename(sym *sexp.Node, newName string) {
	oldName := sexp.StringValue(sym, 1)
	if oldName == newName || len(sym.Children) < 2 {
		return
	}
	sym.Children[1] = sexp.Str(newName)
	for _, c := range sym.Children[2:] {
		if c.Head() != "symbol" {
			continue
		}
		subName := sexp.StringValue(c, 1)
		if strings.HasPrefix(subName, oldName+"_") {
			c.Children[1] = sexp.Str(newName + subName[len(oldName):])
		}
	}
	// The Value property is what the schematic shows; leaving it on the old
	// name makes every placed instance read as the part we renamed away from.
	if Property(sym, "Value") == oldName {
		SetProperty(sym, "Value", newName)
	}
}

// Bytes serializes the library.
func (l *Lib) Bytes() []byte {
	return []byte(sexp.Write([]*sexp.Node{l.root}))
}

// Save writes the library, creating the parent directory when needed.
func (l *Lib) Save(path string) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("symlib: cannot create %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, l.Bytes(), 0o644); err != nil {
		return fmt.Errorf("symlib: cannot write %s: %w", path, err)
	}
	return nil
}

// Property returns the value of a symbol property, or "".
func Property(sym *sexp.Node, key string) string {
	if n := findProperty(sym, key); n != nil {
		return sexp.StringValue(n, 2)
	}
	return ""
}

// SetProperty sets a symbol property, creating it when absent.
//
// A created property is hidden and parked at the origin: it carries metadata
// (a source URL, a licence) that belongs in the file but not on the drawing,
// and an unhidden field there would land on top of the symbol body.
func SetProperty(sym *sexp.Node, key, value string) {
	if n := findProperty(sym, key); n != nil {
		if len(n.Children) > 2 {
			n.Children[2] = sexp.Str(value)
			return
		}
	}
	prop := sexp.List(
		sexp.Atom("property"),
		sexp.Str(key),
		sexp.Str(value),
		sexp.List(sexp.Atom("at"), sexp.Atom("0"), sexp.Atom("0"), sexp.Atom("0")),
		sexp.List(sexp.Atom("hide"), sexp.Atom("yes")),
		sexp.List(sexp.Atom("effects"),
			sexp.List(sexp.Atom("font"),
				sexp.List(sexp.Atom("size"), sexp.Atom("1.27"), sexp.Atom("1.27")))),
	)
	// Properties come before the sub-unit nodes in every library KiCad writes;
	// inserting after them parses fine but diffs badly against KiCad's own
	// output the first time the GUI touches the file.
	insertAt := len(sym.Children)
	for i, c := range sym.Children {
		if c.Head() == "symbol" {
			insertAt = i
			break
		}
	}
	sym.Children = append(sym.Children, nil)
	copy(sym.Children[insertAt+1:], sym.Children[insertAt:])
	sym.Children[insertAt] = prop
}

func findProperty(sym *sexp.Node, key string) *sexp.Node {
	for _, c := range sym.Children {
		if c.Head() == "property" && sexp.StringValue(c, 1) == key {
			return c
		}
	}
	return nil
}

// PinNumbers returns the electrical pin numbers of a symbol, deduplicated and
// sorted. Pins live inside the sub-unit nodes, so a caller that only walks the
// top level finds none.
func PinNumbers(sym *sexp.Node) []string {
	seen := map[string]bool{}
	var out []string
	var walk func(n *sexp.Node)
	walk = func(n *sexp.Node) {
		for _, c := range n.Children {
			if !c.IsList() {
				continue
			}
			if c.Head() == "pin" {
				if num := sexp.FindList(c, "number"); num != nil {
					v := sexp.StringValue(num, 1)
					if v != "" && !seen[v] {
						seen[v] = true
						out = append(out, v)
					}
				}
				continue
			}
			walk(c)
		}
	}
	walk(sym)
	sort.Strings(out)
	return out
}
