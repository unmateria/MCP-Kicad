package compile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
)

var (
	validSheets = []string{"", "auto", "A4", "A3"}
	validDirs   = []string{"left", "right", "up", "down"}
	validRots   = []int{0, 90, 180, 270}
)

// ParseDesign decodes a .design.json source and validates it against the v1
// format specification. Every validation problem is reported: the returned
// error joins all of them in document order.
func ParseDesign(data []byte) (*Design, error) {
	var d Design
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("design: %w", err)
	}
	if err := d.validate(); err != nil {
		return nil, err
	}
	return &d, nil
}

// ParseDesignFile reads and parses a .design.json source from disk.
func ParseDesignFile(path string) (*Design, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	d, err := ParseDesign(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return d, nil
}

// UnmarshalJSON accepts the two shapes the format allows for no_connect:
// an array of explicit "REF.pin" entries, or an object mapping a reference to
// the literal "unused". The two shapes cannot be mixed in the same field.
func (nc *NoConnect) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		*nc = NoConnect{}
		return nil
	}
	switch trimmed[0] {
	case '[':
		var pins []string
		if err := json.Unmarshal(trimmed, &pins); err != nil {
			return fmt.Errorf("no_connect: %w", err)
		}
		*nc = NoConnect{Pins: pins}
		return nil
	case '{':
		var marks map[string]string
		if err := json.Unmarshal(trimmed, &marks); err != nil {
			return fmt.Errorf("no_connect: %w", err)
		}
		refs := sortedKeys(marks)
		for _, ref := range refs {
			if marks[ref] != "unused" {
				return fmt.Errorf("no_connect: reference %q: the only accepted value is %q, got %q", ref, "unused", marks[ref])
			}
		}
		*nc = NoConnect{Unused: refs}
		return nil
	}
	return errors.New(`no_connect: must be an array of "REF.pin" entries or an object mapping a reference to "unused"`)
}

// validator accumulates validation errors and the cross-references needed to
// check nets, power_nets and no_connect once every block has been walked.
type validator struct {
	errs     []error
	refBlock map[string]string       // reference -> label of the block that declared it
	tmplRef  map[string]bool         // references owned by template blocks
	refLib   map[string]string       // explicit reference -> lib_id (units must agree)
	refUnits map[string]map[int]bool // explicit reference -> units already declared
	pinNet   map[string]string       // "REF.pin" -> name of the net that claims it
}

func (v *validator) errorf(format string, args ...any) {
	v.errs = append(v.errs, fmt.Errorf(format, args...))
}

