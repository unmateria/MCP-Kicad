// Package importer turns a provider's bytes into a part KiCad can use.
//
// It carries the post-condition the rest of this repository is built around.
// The compiler draws wires and then proves the emitted netlist equals the
// declared one; nothing about the geometry is trusted on its own. The importer
// owes the same debt:
//
//	A part is imported if and only if, after installing it, KiCad can read it
//	back, the symbol instantiates in a schematic with its pins resolved, and
//	the footprint it names exists.
//
// Everything is verified in a temporary directory first. A part that fails is
// not installed at all — a half-imported symbol in the chooser is worse than
// no symbol, because it looks like it works.
package importer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mcp-kicad/internal/fplib"
	"mcp-kicad/internal/parts"
	"mcp-kicad/internal/parts/providers"
	"mcp-kicad/internal/sexp"
	"mcp-kicad/internal/symlib"
)

// Importer installs parts into libsRoot and registers them with KiCad.
type Importer struct {
	LibsRoot        string
	KicadCLI        string
	KicadSymbols    string
	KicadFootprints string
	// ProviderEnv is what the sources need to reach the network.
	ProviderEnv providers.Env
	// PreviewDir is where the symbol and footprint SVGs are written, for a
	// caller that will turn them into pictures. It must OUTLIVE Install:
	// everything else happens in a staging directory that is deleted on the
	// way out, and rendering previews from there produced two identical
	// screenshots of the browser's file-not-found page. Empty means no
	// previews are rendered at all.
	PreviewDir string
	// Now is injectable so tests can assert on the MCP_Source stamp.
	Now func() time.Time
	// RegisterGlobally adds the imported libraries to KiCad's own tables so
	// the part shows up in the GUI's chooser too. Off in tests, which have no
	// business writing into the user's KiCad configuration.
	RegisterGlobally bool
}

// Result is what one import produced, and how it was checked.
type Result struct {
	LibID        string   `json:"lib_id"`
	MPN          string   `json:"mpn"`
	Provider     string   `json:"provider"`
	License      string   `json:"license,omitempty"`
	SourceURL    string   `json:"source_url,omitempty"`
	Datasheet    string   `json:"datasheet,omitempty"`
	Description  string   `json:"description,omitempty"`
	FootprintRef string   `json:"footprint,omitempty"`
	PinCount     int      `json:"pin_count"`
	PadCount     int      `json:"pad_count,omitempty"`
	Checks       []Check  `json:"checks"`
	Warnings     []string `json:"warnings,omitempty"`

	SymbolLibPath string `json:"symbol_lib_path"`
	FootprintPath string `json:"footprint_path,omitempty"`
	Model3DPath   string `json:"model3d_path,omitempty"`
	SymbolSVG     string `json:"symbol_svg,omitempty"`
	FootprintSVG  string `json:"footprint_svg,omitempty"`
	RegisteredIn  string `json:"registered_in,omitempty"`
}

// Failed reports whether any fatal check tripped.
func (r *Result) Failed() bool {
	for _, c := range r.Checks {
		if c.Fatal {
			return true
		}
	}
	return false
}

func (im *Importer) now() time.Time {
	if im.Now != nil {
		return im.Now()
	}
	return time.Now()
}

// Import fetches ref ("provider:id"), verifies it and installs it.
//
// The returned Result is filled in even when the import fails, because the
// checks are the answer to "why not" — and a caller that only gets an error
// string has to guess.
func (im *Importer) Import(ctx context.Context, ref string) (*Result, error) {
	provName, id, err := providers.ParseRef(ref)
	if err != nil {
		return nil, err
	}
	prov, err := providers.Get(im.ProviderEnv, provName)
	if err != nil {
		return nil, err
	}
	bundle, err := prov.Fetch(ctx, id)
	if err != nil {
		return nil, err
	}
	return im.Install(ctx, bundle)
}

