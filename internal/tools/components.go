package tools

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-kicad/internal/parts"
)

// recoverToolPanic is a deferred helper that converts a panic into a toolText error.
// Usage: defer recoverToolPanic(&result)
func recoverToolPanic(result **mcp.CallToolResult) {
	if r := recover(); r != nil {
		log.Printf("tool panic: %v\n%s", r, debug.Stack())
		*result = toolText(fmt.Sprintf("internal error (panic): %v\nSee server stderr for stack trace.", r))
	}
}

// Env is the shared server environment injected into all tool handlers.
type Env struct {
	LibsRoot        string // e.g. "libs"
	KicadCLI        string // path to kicad-cli.exe
	KicadSymbols    string // path to KiCad global symbols dir, e.g. ".../share/kicad/symbols"
	KicadFootprints string // path to KiCad global footprints dir, e.g. ".../share/kicad/footprints"
	SnapEDA         *parts.SnapEDAClient
	AnthropicKey    string         // optional; enables visual ranking of layout candidates
	Server          *mcp.Server    // set after server creation; used to register export resources
	OutputDir       string         // directory where generated files are written
	ConfigPath      string         // absolute path to config.ini, for write-back
	Log             *SessionLogger // session logger; always non-nil after server init
}

// --- check_component_existence ---

type checkComponentInput struct {
	Query string `json:"query" jsonschema:"Search query in LibName:PartName format (e.g. Device:R) or keyword"`
}

func (e *Env) handleCheckComponentExistence(_ context.Context, _ *mcp.CallToolRequest, input checkComponentInput) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)
	q := input.Query
	if q == "" {
		return toolText("error: query is required"), nil, nil
	}

	result, err := parts.LocalSearch(e.LibsRoot, q)
	if err == nil {
		return toolText(fmt.Sprintf("Found in %s: %s", result.Source, result.Path)), nil, nil
	}

	if strings.Contains(q, ":") {
		fpResult, fpErr := parts.FootprintSearch(e.LibsRoot, q)
		if fpErr == nil {
			return toolText(fmt.Sprintf("Footprint found in %s: %s", fpResult.Source, fpResult.Path)), nil, nil
		}
	}

	if e.KicadSymbols != "" {
		if result, err := parts.GlobalSearch(e.KicadSymbols, q); err == nil {
			return toolText(fmt.Sprintf("Found in %s: %s", result.Source, result.Path)), nil, nil
		}
		if strings.Contains(q, ":") {
			// Qualified query like "Timer:NE555" — search within that specific library
			// for substring matches (e.g. finds "Timer:NE555P", "Timer:NE555D").
			qParts := strings.SplitN(q, ":", 2)
			if matches, err := parts.SearchSymbols(e.KicadSymbols, qParts[0], qParts[1]); err == nil && len(matches) > 0 {
				if len(matches) > 8 {
					matches = matches[:8]
				}
				var sb strings.Builder
				fmt.Fprintf(&sb, "Not found as exact lib_id. Partial matches for %q in %s library:\n", q, qParts[0])
				for _, m := range matches {
					fmt.Fprintf(&sb, "  %s\n", m)
				}
				sb.WriteString("Use one of these lib_ids with add_symbol (format: LibName:PartName).")
				return toolText(sb.String()), nil, nil
			}
		} else {
			// Unqualified query — cross-library fuzzy search by part name.
			if matches := parts.FuzzySearchGlobal(e.KicadSymbols, q, 8); len(matches) > 0 {
				var sb strings.Builder
				fmt.Fprintf(&sb, "Not found as exact lib_id. Partial matches for %q in KiCad global libraries:\n", q)
				for _, m := range matches {
					fmt.Fprintf(&sb, "  %s\n", m)
				}
				sb.WriteString("Use one of these lib_ids with add_symbol (format: LibName:PartName).")
				return toolText(sb.String()), nil, nil
			}
		}
	}

	if e.SnapEDA != nil {
		results, snapErr := e.SnapEDA.Search(q)
		if snapErr != nil {
			return toolText(fmt.Sprintf("not found locally; SnapEDA error: %v", snapErr)), nil, nil
		}
		if len(results) == 0 {
			return toolText("component not found in local libraries or SnapEDA"), nil, nil
		}
		var sb strings.Builder
		sb.WriteString("Not found locally. SnapEDA matches:\n")
		for i, r := range results {
			if i >= 5 {
				break
			}
			fmt.Fprintf(&sb, "  - [%d] %s by %s (symbol=%v footprint=%v)\n",
				r.ID, r.PartName, r.Manufacturer, r.HasSymbol, r.HasFootprint)
		}
		sb.WriteString("Use fetch_external_part with the part ID to download.")
		return toolText(sb.String()), nil, nil
	}

	return toolText(fmt.Sprintf("component %q not found in local libraries (SnapEDA not configured)", q)), nil, nil
}

// --- fetch_external_part ---

type fetchExternalPartInput struct {
	PartID int    `json:"part_id" jsonschema:"SnapEDA part ID (integer)"`
	Dest   string `json:"dest,omitempty"    jsonschema:"Optional destination subfolder inside libs/downloaded/"`
}

func (e *Env) handleFetchExternalPart(_ context.Context, _ *mcp.CallToolRequest, input fetchExternalPartInput) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)
	if e.SnapEDA == nil {
		return toolText("error: SnapEDA API key not configured"), nil, nil
	}
	if input.PartID == 0 {
		return toolText("error: part_id is required"), nil, nil
	}

	destDir := filepath.Join(e.LibsRoot, "downloaded")
	if input.Dest != "" {
		destDir = filepath.Join(destDir, input.Dest)
	}

	symPath, fpPath, err := e.SnapEDA.Download(input.PartID, destDir)
	if err != nil {
		return toolText(fmt.Sprintf("download failed: %v", err)), nil, nil
	}

	return toolText(fmt.Sprintf("Downloaded:\n  symbol: %s\n  footprint: %s", symPath, fpPath)), nil, nil
}