func (d *Design) validate() error {
	v := &validator{
		refBlock: make(map[string]string),
		tmplRef:  make(map[string]bool),
		refLib:   make(map[string]string),
		refUnits: make(map[string]map[int]bool),
		pinNet:   make(map[string]string),
	}

	if d.Version != 1 {
		v.errorf("version: unsupported version %d; this parser only understands version 1", d.Version)
	}
	switch {
	case d.Project == "":
		v.errorf("project: must not be empty")
	case strings.ContainsAny(d.Project, `/\`):
		v.errorf("project %q: must not contain path separators", d.Project)
	}
	if !slices.Contains(validSheets, d.Sheet) {
		v.errorf(`sheet %q: must be "auto", "A4", "A3" or absent`, d.Sheet)
	}

	if len(d.Blocks) == 0 {
		v.errorf("blocks: at least one block is required")
	}
	seenBlock := make(map[string]bool, len(d.Blocks))
	for i := range d.Blocks {
		b := &d.Blocks[i]
		label := blockLabel(i, b.Name)
		switch {
		case b.Name == "":
			v.errorf("%s: name must not be empty", label)
		case seenBlock[b.Name]:
			v.errorf("%s: duplicate block name", label)
		default:
			seenBlock[b.Name] = true
		}

		isTemplate := b.Template != ""
		hasSymbols := len(b.Symbols) > 0
		switch {
		case isTemplate && hasSymbols:
			v.errorf(`%s: has both "template" and "symbols"; exactly one is allowed`, label)
		case !isTemplate && !hasSymbols:
			v.errorf(`%s: needs either "template" or "symbols"`, label)
		}
		if isTemplate {
			v.templateBlock(b, label)
		}
		if hasSymbols {
			v.explicitBlock(b, label)
		}
	}

	v.arrange(d, seenBlock)
	v.nets(d)
	v.powerNets(d)
	v.noConnect(d)
	v.labelNets(d)

	return errors.Join(v.errs...)
}

// labelNets rejects entries that name no declared net (a typo would silently
// change nothing) and power nets (they are never routed anyway).
func (v *validator) labelNets(d *Design) {
	seen := make(map[string]bool, len(d.LabelNets))
	for _, name := range d.LabelNets {
		switch {
		case name == "":
			v.errorf("label_nets: net name must not be empty")
		case seen[name]:
			v.errorf("label_nets: %q is listed more than once", name)
		default:
			seen[name] = true
			if _, ok := d.Nets[name]; !ok {
				v.errorf("label_nets: %q is not a declared net", name)
			}
			if _, pwr := d.PowerNets[name]; pwr {
				v.errorf("label_nets: %q is a power net — power nets are never routed, listing it here changes nothing", name)
			}
		}
	}
}

// templateBlock checks the shape of refs/connect. Whether the template name
// exists is decided by the integration layer, which owns the template library.
func (v *validator) templateBlock(b *Block, label string) {
	for _, role := range sortedKeys(b.Refs) {
		ref := b.Refs[role]
		switch {
		case role == "":
			v.errorf("%s: refs has an empty role name", label)
		case ref == "":
			v.errorf("%s: refs role %q maps to an empty reference", label, role)
		default:
			v.declareTemplateRef(ref, label)
		}
	}
	for _, port := range sortedKeys(b.Connect) {
		switch {
		case port == "":
			v.errorf("%s: connect has an empty port name", label)
		case b.Connect[port] == "":
			v.errorf("%s: connect port %q maps to an empty net", label, port)
		}
	}
}

// explicitBlock checks the anchor tree: the first symbol is the block anchor
// and every later symbol hangs off a symbol declared before it in this block.
func (v *validator) explicitBlock(b *Block, label string) {
	declared := make(map[string]bool, len(b.Symbols))
	for i := range b.Symbols {
		s := &b.Symbols[i]
		sym := fmt.Sprintf("%s: symbol %s", label, symbolLabel(i, s.Ref))

		if s.Ref == "" {
			v.errorf("%s: ref must not be empty", sym)
		}
		switch {
		case s.Lib == "":
			v.errorf("%s: lib must not be empty", sym)
		case !validLibID(s.Lib):
			v.errorf("%s: lib %q must have the form \"Library:Name\"", sym, s.Lib)
		}
		if s.Rot != nil && !slices.Contains(validRots, *s.Rot) {
			v.errorf("%s: rot %d must be 0, 90, 180 or 270", sym, *s.Rot)
		}
		if s.Unit < 0 {
			v.errorf("%s: unit %d must be >= 0", sym, s.Unit)
		}

		switch {
		case i == 0 && s.Place != nil:
			v.errorf(`%s: is the block anchor and must not have "place"`, sym)
		case i > 0 && s.Place == nil:
			v.errorf(`%s: needs "place"; only the first symbol of a block may omit it`, sym)
		case i > 0:
			v.place(s.Place, sym, label, declared)
		}

		if s.Ref != "" {
			v.declareUnit(s.Ref, s.Lib, s.Unit, label)
			declared[s.Ref] = true
		}
	}
}

func (v *validator) place(p *Place, sym, label string, declared map[string]bool) {
	if p.Pin == "" {
		v.errorf("%s: place.pin must not be empty", sym)
	}
	if !slices.Contains(validDirs, p.Dir) {
		v.errorf("%s: place.dir %q must be left, right, up or down", sym, p.Dir)
	}
	if p.Cells < 1 {
		v.errorf("%s: place.cells %d must be >= 1", sym, p.Cells)
	}
	target, _, ok := splitRefPin(p.At)
	switch {
	case !ok:
		v.errorf("%s: place.at %q must have the form \"REF.pin\"", sym, p.At)
	case !declared[target]:
		v.errorf("%s: place.at %q anchors to %q, which is not a symbol declared earlier in %s", sym, p.At, target, label)
	}
}

// declareTemplateRef claims a reference for a template block. Template refs
// are indivisible: any reuse, template or explicit, is an error.
func (v *validator) declareTemplateRef(ref, label string) {
	if owner, taken := v.refBlock[ref]; taken {
		v.errorf("%s: reference %q is already used by %s", label, ref, owner)
		return
	}
	v.refBlock[ref] = label
	v.tmplRef[ref] = true
}

// declareUnit claims one unit of an explicit symbol reference. The same
// reference may be declared several times — one Symbol per unit of a
// multi-unit part — as long as the units differ and the lib matches.
func (v *validator) declareUnit(ref, lib string, unit int, label string) {
	if v.tmplRef[ref] {
		v.errorf("%s: reference %q is already used by %s", label, ref, v.refBlock[ref])
		return
	}
	if unit < 1 {
		unit = 1
	}
	if prevLib, seen := v.refLib[ref]; seen && lib != "" && prevLib != lib {
		v.errorf("%s: reference %q is declared with lib %q but was %q earlier", label, ref, lib, prevLib)
	}
	if v.refUnits[ref] == nil {
		v.refUnits[ref] = make(map[int]bool)
	}
	if v.refUnits[ref][unit] {
		v.errorf("%s: reference %q unit %d is declared twice", label, ref, unit)
		return
	}
	v.refUnits[ref][unit] = true
	if _, seen := v.refBlock[ref]; !seen {
		v.refBlock[ref] = label
	}
	if lib != "" {
		if _, seen := v.refLib[ref]; !seen {
			v.refLib[ref] = lib
		}
	}
}

// arrange rejects names that do not match a declared block and repeats: a
// typo here would otherwise silently demote a block to its own row.
func (v *validator) arrange(d *Design, blocks map[string]bool) {
	seen := make(map[string]bool)
	for ri, row := range d.Arrange {
		for _, name := range row {
			switch {
			case !blocks[name]:
				v.errorf("arrange row %d: unknown block %q", ri+1, name)
			case seen[name]:
				v.errorf("arrange row %d: block %q is listed more than once", ri+1, name)
			default:
				seen[name] = true
			}
		}
	}
}

func (v *validator) nets(d *Design) {
	for _, name := range sortedKeys(d.Nets) {
		if name == "" {
			v.errorf("nets: net name must not be empty")
			continue
		}
		for _, entry := range d.Nets[name] {
			ref, _, ok := splitRefPin(entry)
			if !ok {
				v.errorf("net %q: pin %q must have the form \"REF.pin\"", name, entry)
				continue
			}
			if _, known := v.refBlock[ref]; !known {
				v.errorf("net %q: pin %q refers to unknown reference %q", name, entry, ref)
			}
			if owner, taken := v.pinNet[entry]; taken {
				if owner == name {
					v.errorf("net %q: pin %q is listed twice", name, entry)
				} else {
					v.errorf("net %q: pin %q is already claimed by net %q", name, entry, owner)
				}
				continue
			}
			v.pinNet[entry] = name
		}
	}
}

func (v *validator) powerNets(d *Design) {
	connected := make(map[string]bool)
	for i := range d.Blocks {
		b := &d.Blocks[i]
		for _, port := range sortedKeys(b.Connect) {
			connected[b.Connect[port]] = true
		}
	}
	for _, name := range sortedKeys(d.PowerNets) {
		if _, declared := d.Nets[name]; !declared && !connected[name] {
			v.errorf(`power_nets %q: net appears neither in "nets" nor in any block "connect"`, name)
		}
		lib := d.PowerNets[name]
		if !strings.HasPrefix(lib, "power:") || lib == "power:" {
			v.errorf("power_nets %q: %q must have the form \"power:Name\"", name, lib)
		}
	}
}

func (v *validator) noConnect(d *Design) {
	for _, entry := range d.NoConnect.Pins {
		ref, _, ok := splitRefPin(entry)
		if !ok {
			v.errorf("no_connect: pin %q must have the form \"REF.pin\"", entry)
			continue
		}
		if _, known := v.refBlock[ref]; !known {
			v.errorf("no_connect: pin %q refers to unknown reference %q", entry, ref)
		}
		if net, claimed := v.pinNet[entry]; claimed {
			v.errorf("no_connect: pin %q is also connected by net %q", entry, net)
		}
	}
	for _, ref := range d.NoConnect.Unused {
		if _, known := v.refBlock[ref]; !known {
			v.errorf("no_connect: %q is marked \"unused\" but is not a known reference", ref)
		}
	}
}

// splitRefPin splits a "REF.pin" entry at the first dot. Pin names never
// contain dots, so the reference is always the leading segment.
func splitRefPin(entry string) (ref, pin string, ok bool) {
	ref, pin, ok = strings.Cut(entry, ".")
	if !ok || ref == "" || pin == "" {
		return "", "", false
	}
	return ref, pin, true
}

func validLibID(id string) bool {
	lib, name, ok := strings.Cut(id, ":")
	return ok && lib != "" && name != "" && !strings.Contains(name, ":")
}

func blockLabel(i int, name string) string {
	if name == "" {
		return fmt.Sprintf("block #%d", i+1)
	}
	return fmt.Sprintf("block %q", name)
}

func symbolLabel(i int, ref string) string {
	if ref == "" {
		return fmt.Sprintf("#%d", i+1)
	}
	return fmt.Sprintf("%q", ref)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