// Install verifies and installs an already-fetched bundle. Split out from
// Import so a converter that builds a bundle in memory — EasyEDA, say — goes
// through exactly the same verification as a downloaded file.
func (im *Importer) Install(_ context.Context, b *providers.Bundle) (*Result, error) {
	if im.LibsRoot == "" {
		return nil, fmt.Errorf("importer: libs root is not configured")
	}
	if err := parts.EnsureTree(im.LibsRoot); err != nil {
		return nil, err
	}

	mpn := CanonicalMPN(b.MPN)
	if mpn == "" {
		return nil, fmt.Errorf("importer: %q has no usable part name", b.MPN)
	}
	res := &Result{
		LibID:       parts.ImportedLib + ":" + mpn,
		MPN:         mpn,
		Provider:    b.Provider,
		License:     b.License,
		SourceURL:   b.SourceURL,
		Datasheet:   b.Datasheet,
		Description: b.Description,
		Warnings:    append([]string(nil), b.Notes...),
	}

	symData, ok := b.Assets[providers.Symbol]
	if !ok || len(symData) == 0 {
		res.Checks = append(res.Checks, checkFatal("symbol present", "the source returned no symbol"))
		return res, fmt.Errorf("importer: %s carries no symbol", b.Ref())
	}

	// --- 1. it parses ---
	_, sym, err := symbolFromLibBytes(symData)
	if err != nil {
		res.Checks = append(res.Checks, checkFatal("symbol parses", err.Error()))
		return res, fmt.Errorf("importer: %s: %w", b.Ref(), err)
	}
	res.Checks = append(res.Checks, checkOK("symbol parses", "one symbol, valid S-expression"))

	// --- footprint decided before the symbol is stamped, because the symbol
	// has to point at the name the footprint will actually be installed under.
	fpName, fpRef, fpData, fpChecks, fpWarnings := im.resolveFootprint(b, mpn)
	res.Checks = append(res.Checks, fpChecks...)
	res.Warnings = append(res.Warnings, fpWarnings...)
	res.FootprintRef = fpRef

	symlib.Rename(sym, mpn)
	symlib.SetProperty(sym, "Footprint", fpRef)
	if b.Datasheet != "" {
		symlib.SetProperty(sym, "Datasheet", b.Datasheet)
	}
	symlib.SetProperty(sym, "MCP_Source", im.sourceStamp(b, mpn))

	staged := symlib.New()
	staged.Put(sym)

	// --- 2. KiCad reads it back ---
	upgraded, err := upgradeSymbolLib(im.KicadCLI, staged.Bytes())
	switch {
	case err != nil && im.KicadCLI == "":
		res.Checks = append(res.Checks, checkWarn("KiCad re-reads the symbol",
			"kicad-cli is not configured, so this could not be checked"))
		upgraded = staged.Bytes()
	case err != nil:
		res.Checks = append(res.Checks, checkFatal("KiCad re-reads the symbol", err.Error()))
		return res, fmt.Errorf("importer: KiCad rejected the symbol for %s: %w", mpn, err)
	default:
		res.Checks = append(res.Checks, checkOK("KiCad re-reads the symbol",
			"kicad-cli sym upgrade accepted and normalised it"))
	}

	// --- 3. it instantiates with pins ---
	stage, err := os.MkdirTemp("", "mcp-kicad-import-*")
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(stage)

	stagedSymPath := filepath.Join(stage, parts.ImportedLib+".kicad_sym")
	if err := os.WriteFile(stagedSymPath, upgraded, 0o644); err != nil {
		return res, err
	}
	pins, err := probePins(stagedSymPath, parts.ImportedLib, mpn)
	if err != nil {
		res.Checks = append(res.Checks, checkFatal("symbol instantiates", err.Error()))
		return res, fmt.Errorf("importer: %s does not place: %w", mpn, err)
	}
	if len(pins) == 0 {
		res.Checks = append(res.Checks, checkFatal("symbol instantiates",
			"the symbol places but resolves to zero pins — nothing could ever connect to it"))
		return res, fmt.Errorf("importer: %s resolves to zero pins", mpn)
	}
	res.PinCount = len(pins)
	res.Checks = append(res.Checks, checkOK("symbol instantiates",
		fmt.Sprintf("placed in a scratch schematic, %d pins resolved", len(pins))))

	// --- 4. pins against pads ---
	var stagedFP *fplib.FP
	stagedPretty := filepath.Join(stage, parts.ImportedLib+".pretty")
	if len(fpData) > 0 {
		if err := os.MkdirAll(stagedPretty, 0o755); err != nil {
			return res, err
		}
		if err := os.WriteFile(filepath.Join(stagedPretty, fpName+".kicad_mod"), fpData, 0o644); err != nil {
			return res, err
		}
		if stagedFP, err = fplib.Parse(fpData); err == nil {
			res.PadCount = len(stagedFP.PadNumbers())
		}
	}
	res.Checks = append(res.Checks, comparePinsAndPads(pins, stagedFP))

	// --- 5. it can be looked at ---
	// Into PreviewDir, not into `stage`: the caller renders these AFTER
	// Install returns, and by then the staging directory is gone.
	if im.PreviewDir != "" {
		svgDir := filepath.Join(im.PreviewDir, "imported", mpn)
		_ = os.RemoveAll(svgDir) // a re-import must not show the previous run's drawing
		if p, err := renderSymbolSVG(im.KicadCLI, stagedSymPath, filepath.Join(svgDir, "sym")); err == nil {
			res.SymbolSVG = p
			res.Checks = append(res.Checks, checkOK("renders", "symbol drawn by KiCad"))
		} else {
			res.Checks = append(res.Checks, checkWarn("renders", "symbol preview failed: "+err.Error()))
		}
		if len(fpData) > 0 {
			if p, err := renderFootprintSVG(im.KicadCLI, stagedPretty, fpName, filepath.Join(svgDir, "fp")); err == nil {
				res.FootprintSVG = p
			} else {
				res.Warnings = append(res.Warnings, "footprint preview failed: "+err.Error())
			}
		}
	}

	// Every fatal check has returned by now; what follows actually installs.
	if err := im.commit(res, b, mpn, upgraded, fpName, fpData); err != nil {
		return res, err
	}
	return res, nil
}

