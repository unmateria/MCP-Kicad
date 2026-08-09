package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/ini.v1"

	"mcp-kicad/internal/kicadcli"
	"mcp-kicad/internal/parts"
)

// outputDirMu guards concurrent access to env.OutputDir and config writes.
var outputDirMu sync.Mutex

// validateDesignInput defines the parameters for the validate_design tool.
type validateDesignInput struct {
	SchematicPath string `json:"schematic_path,omitempty" jsonschema:"Path to .kicad_sch file for ERC. Give this, pcb_path, or both."`
	PCBPath       string `json:"pcb_path,omitempty"       jsonschema:"Path to .kicad_pcb file for DRC. Give this, schematic_path, or both."`
}

// getProjectInfoInput is intentionally empty — it returns server/env metadata.
type getProjectInfoInput struct{}

func (e *Env) handleValidateDesign(_ context.Context, _ *mcp.CallToolRequest, input validateDesignInput) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)
	if input.SchematicPath == "" && input.PCBPath == "" {
		return toolText("error: at least one of schematic_path or pcb_path is required"), nil, nil
	}

	runner := kicadcli.New(e.KicadCLI)
	var sb strings.Builder

	if input.SchematicPath != "" {
		sb.WriteString(e.runERC(runner, input.SchematicPath))
	}
	if input.PCBPath != "" {
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		res, err := runner.DRC(input.PCBPath)
		if err != nil {
			fmt.Fprintf(&sb, "DRC error: %v\n", err)
		}
		classified := kicadcli.ClassifyViolations(res.Violations, res.Stderr)
		sb.WriteString(kicadcli.FormatViolations(classified))
	}

	return toolText(strings.TrimRight(sb.String(), "\n")), nil, nil
}

// runERC runs ERC on a schematic and returns the formatted result string.
// Used both by validate_design and by auto-validation in modify_schematic.
func (e *Env) runERC(runner *kicadcli.Runner, schPath string) string {
	res, err := runner.ERC(schPath)
	stderr := ""
	if err != nil {
		stderr = err.Error()
	} else {
		stderr = res.Stderr
	}
	classified := kicadcli.ClassifyViolations(res.Violations, stderr)
	return kicadcli.FormatViolations(classified)
}

// AutoValidateSCH runs ERC on the schematic and returns a compact result.
// Returns "" if kicad-cli is not configured.
func (e *Env) AutoValidateSCH(schPath string) string {
	if e.KicadCLI == "" {
		return ""
	}
	return e.runERC(kicadcli.New(e.KicadCLI), schPath)
}

func (e *Env) handleGetProjectInfo(_ context.Context, _ *mcp.CallToolRequest, _ getProjectInfoInput) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)
	cwd, _ := os.Getwd()
	logPath := ""
	if e.Log != nil {
		logPath = e.Log.Path()
	}
	info := fmt.Sprintf(
		"MCP-KiCad server\nworking_dir: %s\noutput_dir: %s\nlog_file: %s\nlibs root: %s\nkicad-cli: %s\nkicad-symbols: %s\nkicad-footprints: %s\nimported symbols: %s\nimported footprints: %s\ndistributor keys: mouser=%v digikey=%v\nconfig.ini: %s",
		cwd,
		e.OutputDir,
		logPath,
		e.LibsRoot,
		e.KicadCLI,
		e.KicadSymbols,
		e.KicadFootprints,
		parts.ImportedSymbolLib(e.LibsRoot),
		parts.ImportedFootprintLib(e.LibsRoot),
		e.Mouser != "",
		e.DigiKeyID != "" && e.DigiKeySecret != "",
		e.ConfigPath,
	)
	return toolText(info), nil, nil
}

// --- get_output_dir ---

type getOutputDirInput struct{}

