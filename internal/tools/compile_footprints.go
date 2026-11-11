package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mcp-kicad/internal/compile"
	"mcp-kicad/internal/fplib"
	"mcp-kicad/internal/parts"
	"mcp-kicad/internal/sexp"
	"mcp-kicad/internal/symlib"
)

// Where a symbol's footprint came from, in order of precedence.
const (
	fpExplicit = "explicit" // the source's "footprint" field
	fpLibrary  = "library"  // the library symbol's own Footprint property
	fpDefault  = "default"  // parts.SuggestFootprint for the design's mount type
)

type fpChoice struct {
	ref, libID, fp, origin string
	problem                string // non-empty: left empty, and why
}

// assignFootprints gives every board symbol a footprint, so that KiCad's
// "Update PCB from Schematic" has something to place. A schematic with empty
// Footprint fields compiles, verifies and passes ERC, and is still useless the
// moment anyone tries to make a board from it.
//
// Every reference written is checked first: the .kicad_mod must exist along
// the footprint search path and carry a pad for every pin of the symbol. A
// library or default choice that fails is left EMPTY and reported; an explicit
// one that fails is an authoring error. A broken reference is worse than none:
// it looks assigned until the PCB editor refuses it.
//
// When no footprint library can be found at all (CI has no KiCad), references
// are written unverified and the report says so.
func (e *Env) assignFootprints(sch *sexp.Schematic, d *compile.Design) (string, error) {
	explicit, err := explicitFootprints(d)
	if err != nil {
		return "", err
	}
	mount := d.Mount
	if mount == "" {
		mount = "THT"
	}
	searchPath := existingDirs(parts.FootprintSearchPath(e.LibsRoot, e.KicadFootprints))
	verified := len(searchPath) > 0
	pads := map[string][]string{} // footprint ref -> pad numbers; nil = not found

	nodesByRef := map[string][]*sexp.Node{}
	libByRef := map[string]string{}
	for _, n := range sch.Root().Children {
		if n.Head() != "symbol" || sexp.AtomValue(sexp.FindList(n, "on_board"), 1) == "no" {
			continue
		}
		libID := sexp.StringValue(sexp.FindList(n, "lib_id"), 1)
		ref := symlib.Property(n, "Reference")
		if strings.HasPrefix(libID, "power:") || strings.HasPrefix(ref, "#") {
			continue
		}
		nodesByRef[ref] = append(nodesByRef[ref], n)
		libByRef[ref] = libID
	}
	refs := make([]string, 0, len(nodesByRef))
	for ref := range nodesByRef {
		refs = append(refs, ref)
	}
	sort.Strings(refs)

	var choices []fpChoice
	for _, ref := range refs {
		libID := libByRef[ref]
		def := libSymbolDef(sch, libID)
		c := fpChoice{ref: ref, libID: libID}
		switch {
		case explicit[ref] != "":
			c.fp, c.origin = explicit[ref], fpExplicit
		case def != nil && symlib.Property(def, "Footprint") != "":
			c.fp, c.origin = symlib.Property(def, "Footprint"), fpLibrary
		default:
			c.fp, c.origin = parts.SuggestFootprint(libID, mount), fpDefault
		}
		if c.fp == "" {
			c.problem = `no library or default footprint; declare "footprint" on it`
		} else if verified {
			c.problem = checkFootprint(c.fp, def, searchPath, pads)
		}
		if c.problem != "" && c.origin == fpExplicit {
			return "", fmt.Errorf("%s: footprint %s: %s", ref, c.fp, c.problem)
		}
		if c.problem == "" {
			for _, n := range nodesByRef[ref] {
				symlib.SetProperty(n, "Footprint", c.fp)
			}
		}
		choices = append(choices, c)
	}
	return footprintReport(choices, mount, verified), nil
}

// explicitFootprints collects the source's per-symbol footprints. The units
// of one part share a single footprint, so two units naming different ones is
// a contradiction in the source.
func explicitFootprints(d *compile.Design) (map[string]string, error) {
	out := map[string]string{}
	for _, b := range d.Blocks {
		for _, s := range b.Symbols {
			if s.Footprint == "" {
				continue
			}
			if prev := out[s.Ref]; prev != "" && prev != s.Footprint {
				return nil, fmt.Errorf("%s: units declare different footprints (%s, %s)", s.Ref, prev, s.Footprint)
			}
			out[s.Ref] = s.Footprint
		}
	}
	return out, nil
}

// checkFootprint returns "" when fpRef resolves to an installed footprint
// with a pad for every pin of the symbol, or why it does not.
func checkFootprint(fpRef string, def *sexp.Node, searchPath []string, cache map[string][]string) string {
	padNums, seen := cache[fpRef]
	if !seen {
		lib, name, _ := strings.Cut(fpRef, ":")
		for _, dir := range searchPath {
			p := filepath.Join(dir, lib+".pretty", name+".kicad_mod")
			if fp, err := fplib.Load(p); err == nil {
				padNums = fp.PadNumbers()
				break
			}
		}
		cache[fpRef] = padNums
	}
	if padNums == nil {
		return "not found in the installed footprint libraries"
	}
	if def == nil {
		return ""
	}
	have := map[string]bool{}
	for _, p := range padNums {
		have[p] = true
	}
	var missing []string
	for _, pin := range sexp.ExtractPinNumbers(def, 0) {
		if !have[pin] {
			missing = append(missing, pin)
		}
	}
	if len(missing) > 0 {
		return fmt.Sprintf("has no pad for pin(s) %s", strings.Join(missing, ", "))
	}
	return ""
}

func existingDirs(dirs []string) []string {
	var out []string
	for _, d := range dirs {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			out = append(out, d)
		}
	}
	return out
}

// footprintReport groups the choices by origin and footprint so a board of
// twenty 0603 resistors reads as one line, and puts the gaps first.
func footprintReport(choices []fpChoice, mount string, verified bool) string {
	if len(choices) == 0 {
		return ""
	}
	var sb strings.Builder
	assigned := 0
	for _, c := range choices {
		if c.problem == "" {
			assigned++
		}
	}
	fmt.Fprintf(&sb, "footprints: %d of %d assigned (mount %s)", assigned, len(choices), mount)
	if !verified {
		sb.WriteString(" — NOT verified: no footprint library found")
	}
	sb.WriteString("\n")
	for _, c := range choices {
		if c.problem != "" {
			fmt.Fprintf(&sb, "  MISSING   %s (%s) %s\n", c.ref, c.libID, strings.TrimPrefix(c.fp+": "+c.problem, ": "))
		}
	}
	type key struct{ origin, fp string }
	var order []key
	groups := map[key][]string{}
	for _, c := range choices {
		if c.problem != "" {
			continue
		}
		k := key{c.origin, c.fp}
		if groups[k] == nil {
			order = append(order, k)
		}
		groups[k] = append(groups[k], c.ref)
	}
	rank := map[string]int{fpExplicit: 0, fpLibrary: 1, fpDefault: 2}
	sort.SliceStable(order, func(i, j int) bool {
		if rank[order[i].origin] != rank[order[j].origin] {
			return rank[order[i].origin] < rank[order[j].origin]
		}
		return order[i].fp < order[j].fp
	})
	for _, k := range order {
		fmt.Fprintf(&sb, "  %-9s %s  %s\n", k.origin, k.fp, strings.Join(groups[k], " "))
	}
	for _, k := range order {
		if k.origin == fpDefault {
			sb.WriteString(`  defaults are a choice, not a fact about the part: override with "footprint" on the symbol, or "mount": "SMD"` + "\n")
			break
		}
	}
	return sb.String()
}
