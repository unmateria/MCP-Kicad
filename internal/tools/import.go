package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-kicad/internal/parts"
	"mcp-kicad/internal/parts/importer"
	"mcp-kicad/internal/parts/providers"
)

// providerEnv is what the component sources need from the server.
func (e *Env) providerEnv() providers.Env {
	return providers.Env{
		LibsRoot:        e.LibsRoot,
		KicadSymbols:    e.KicadSymbols,
		KicadFootprints: e.KicadFootprints,
		Mouser:          e.Mouser,
		DigiKeyID:       e.DigiKeyID,
		DigiKeySecret:   e.DigiKeySecret,
	}
}

func (e *Env) importer() *importer.Importer {
	return &importer.Importer{
		LibsRoot:        e.LibsRoot,
		KicadCLI:        e.KicadCLI,
		KicadSymbols:    e.KicadSymbols,
		KicadFootprints: e.KicadFootprints,
		ProviderEnv:     e.providerEnv(),
		// The previews have to survive the import: import_part turns them into
		// PNGs after Install returns.
		PreviewDir: e.OutputDir,
		// The point of importing is that the part becomes usable, and half of
		// "usable" is the KiCad GUI seeing it too.
		RegisterGlobally: true,
	}
}

// --- find_part ---

type findPartInput struct {
	Query        string   `json:"query" jsonschema:"What to look for: a manufacturer part number (NE555P, ESP32-C3-MINI-1), an LCSC code (C115450), or plain words (dual op-amp SOIC-8)"`
	Manufacturer string   `json:"manufacturer,omitempty" jsonschema:"Narrow the result when the same part number exists at several manufacturers"`
	Sources      []string `json:"sources,omitempty" jsonschema:"Limit the search to these sources. Omit to search all of them. Names: installed, cern, jlcpcb, digikey-lib, espressif, sparkfun, lcsc"`
	Limit        int      `json:"limit,omitempty" jsonschema:"Maximum candidates per source (default 8)"`
	Refresh      bool     `json:"refresh,omitempty" jsonschema:"Re-download the source catalogues before searching. Slow; only needed when a part was added upstream after your last search"`
}

func (e *Env) handleFindPart(ctx context.Context, _ *mcp.CallToolRequest, input findPartInput) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)
	if strings.TrimSpace(input.Query) == "" {
		return toolText("error: query is required"), nil, nil
	}
	env := e.providerEnv()

	var sb strings.Builder
	if input.Refresh {
		// Bounded: rebuilding every catalogue means downloading a couple of
		// hundred library files, and an unbounded tool call just hangs.
		rctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		statuses, errs := providers.Refresh(rctx, env, input.Sources)
		cancel()
		for _, s := range statuses {
			fmt.Fprintf(&sb, "refreshed %s: %d parts\n", s.Provider, s.Entries)
		}
		for _, err := range errs {
			fmt.Fprintf(&sb, "refresh failed for %v\n", err)
		}
		sb.WriteString("\n")
	}

	sctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	cands, errs := providers.SearchAll(sctx, env, providers.Query{
		Text:         input.Query,
		Manufacturer: input.Manufacturer,
		Limit:        input.Limit,
	}, input.Sources)

	if len(cands) == 0 {
		sb.WriteString(e.noCandidatesMessage(input.Query, errs))
		return toolText(sb.String()), nil, nil
	}

	// Catalogue entries are listed apart. They identify a part but carry no
	// files, and mixing them into the same numbered list invites an import
	// call that cannot succeed.
	var installable, catalogue []providers.Candidate
	for _, c := range cands {
		if c.MetadataOnly {
			catalogue = append(catalogue, c)
			continue
		}
		installable = append(installable, c)
	}

	fmt.Fprintf(&sb, "%d candidate(s) for %q, best first.\n\n", len(installable), input.Query)
	for i, c := range installable {
		if i >= 25 {
			fmt.Fprintf(&sb, "\n… %d more, narrow the query or set sources to see them.\n", len(installable)-i)
			break
		}
		if c.Installed {
			fmt.Fprintf(&sb, "%2d. %s  [ALREADY INSTALLED — use this lib_id directly, no import needed]\n", i+1, c.LibID)
		} else {
			fmt.Fprintf(&sb, "%2d. %s\n", i+1, c.MPN)
			fmt.Fprintf(&sb, "    ref:      %s\n", c.Ref())
		}
		if c.Manufacturer != "" {
			fmt.Fprintf(&sb, "    maker:    %s\n", c.Manufacturer)
		}
		if c.Package != "" {
			fmt.Fprintf(&sb, "    package:  %s\n", c.Package)
		}
		if !c.Installed {
			fmt.Fprintf(&sb, "    provides: %s\n", assetList(c.Has))
			if c.License != "" {
				fmt.Fprintf(&sb, "    licence:  %s\n", c.License)
			}
		}
		if c.Description != "" {
			fmt.Fprintf(&sb, "    %s\n", truncate(c.Description, 160))
		}
	}

	if len(installable) > 0 {
		sb.WriteString("\nCall import_part with a ref to install one. An ALREADY INSTALLED row needs no import.\n")
	}

	if len(catalogue) > 0 {
		sb.WriteString("\nDistributor matches — identification only, NO files to import:\n")
		for i, c := range catalogue {
			if i >= 6 {
				break
			}
			fmt.Fprintf(&sb, "  %s", c.MPN)
			if c.Manufacturer != "" {
				fmt.Fprintf(&sb, "  by %s", c.Manufacturer)
			}
			if c.Package != "" {
				fmt.Fprintf(&sb, "  [%s]", c.Package)
			}
			fmt.Fprintf(&sb, "   (%s)\n", c.Provider)
			if c.Description != "" {
				fmt.Fprintf(&sb, "      %s\n", truncate(c.Description, 140))
			}
			if c.Datasheet != "" {
				fmt.Fprintf(&sb, "      %s\n", c.Datasheet)
			}
		}
		if len(installable) == 0 {
			sb.WriteString("\nThese say what the part IS but hold no CAD data. Run find_part again with one of\n" +
				"the manufacturer part numbers above to look for a symbol and footprint.\n")
		}
	}

	for _, err := range errs {
		fmt.Fprintf(&sb, "\n(source unavailable: %v)", err)
	}
	return toolText(sb.String()), nil, nil
}