// commit writes the verified part into the real library and registers it.
func (im *Importer) commit(res *Result, b *providers.Bundle, mpn string, symData []byte, fpName string, fpData []byte) error {
	// The 3D model goes first: the footprint has to point at where it landed.
	if raw, ok := b.Assets[providers.Model3D]; ok && len(raw) > 0 && len(fpData) > 0 {
		ext := b.Model3DExt
		if ext == "" {
			ext = ".step"
		}
		modelPath := filepath.Join(parts.Models3DPath(im.LibsRoot), mpn+ext)
		if err := os.WriteFile(modelPath, raw, 0o644); err != nil {
			res.Warnings = append(res.Warnings, "3D model could not be written: "+err.Error())
		} else {
			res.Model3DPath = modelPath
			if fp, err := fplib.Parse(fpData); err == nil {
				// An absolute path, because this library is not a project and
				// not versioned: KiCad's ${KIPRJMOD} would resolve somewhere
				// different for every schematic that used the part.
				abs, _ := filepath.Abs(modelPath)
				fp.SetModel(filepath.ToSlash(abs))
				fpData = fp.Bytes()
			}
		}
	}

	if len(fpData) > 0 {
		prettyDir := parts.ImportedFootprintLib(im.LibsRoot)
		if err := os.MkdirAll(prettyDir, 0o755); err != nil {
			return err
		}
		path := filepath.Join(prettyDir, fpName+".kicad_mod")
		if err := os.WriteFile(path, fpData, 0o644); err != nil {
			return err
		}
		res.FootprintPath = path
	}

	// The symbol is merged into the shared library rather than replacing it:
	// this file holds every part imported so far.
	symPath := parts.ImportedSymbolLib(im.LibsRoot)
	lib, err := symlib.Load(symPath)
	if err != nil {
		return err
	}
	_, node, err := symbolFromLibBytes(symData)
	if err != nil {
		return err
	}
	lib.Put(node)
	if err := lib.Save(symPath); err != nil {
		return err
	}
	res.SymbolLibPath = symPath

	if im.RegisterGlobally {
		if where, err := im.register(); err != nil {
			res.Warnings = append(res.Warnings, "could not register with KiCad: "+err.Error())
		} else {
			res.RegisteredIn = where
		}
	}
	return nil
}

// register adds the imported libraries to KiCad's global tables, so the part
// appears in the GUI's chooser and not only in this server's search path.
func (im *Importer) register() (string, error) {
	dir, err := parts.GlobalTableDir(im.KicadCLI)
	if err != nil {
		return "", err
	}
	symURI, _ := filepath.Abs(parts.ImportedSymbolLib(im.LibsRoot))
	fpURI, _ := filepath.Abs(parts.ImportedFootprintLib(im.LibsRoot))

	if _, err := parts.RegisterLibrary(filepath.Join(dir, "sym-lib-table"), parts.LibTableEntry{
		Name:  parts.ImportedLib,
		URI:   filepath.ToSlash(symURI),
		Descr: "Parts imported by mcp-kicad",
	}); err != nil {
		return "", err
	}
	if _, err := parts.RegisterLibrary(filepath.Join(dir, "fp-lib-table"), parts.LibTableEntry{
		Name:  parts.ImportedLib,
		URI:   filepath.ToSlash(fpURI),
		Descr: "Footprints imported by mcp-kicad",
	}); err != nil {
		return "", err
	}
	return dir, nil
}