// --- register_library ---

type registerLibraryInput struct {
	TableFile string `json:"table_file" jsonschema:"Path to sym-lib-table or fp-lib-table"`
	LibName   string `json:"lib_name"   jsonschema:"Library name"`
	LibPath   string `json:"lib_path"   jsonschema:"Absolute path to the library file or directory"`
	LibType   string `json:"lib_type,omitempty"   jsonschema:"Library type: KiCad (default) or Legacy"`
}

func (e *Env) handleRegisterLibrary(_ context.Context, _ *mcp.CallToolRequest, input registerLibraryInput) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)
	if input.TableFile == "" || input.LibName == "" || input.LibPath == "" {
		return toolText("error: table_file, lib_name, and lib_path are required"), nil, nil
	}
	libType := input.LibType
	if libType == "" {
		libType = "KiCad"
	}

	data, err := os.ReadFile(input.TableFile)
	if err != nil && !os.IsNotExist(err) {
		return toolText(fmt.Sprintf("error reading table: %v", err)), nil, nil
	}

	content := string(data)
	if content == "" {
		content = "(sym_lib_table\n)\n"
	}

	if strings.Contains(content, fmt.Sprintf(`(name "%s")`, input.LibName)) {
		return toolText(fmt.Sprintf("library %q already registered in %s", input.LibName, input.TableFile)), nil, nil
	}

	entry := fmt.Sprintf(`  (lib (name "%s")(type "%s")(uri "%s")(options "")(descr ""))`,
		input.LibName, libType, input.LibPath)
	closeIdx := strings.LastIndex(content, ")")
	if closeIdx < 0 {
		return toolText("error: malformed table file (no closing paren)"), nil, nil
	}
	newContent := content[:closeIdx] + entry + "\n" + content[closeIdx:]

	if err := os.WriteFile(input.TableFile, []byte(newContent), 0o644); err != nil {
		return toolText(fmt.Sprintf("error writing table: %v", err)), nil, nil
	}

	return toolText(fmt.Sprintf("registered %q in %s", input.LibName, input.TableFile)), nil, nil
}

// --- list_symbol_libraries ---

type listSymbolLibrariesInput struct {
	Filter  string `json:"filter,omitempty"   jsonschema:"Optional substring to filter library names (case-insensitive). Leave empty to list all."`
	LibName string `json:"lib_name" jsonschema:"If provided, list symbols inside this library (e.g. 'Device'). Combine with filter to narrow results."`
}

func (e *Env) handleListSymbolLibraries(_ context.Context, _ *mcp.CallToolRequest, input listSymbolLibrariesInput) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)
	if e.KicadSymbols == "" {
		return toolText("error: KiCad symbols directory not configured"), nil, nil
	}

	if input.LibName != "" {
		// List symbols inside a specific library.
		results, err := parts.SearchSymbols(e.KicadSymbols, input.LibName, input.Filter)
		if err != nil {
			return toolText(fmt.Sprintf("error: %v", err)), nil, nil
		}
		if len(results) == 0 {
			return toolText(fmt.Sprintf("no symbols found in %q matching %q", input.LibName, input.Filter)), nil, nil
		}
		return toolText(strings.Join(results, "\n")), nil, nil
	}

	// List library names.
	names, err := parts.ListLibraries(e.KicadSymbols, input.Filter)
	if err != nil {
		return toolText(fmt.Sprintf("error: %v", err)), nil, nil
	}
	if len(names) == 0 {
		return toolText(fmt.Sprintf("no libraries found matching %q", input.Filter)), nil, nil
	}
	return toolText(strings.Join(names, "\n")), nil, nil
}

// RegisterComponentTools registers all component management tools on the server.
func RegisterComponentTools(s *mcp.Server, env *Env) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "check_component_existence",
		Description: "Search for a component in local KiCad libraries and SnapEDA. Query format: 'LibName:PartName' (e.g. 'Device:R') or keyword. Tells you WHETHER a symbol exists; call symbol_pins to see what its pins are called.",
	}, WrapTool(env.Log, "check_component_existence", env.handleCheckComponentExistence))

	mcp.AddTool(s, &mcp.Tool{
		Name: "symbol_pins",
		Description: "List the real pins of a library symbol: number, name, electrical type and which side it points to. " +
			"Call this BEFORE writing nets in a .design.json source — KiCad's pin names are often not the datasheet's " +
			"(NE555P has THRES and ~{RST}, not THR and RESET), and guessing costs a compile cycle each time.",
	}, WrapTool(env.Log, "symbol_pins", env.handleSymbolPins))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "fetch_external_part",
		Description: "Download a component from SnapEDA by part ID and save it to libs/downloaded/.",
	}, WrapTool(env.Log, "fetch_external_part", env.handleFetchExternalPart))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "register_library",
		Description: "Add a library entry to sym-lib-table or fp-lib-table.",
	}, WrapTool(env.Log, "register_library", env.handleRegisterLibrary))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_symbol_libraries",
		Description: "Browse KiCad global symbol libraries. Without lib_name: lists all library names (optionally filtered). With lib_name: lists symbols inside that library (e.g. lib_name='Device', filter='LED' → returns 'Device:LED', 'Device:LED_ALT', etc.).",
	}, WrapTool(env.Log, "list_symbol_libraries", env.handleListSymbolLibraries))
}

// toolText creates a CallToolResult with a single text content item.
func toolText(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: text},
		},
	}
}