// noCandidatesMessage says why nothing came back, and what to do instead.
// Giving up is a legitimate answer here — inventing a similar part would be
// worse than useless — but giving up silently is not.
func (e *Env) noCandidatesMessage(query string, errs []error) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "No candidate found for %q in any source.\n\n", query)

	sb.WriteString("Sources searched:\n")
	for _, p := range providers.All(e.providerEnv()) {
		st := providers.Status(e.LibsRoot, p.Name())
		switch {
		case !p.Available():
			fmt.Fprintf(&sb, "  %-12s not available (%s)\n", p.Name(), p.Description())
		case st.Built:
			fmt.Fprintf(&sb, "  %-12s %d parts, indexed %s\n", p.Name(), st.Entries, humanAge(st.Age))
		default:
			fmt.Fprintf(&sb, "  %-12s %s\n", p.Name(), p.Description())
		}
	}
	for _, err := range errs {
		fmt.Fprintf(&sb, "  ! %v\n", err)
	}

	sb.WriteString("\nWhat to try, in order:\n")
	sb.WriteString("  1. Search the family instead of the exact order code: \"NE555\" rather than \"NE555PWRG4\".\n")
	sb.WriteString("  2. Search by function: \"dual op-amp\", \"3.3V LDO SOT-23\", \"optocoupler\".\n")
	sb.WriteString("  3. If it is an LCSC part, search its C-number.\n")
	sb.WriteString("  4. find_part with refresh=true, in case it was published upstream recently.\n")
	sb.WriteString("\nIf none of that finds it, the part is not available from any source this server knows.\n")
	sb.WriteString("Say so plainly and offer the nearest equivalents from check_component_existence —\n")
	sb.WriteString("do NOT substitute a similar part silently, and never draw a symbol by hand.\n")
	return sb.String()
}

// --- import_part ---

type importPartInput struct {
	Ref string `json:"ref" jsonschema:"The ref from find_part, e.g. jlcpcb:symbols/JLCPCB-Optocouplers.kicad_sym#LTV-217-B-G"`
}