// resolveFootprint decides which footprint the imported symbol will point at.
//
// Three outcomes, in order of preference, and never a fourth: the source
// shipped one, the reference names one that is already installed, or there is
// none and the caller is told so. Substituting a similar-looking footprint
// would produce a part that assembles wrong, silently.
func (im *Importer) resolveFootprint(b *providers.Bundle, mpn string) (name, ref string, data []byte, checks []Check, warnings []string) {
	if raw, has := b.Assets[providers.Footprint]; has && len(raw) > 0 {
		fp, err := fplib.Parse(raw)
		if err != nil {
			return "", "", nil, []Check{checkWarn("footprint", "the source's footprint does not parse: "+err.Error())},
				[]string{"imported without a footprint"}
		}
		name = FootprintName(mpn, fp.Name())
		fp.SetName(name)
		fp.SetProperty("MCP_Source", im.sourceStamp(b, mpn))
		out := fp.Bytes()

		if upgraded, err := upgradeFootprint(im.KicadCLI, out, name); err == nil {
			out = upgraded
			checks = append(checks, checkOK("KiCad re-reads the footprint", "kicad-cli fp upgrade accepted it"))
		} else if im.KicadCLI == "" {
			checks = append(checks, checkWarn("KiCad re-reads the footprint", "kicad-cli is not configured"))
		} else {
			// Not fatal: the symbol is still worth having, and the footprint
			// KiCad refused is simply not installed.
			return "", "", nil,
				append(checks, checkWarn("KiCad re-reads the footprint", "rejected: "+err.Error())),
				[]string{"the source's footprint was rejected by KiCad and has not been installed"}
		}
		return name, parts.ImportedLib + ":" + name, out, checks, nil
	}

	if b.FootprintRef == "" {
		return "", "", nil,
			[]Check{checkWarn("footprint", "neither the source nor the symbol names one")},
			[]string{"no footprint: set the Footprint property yourself before laying out a board"}
	}

	if resolved, where := im.findInstalledFootprint(b.FootprintRef); resolved != "" {
		return "", resolved, nil,
			[]Check{checkOK("footprint", fmt.Sprintf("%s resolved to the installed %s", b.FootprintRef, where))},
			nil
	}
	return "", "", nil,
		[]Check{checkWarn("footprint", fmt.Sprintf("%q is not in this source nor installed here", b.FootprintRef))},
		[]string{fmt.Sprintf("the symbol asks for footprint %q, which is not on this machine — install it or pick another before laying out a board", b.FootprintRef)}
}

// findInstalledFootprint looks a "Lib:Name" reference up in the footprint
// libraries this machine already has, returning the reference to use and where
// it was found.
func (im *Importer) findInstalledFootprint(ref string) (string, string) {
	libName, fpName, qualified := strings.Cut(ref, ":")
	if !qualified {
		fpName, libName = libName, ""
	}
	dirs := parts.FootprintSearchPath(im.LibsRoot, im.KicadFootprints)

	if libName != "" {
		for _, d := range dirs {
			p := filepath.Join(d, libName+".pretty", fpName+".kicad_mod")
			if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
				return ref, p
			}
		}
	}
	// The alias in front of the colon is whatever the source called its own
	// library, which says nothing about what this machine calls it. The
	// footprint NAME is the part that is portable.
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || !strings.HasSuffix(e.Name(), ".pretty") {
				continue
			}
			p := filepath.Join(d, e.Name(), fpName+".kicad_mod")
			if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
				return strings.TrimSuffix(e.Name(), ".pretty") + ":" + fpName, p
			}
		}
	}
	return "", ""
}

// sourceStamp is the MCP_Source property: where this part came from, under
// what licence, and when. It is what makes libs/ reproducible without being
// versioned, and what lets a licence question be answered months later.
func (im *Importer) sourceStamp(b *providers.Bundle, installedAs string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "provider=%s", b.Provider)
	if b.SourceURL != "" {
		fmt.Fprintf(&sb, " url=%s", b.SourceURL)
	}
	if b.License != "" {
		fmt.Fprintf(&sb, " license=%q", b.License)
	}
	// The name the source used, when we renamed it. Without this there is no
	// way back from "MCP_Imported:LTV-217-B-G" to the file it came out of.
	if b.SymbolName != "" && b.SymbolName != installedAs {
		fmt.Fprintf(&sb, " original=%q", b.SymbolName)
	}
	fmt.Fprintf(&sb, " imported=%s", im.now().UTC().Format("2006-01-02"))
	return sb.String()
}

// ImportedParts lists what is currently in the imported library, with the
// source stamp of each. It answers "where did this come from" without opening
// KiCad.
func ImportedParts(libsRoot string) ([]ImportedPart, error) {
	lib, err := symlib.Load(parts.ImportedSymbolLib(libsRoot))
	if err != nil {
		return nil, err
	}
	var out []ImportedPart
	for _, name := range lib.Names() {
		sym, _ := lib.Get(name)
		out = append(out, ImportedPart{
			LibID:     parts.ImportedLib + ":" + name,
			MPN:       name,
			Footprint: symlib.Property(sym, "Footprint"),
			Source:    symlib.Property(sym, "MCP_Source"),
			Pins:      len(symlib.PinNumbers(sym)),
		})
	}
	return out, nil
}

// ImportedPart is one row of ImportedParts.
type ImportedPart struct {
	LibID     string
	MPN       string
	Footprint string
	Source    string
	Pins      int
}

var _ = sexp.NewUUID // keep the sexp dependency explicit for probePins
