package tools

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-kicad/internal/kicadcli"
)

// fabLayers are the layers a two-layer board house needs.
const fabLayers = "F.Cu,B.Cu,F.Paste,B.Paste,F.SilkS,B.SilkS,F.Mask,B.Mask,Edge.Cuts"

// FabResult is what ExportFabrication wrote.
type FabResult struct {
	Dir    string
	Zip    string
	Report string
}

// ExportFabrication writes everything a board house and an assembler need,
// next to the board in <name>_fab/: Gerbers, Excellon drill files and drill
// map, a pick-and-place file, a BOM, and <name>_gerbers.zip ready to upload.
//
// Precondition, checked by KiCad: DRC with schematic parity finds no
// error-level violation and no parity issue, and nothing is left unrouted
// unless allowUnrouted is set. Fabricating a board that does not implement
// its schematic is the one mistake that costs money and weeks.
func (e *Env) ExportFabrication(pcbPath string, allowUnrouted bool) (*FabResult, error) {
	if e.KicadCLI == "" {
		return nil, fmt.Errorf("export_fabrication needs kicad-cli")
	}
	abs, err := filepath.Abs(pcbPath)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(abs, ".kicad_pcb")
	name := filepath.Base(base)
	schPath := base + ".kicad_sch"
	cli := kicadcli.New(e.KicadCLI)

	rep, err := cli.BoardDRC(abs, false)
	if err != nil {
		return nil, err
	}
	var problems []string
	if errs := errorTypes(rep); len(errs) > 0 {
		problems = append(problems, fmt.Sprintf("%d DRC error(s): %s", len(errs), strings.Join(errs, "; ")))
	}
	if len(rep.Parity) > 0 {
		problems = append(problems, fmt.Sprintf("%d schematic parity issue(s): the board does not implement %s", len(rep.Parity), filepath.Base(schPath)))
	}
	if len(rep.Unconnected) > 0 && !allowUnrouted {
		problems = append(problems, fmt.Sprintf("%d connection(s) not routed (pass allow_unrouted to export anyway)", len(rep.Unconnected)))
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("not exporting %s: %s", filepath.Base(abs), strings.Join(problems, "; "))
	}

	dir := base + "_fab"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	steps := [][]string{
		{"pcb", "export", "gerbers", "-o", dir + string(os.PathSeparator), "--layers", fabLayers, "--check-zones", abs},
		{"pcb", "export", "drill", "-o", dir + string(os.PathSeparator), "--format", "excellon", "--excellon-units", "mm",
			"--generate-map", "--map-format", "gerberx2", abs},
		{"pcb", "export", "pos", "-o", filepath.Join(dir, name+"-pos.csv"), "--format", "csv", "--units", "mm", "--side", "both", abs},
	}
	if _, err := os.Stat(schPath); err == nil {
		steps = append(steps, []string{"sch", "export", "bom", "-o", filepath.Join(dir, name+"-bom.csv"),
			"--fields", "Reference,Value,Footprint,${QUANTITY}", "--labels", "Refs,Value,Footprint,Qty",
			"--group-by", "Value,Footprint", "--ref-range-delimiter", "", schPath})
	}
	for _, args := range steps {
		if err := cli.Run(args...); err != nil {
			return nil, err
		}
	}

	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names, fab []string
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		names = append(names, f.Name())
		switch strings.ToLower(filepath.Ext(f.Name())) {
		case ".gbr", ".drl", ".gbrjob", ".gtl", ".gbl", ".gto", ".gbo", ".gts", ".gbs", ".gtp", ".gbp", ".gm1":
			fab = append(fab, f.Name())
		}
	}
	sort.Strings(names)
	sort.Strings(fab)
	if len(fab) == 0 {
		return nil, fmt.Errorf("kicad-cli wrote no Gerber or drill file into %s", dir)
	}
	zipPath := filepath.Join(dir, name+"_gerbers.zip")
	if err := zipFiles(zipPath, dir, fab); err != nil {
		return nil, err
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "export_fabrication %s: DRC clean, parity verified", name)
	if len(rep.Unconnected) > 0 {
		fmt.Fprintf(&sb, ", %d connection(s) NOT routed (exported because allow_unrouted was set)", len(rep.Unconnected))
	}
	fmt.Fprintf(&sb, "\nfolder: %s\n", dir)
	for _, n := range names {
		fmt.Fprintf(&sb, "  %s\n", n)
	}
	fmt.Fprintf(&sb, "upload to the board house: %s (%d Gerber and drill files)\n", filepath.Base(zipPath), len(fab))
	return &FabResult{Dir: dir, Zip: zipPath, Report: sb.String()}, nil
}

func zipFiles(zipPath, dir string, names []string) error {
	out, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(out)
	for _, n := range names {
		w, err := zw.Create(n)
		if err != nil {
			out.Close()
			return err
		}
		in, err := os.Open(filepath.Join(dir, n))
		if err != nil {
			out.Close()
			return err
		}
		_, err = io.Copy(w, in)
		in.Close()
		if err != nil {
			out.Close()
			return err
		}
	}
	if err := zw.Close(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// ExportFabricationArgs is the input of the export_fabrication tool.
type ExportFabricationArgs struct {
	PCBPath       string `json:"pcb_path" jsonschema:"The .kicad_pcb to fabricate, e.g. the one compile_pcb wrote"`
	AllowUnrouted bool   `json:"allow_unrouted,omitempty" jsonschema:"Export even with connections left unrouted. Off by default: such a board does not work"`
}

func (e *Env) handleExportFabrication(_ context.Context, _ *mcp.CallToolRequest, input ExportFabricationArgs) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)
	if input.PCBPath == "" {
		return toolText("error: pcb_path is required"), nil, nil
	}
	out, err := e.ExportFabrication(input.PCBPath, input.AllowUnrouted)
	if err != nil {
		return toolText(fmt.Sprintf("export_fabrication error: %v", err)), nil, nil
	}
	return toolText(out.Report), nil, nil
}