func (e *Env) handleGetOutputDir(_ context.Context, _ *mcp.CallToolRequest, _ getOutputDirInput) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)
	outputDirMu.Lock()
	dir := e.OutputDir
	outputDirMu.Unlock()

	absDir, _ := filepath.Abs(dir)
	var sb strings.Builder
	fmt.Fprintf(&sb, "output_dir: %s\n", absDir)

	entries, err := os.ReadDir(absDir)
	if err != nil {
		fmt.Fprintf(&sb, "status: directory does not exist or is not readable (%v)\n", err)
		return toolText(sb.String()), nil, nil
	}

	// Collect files sorted by mod time descending.
	type fileInfo struct {
		name    string
		size    int64
		modTime string
	}
	var files []fileInfo
	for _, de := range entries {
		if de.IsDir() {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		files = append(files, fileInfo{
			name:    de.Name(),
			size:    info.Size(),
			modTime: info.ModTime().Format("2006-01-02 15:04:05"),
		})
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime > files[j].modTime
	})
	if len(files) == 0 {
		sb.WriteString("status: directory exists but is empty\n")
	} else {
		fmt.Fprintf(&sb, "status: %d file(s) — last 5:\n", len(files))
		limit := 5
		if len(files) < limit {
			limit = len(files)
		}
		for _, f := range files[:limit] {
			fmt.Fprintf(&sb, "  %-40s  %8d bytes  %s\n", f.name, f.size, f.modTime)
		}
	}
	return toolText(strings.TrimRight(sb.String(), "\n")), nil, nil
}

// --- set_output_dir ---

type setOutputDirInput struct {
	Path string `json:"path" jsonschema:"New output directory (absolute or relative to server working dir)"`
}

func (e *Env) handleSetOutputDir(_ context.Context, _ *mcp.CallToolRequest, input setOutputDirInput) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)
	if input.Path == "" {
		return toolText("error: path is required"), nil, nil
	}

	absPath, err := filepath.Abs(input.Path)
	if err != nil {
		return toolText(fmt.Sprintf("error resolving path: %v", err)), nil, nil
	}
	if err := os.MkdirAll(absPath, 0o755); err != nil {
		return toolText(fmt.Sprintf("error creating directory: %v", err)), nil, nil
	}

	// Persist to config.ini.
	outputDirMu.Lock()
	defer outputDirMu.Unlock()

	if e.ConfigPath != "" {
		var cfg *ini.File
		cfg, err = ini.Load(e.ConfigPath)
		if err != nil {
			cfg = ini.Empty()
		}
		cfg.Section("paths").Key("output_dir").SetValue(absPath)
		if err := cfg.SaveTo(e.ConfigPath); err != nil {
			return toolText(fmt.Sprintf("error saving config.ini: %v", err)), nil, nil
		}
	}

	e.OutputDir = absPath
	return toolText(fmt.Sprintf("output_dir set to: %s\nconfig.ini updated: %s", absPath, e.ConfigPath)), nil, nil
}

// RegisterValidationTools registers ERC/DRC and info tools on the server.
func RegisterValidationTools(s *mcp.Server, env *Env) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "validate_design",
		Description: "Run ERC on a schematic and/or DRC on a PCB. Violations are classified as [MCP BUG] (internal error) or [FIXABLE] (LLM can resolve).",
	}, WrapTool(env.Log, "validate_design", env.handleValidateDesign))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_project_info",
		Description: "Return server configuration: working directory, output_dir, log file path, kicad-cli path, symbol/footprint library paths. Use this to find the correct absolute path for schematic files.",
	}, WrapTool(env.Log, "get_project_info", env.handleGetProjectInfo))

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_output_dir",
		Description: "Return the current output directory where the MCP server writes generated files " +
			"(schematics, images, exports). Also lists the 5 most recent files in that directory.",
	}, WrapTool(env.Log, "get_output_dir", env.handleGetOutputDir))

	mcp.AddTool(s, &mcp.Tool{
		Name: "set_output_dir",
		Description: "Change the output directory where generated files are written. " +
			"Creates the directory if it does not exist and persists the setting to config.ini " +
			"so it survives server restarts. Takes effect immediately without restarting.",
	}, WrapTool(env.Log, "set_output_dir", env.handleSetOutputDir))
}