func (e *Env) handleImportPart(ctx context.Context, _ *mcp.CallToolRequest, input importPartInput) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)
	if strings.TrimSpace(input.Ref) == "" {
		return toolText("error: ref is required — call find_part first and pass one of its refs"), nil, nil
	}

	ictx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	im := e.importer()
	result, err := im.Import(ictx, input.Ref)
	if err != nil {
		var sb strings.Builder
		fmt.Fprintf(&sb, "import FAILED: %v\n", err)
		if result != nil {
			writeChecks(&sb, result)
			sb.WriteString("\nNothing was installed. A part that fails verification is not written to the library.\n")
		}
		return toolText(sb.String()), nil, nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Imported %s\n\n", result.MPN)
	fmt.Fprintf(&sb, "  lib_id:    %s        ← put this in your .design.json\n", result.LibID)
	if result.FootprintRef != "" {
		fmt.Fprintf(&sb, "  footprint: %s\n", result.FootprintRef)
	} else {
		sb.WriteString("  footprint: NONE — set one before laying out a board\n")
	}
	fmt.Fprintf(&sb, "  pins:      %d", result.PinCount)
	if result.PadCount > 0 {
		fmt.Fprintf(&sb, "   pads: %d", result.PadCount)
	}
	sb.WriteString("\n")
	fmt.Fprintf(&sb, "  source:    %s", result.Provider)
	if result.License != "" {
		fmt.Fprintf(&sb, "  (licence: %s)", result.License)
	}
	sb.WriteString("\n")
	if result.SourceURL != "" {
		fmt.Fprintf(&sb, "  url:       %s\n", result.SourceURL)
	}
	if result.Datasheet != "" {
		fmt.Fprintf(&sb, "  datasheet: %s\n", result.Datasheet)
	}
	fmt.Fprintf(&sb, "  written:   %s\n", result.SymbolLibPath)
	if result.FootprintPath != "" {
		fmt.Fprintf(&sb, "             %s\n", result.FootprintPath)
	}
	if result.Model3DPath != "" {
		fmt.Fprintf(&sb, "             %s\n", result.Model3DPath)
	}
	if result.RegisteredIn != "" {
		fmt.Fprintf(&sb, "  KiCad:     registered in %s (visible in the GUI's symbol chooser)\n", result.RegisteredIn)
	}

	sb.WriteString("\n")
	writeChecks(&sb, result)

	if len(result.Warnings) > 0 {
		sb.WriteString("\nwarnings:\n")
		for _, w := range result.Warnings {
			fmt.Fprintf(&sb, "  - %s\n", w)
		}
	}
	sb.WriteString("\nLook at the images: a symbol whose pins are wrong is obvious on sight and\n")
	sb.WriteString("invisible to every check above. Then call symbol_pins to get the pin names\n")
	sb.WriteString("before writing nets against it.\n")

	var contents []mcp.Content
	if result.SymbolSVG != "" {
		if png, err := svgToPNG(result.SymbolSVG); err == nil {
			contents = append(contents, &mcp.ImageContent{Data: png, MIMEType: "image/png"})
		}
	}
	if result.FootprintSVG != "" {
		if png, err := svgToPNG(result.FootprintSVG); err == nil {
			contents = append(contents, &mcp.ImageContent{Data: png, MIMEType: "image/png"})
		}
	}
	contents = append(contents, &mcp.TextContent{Text: sb.String()})
	return &mcp.CallToolResult{Content: contents}, nil, nil
}

func writeChecks(sb *strings.Builder, r *importer.Result) {
	sb.WriteString("verification:\n")
	for _, c := range r.Checks {
		mark := "warn"
		switch {
		case c.Fatal:
			mark = "FAIL"
		case c.OK:
			mark = " ok "
		}
		fmt.Fprintf(sb, "  [%s] %-28s %s\n", mark, c.Name, c.Detail)
	}
}

func assetList(kinds []providers.AssetKind) string {
	if len(kinds) == 0 {
		return "nothing"
	}
	out := make([]string, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, string(k))
	}
	sort.Strings(out)
	return strings.Join(out, " + ")
}

// humanAge says how stale a cached catalogue is. Rounding everything to the
// hour turned a fresh index into "indexed 0s ago", which reads as broken.
func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// RegisterImportTools registers the component import tools.
func RegisterImportTools(s *mcp.Server, env *Env) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "find_part",
		Description: "Find a component across every source at once: the KiCad libraries installed here, " +
			"the parts already imported, and the external libraries (CERN, JLCPCB, Digi-Key, Espressif, " +
			"SparkFun, LCSC/EasyEDA). Returns ranked candidates with what each one provides (symbol, " +
			"footprint, 3D model), its licence, and a ref to hand to import_part.\n\n" +
			"Use this when check_component_existence comes back empty. A candidate marked ALREADY " +
			"INSTALLED needs no import — use its lib_id straight away.",
	}, WrapTool(env.Log, "find_part", env.handleFindPart))

	mcp.AddTool(s, &mcp.Tool{
		Name: "import_part",
		Description: "Install a candidate from find_part into the " + parts.ImportedLib + " library and " +
			"register it with KiCad, so compile_schematic and the KiCad GUI can both use it.\n\n" +
			"The part is verified before anything is written: it must parse, KiCad must read it back, " +
			"and it must place in a schematic with its pins resolved. A part that fails is NOT installed. " +
			"Returns the lib_id to use, the verification report, and pictures of the symbol and footprint — " +
			"look at them.",
	}, WrapTool(env.Log, "import_part", env.handleImportPart))
}
