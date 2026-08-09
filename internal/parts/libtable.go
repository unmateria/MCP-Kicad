package parts

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"mcp-kicad/internal/sexp"
)

// LibTableEntry is one (lib …) row of a sym-lib-table or fp-lib-table.
type LibTableEntry struct {
	Name    string
	Type    string // "KiCad" or "Legacy"
	URI     string
	Options string
	Descr   string
}

// tableHead returns the root node name a table file must have. KiCad rejects a
// fp-lib-table whose root says sym_lib_table, and the previous implementation
// wrote that header into every file it created regardless of destination.
func tableHead(path string) string {
	if strings.Contains(strings.ToLower(filepath.Base(path)), "fp-lib-table") {
		return "fp_lib_table"
	}
	return "sym_lib_table"
}

// ReadLibTable parses a lib table file. A missing file is not an error: it
// yields an empty table, which is what RegisterLibrary then creates.
func ReadLibTable(path string) ([]LibTableEntry, error) {
	root, err := readLibTableRoot(path)
	if err != nil {
		return nil, err
	}
	var out []LibTableEntry
	for _, c := range root.Children[1:] {
		if c.Head() != "lib" {
			continue
		}
		out = append(out, entryFromNode(c))
	}
	return out, nil
}

// RegisterLibrary inserts entry into the table file at path, creating the file
// (with the header its name demands) when it does not exist.
//
// An entry whose name already exists is updated in place when its URI differs
// and left alone when it does not, so re-running an import is a no-op rather
// than a duplicate row KiCad would complain about.
func RegisterLibrary(path string, entry LibTableEntry) (changed bool, err error) {
	if entry.Name == "" || entry.URI == "" {
		return false, fmt.Errorf("libtable: name and uri are required")
	}
	if entry.Type == "" {
		entry.Type = "KiCad"
	}
	root, err := readLibTableRoot(path)
	if err != nil {
		return false, err
	}

	for _, c := range root.Children[1:] {
		if c.Head() != "lib" {
			continue
		}
		existing := entryFromNode(c)
		if existing.Name != entry.Name {
			continue
		}
		if existing.URI == entry.URI && existing.Type == entry.Type {
			return false, nil // already registered, identical
		}
		*c = *nodeFromEntry(entry)
		return true, writeLibTable(path, root)
	}

	root.Children = append(root.Children, nodeFromEntry(entry))
	return true, writeLibTable(path, root)
}

func readLibTableRoot(path string) (*sexp.Node, error) {
	head := tableHead(path)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return sexp.List(sexp.Atom(head)), nil
		}
		return nil, fmt.Errorf("libtable: cannot read %s: %w", path, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return sexp.List(sexp.Atom(head)), nil
	}
	nodes, err := sexp.Parse(string(data))
	if err != nil {
		return nil, fmt.Errorf("libtable: cannot parse %s: %w", path, err)
	}
	if len(nodes) == 0 || !nodes[0].IsList() {
		return nil, fmt.Errorf("libtable: %s is not a library table", path)
	}
	root := nodes[0]
	if h := root.Head(); h != "sym_lib_table" && h != "fp_lib_table" {
		return nil, fmt.Errorf("libtable: %s has root %q, expected sym_lib_table or fp_lib_table", path, h)
	}
	return root, nil
}

func writeLibTable(path string, root *sexp.Node) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("libtable: cannot create %s: %w", dir, err)
		}
	}
	return os.WriteFile(path, []byte(sexp.Write([]*sexp.Node{root})), 0o644)
}

func entryFromNode(n *sexp.Node) LibTableEntry {
	field := func(name string) string {
		if sub := sexp.FindList(n, name); sub != nil {
			return sexp.StringValue(sub, 1)
		}
		return ""
	}
	return LibTableEntry{
		Name:    field("name"),
		Type:    field("type"),
		URI:     field("uri"),
		Options: field("options"),
		Descr:   field("descr"),
	}
}

func nodeFromEntry(e LibTableEntry) *sexp.Node {
	kv := func(k, v string) *sexp.Node {
		return sexp.List(sexp.Atom(k), sexp.Str(v))
	}
	return sexp.List(
		sexp.Atom("lib"),
		kv("name", e.Name),
		kv("type", e.Type),
		kv("uri", e.URI),
		kv("options", e.Options),
		kv("descr", e.Descr),
	)
}

// GlobalTableDir returns KiCad's per-user configuration directory — the one
// holding sym-lib-table and fp-lib-table, which is what the GUI reads.
//
// versionHint is a path inside a KiCad installation (kicad-cli works); its
// version segment picks the matching config directory. When it names nothing
// usable, the highest-numbered directory that actually holds a sym-lib-table
// wins, because a machine with 9.0 and 10.0 installed has both.
func GlobalTableDir(versionHint string) (string, error) {
	base, err := kicadConfigBase()
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return "", fmt.Errorf("libtable: cannot read KiCad config dir %s: %w", base, err)
	}
	var versions []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if fileExists(filepath.Join(base, e.Name(), "sym-lib-table")) {
			versions = append(versions, e.Name())
		}
	}
	if len(versions) == 0 {
		return "", fmt.Errorf("libtable: no KiCad configuration found under %s", base)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(versions)))

	if v := versionFromPath(versionHint); v != "" {
		for _, cand := range versions {
			if cand == v {
				return filepath.Join(base, cand), nil
			}
		}
	}
	return filepath.Join(base, versions[0]), nil
}

func kicadConfigBase() (string, error) {
	switch runtime.GOOS {
	case "windows":
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			return filepath.Join(appdata, "kicad"), nil
		}
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Preferences", "kicad"), nil
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "kicad"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "kicad"), nil
}

// versionFromPath pulls a "<major>.<minor>" segment out of an installation
// path, e.g. C:\Program Files\KiCad\10.0\bin\kicad-cli.exe → "10.0".
func versionFromPath(p string) string {
	if p == "" {
		return ""
	}
	for _, seg := range strings.FieldsFunc(filepath.ToSlash(p), func(r rune) bool { return r == '/' }) {
		if strings.Count(seg, ".") != 1 {
			continue
		}
		major, minor, _ := strings.Cut(seg, ".")
		if major != "" && minor != "" && isDigits(major) && isDigits(minor) {
			return seg
		}
	}
	return ""
}
