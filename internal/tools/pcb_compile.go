package tools

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-kicad/internal/kicadcli"
	"mcp-kicad/internal/parts"
	"mcp-kicad/internal/pcbgen"
	"mcp-kicad/internal/sexp"
)

// PCBResult is the outcome of building a board from a schematic.
type PCBResult struct {
	PCBPath string
	PNGPath string
	Report  string
}

// CompilePCB builds <name>.kicad_pcb next to a compiled <name>.kicad_sch:
// every footprint placed without overlaps inside an outline, its pads on the
// nets KiCad itself reads from the schematic, and — when route is set — as
// much of it routed as the router can make legal. Post-condition, checked
// by KiCad: `pcb drc --schematic-parity` reports zero parity issues and no
// overlapping courtyards. The result is returned alongside the error when it
// fails, so the board can be opened and the defect diagnosed.
//
// An existing board is never overwritten unless overwrite is set: it may hold
// hours of hand routing.
func (e *Env) CompilePCB(schPath string, overwrite, route bool) (*PCBResult, error) {
	if e.KicadCLI == "" {
		return nil, fmt.Errorf("compile_pcb needs kicad-cli: it reads the netlist and checks the board with KiCad itself")
	}
	absSch, err := filepath.Abs(schPath)
	if err != nil {
		return nil, err
	}
	pcbPath := strings.TrimSuffix(absSch, ".kicad_sch") + ".kicad_pcb"
	if _, err := os.Stat(pcbPath); err == nil && !overwrite {
		return nil, fmt.Errorf("%s already exists; pass overwrite to replace it (any placement or routing done in KiCad is lost)", pcbPath)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	cli := kicadcli.New(e.KicadCLI)

	nl, err := exportNetlist(cli, absSch)
	if err != nil {
		return nil, err
	}
	seeds, err := symbolSeeds(absSch)
	if err != nil {
		return nil, err
	}

	var missing []string
	var placed []*pcbgen.Part
	cache := map[string]*pcbgen.Footprint{}
	searchPath := existingDirs(parts.FootprintSearchPath(e.LibsRoot, e.KicadFootprints))
	for _, c := range nl.Components {
		if c.Footprint == "" {
			missing = append(missing, c.Ref)
			continue
		}
		fp, ok := cache[c.Footprint]
		if !ok {
			fp, err = loadBoardFootprint(c.Footprint, searchPath)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", c.Ref, err)
			}
			cache[c.Footprint] = fp
		}
		p := &pcbgen.Part{Comp: c, FP: fp}
		p.SeedX, p.SeedY = seedOf(c, seeds)
		placed = append(placed, p)
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("no footprint on %s: declare \"footprint\" on those symbols in the source and recompile the schematic", strings.Join(missing, ", "))
	}
	if len(placed) == 0 {
		return nil, fmt.Errorf("the schematic has no parts that go on a board")
	}

	pr := pcbgen.Place(placed, nl.Nets)
	rules := pcbgen.DefaultRouteRules
	var routing *pcbgen.RouteResult
	if route {
		rr := pcbgen.Route(placed, nl.Nets, pr.Outline, rules)
		routing = &rr
	}
	// KiCad is the judge. Each variant is written and checked in turn, best
	// first, and the first one with no error-level DRC violation is kept:
	// a ground plane with thermal reliefs, then with solid pad connections
	// (a relief can be starved of spokes by the tracks around its pad), then
	// the routing without a plane, then no routing at all. The board never
	// goes out wrong; the report says what was given up and why.
	type variant struct {
		routing *pcbgen.RouteResult
		solid   bool
		label   string
	}
	var variants []variant
	if routing != nil {
		if routing.PlaneNet != "" {
			variants = append(variants, variant{routing, false, "ground plane with thermal reliefs"},
				variant{routing, true, "ground plane with solid pad connections"})
			bare := *routing
			bare.PlaneNet = ""
			variants = append(variants, variant{&bare, false, "routing without a ground plane"})
		} else {
			variants = append(variants, variant{routing, false, "routing"})
		}
	}
	variants = append(variants, variant{nil, false, "unrouted board"})
	var rep *kicadcli.BoardReport
	var discarded strings.Builder
	for i, v := range variants {
		rules.PlaneSolid = v.solid
		if rep, err = writeBoard(cli, pcbPath, placed, pr.Outline, nl, v.routing, rules); err != nil {
			return nil, err
		}
		errs := errorTypes(rep)
		if len(errs) == 0 || i == len(variants)-1 {
			routing = v.routing
			break
		}
		fmt.Fprintf(&discarded, "%s rejected by KiCad's DRC (%s); trying the next option\n", v.label, strings.Join(errs, ", "))
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "compile_pcb %s: %d footprints, board %.0f × %.0f mm\n",
		strings.TrimSuffix(filepath.Base(pcbPath), ".kicad_pcb"), len(placed), pr.Outline.W(), pr.Outline.H())
	fmt.Fprintf(&sb, "placement: schematic layout ×%.2f; %d decoupling cap(s) at their IC's power pin, %d connector(s) on the edge; ratsnest HPWL %.0f mm\n",
		pr.Scale, pr.Decaps, pr.Edge, pr.HPWL)
	sb.WriteString(discarded.String())
	if routing != nil {
		fmt.Fprintf(&sb, "routing: %d of %d connections, %d via(s), %d iteration(s); track %.2f mm, clearance %.2f mm\n",
			routing.Routed, routing.Connections, len(routing.Vias), routing.Iterations, rules.Track, rules.Clearance)
		if len(routing.PowerNets) > 0 {
			fmt.Fprintf(&sb, "  power width %.2f mm: %s\n", rules.PowerTrack, strings.Join(routing.PowerNets, ", "))
		}
		if routing.PlaneNet != "" {
			joint := "thermal reliefs"
			if rules.PlaneSolid {
				joint = "solid connections"
			}
			fmt.Fprintf(&sb, "  ground plane: %s poured over the back layer, pads on %s\n", routing.PlaneNet, joint)
		}
		if routing.Improved > 0 {
			fmt.Fprintf(&sb, "  improvement pass made %d net(s) shorter or with fewer vias/bends\n", routing.Improved)
		}
		if len(routing.Unrouted) > 0 {
			fmt.Fprintf(&sb, "  left for you to route: %s\n", strings.Join(routing.Unrouted, ", "))
		}
	}

	var defects []string
	if len(rep.Parity) == 0 {
		sb.WriteString("parity: verified — the board implements the schematic (kicad-cli --schematic-parity)\n")
	} else {
		fmt.Fprintf(&sb, "parity: FAILED, %d issue(s):\n", len(rep.Parity))
		for _, v := range rep.Parity {
			fmt.Fprintf(&sb, "  %s: %s\n", v.Type, v.Description)
		}
		defects = append(defects, fmt.Sprintf("%d schematic parity issue(s)", len(rep.Parity)))
	}
	byType := map[string]int{}
	for _, v := range rep.Violations {
		byType[v.Severity+" "+v.Type]++
		if v.Type == "courtyards_overlap" {
			defects = append(defects, "overlapping courtyards")
		}
	}
	if len(rep.Violations) == 0 {
		sb.WriteString("DRC: clean\n")
	} else {
		keys := make([]string, 0, len(byType))
		for k := range byType {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		sb.WriteString("DRC:\n")
		for _, k := range keys {
			fmt.Fprintf(&sb, "  %3d × %s\n", byType[k], k)
		}
	}
	if len(rep.Unconnected) == 0 {
		sb.WriteString("unrouted: none — every connection is copper\n")
	} else {
		fmt.Fprintf(&sb, "unrouted: %d connection(s) — finish them in KiCad (the ratsnest shows each one)\n", len(rep.Unconnected))
	}

	res := &PCBResult{PCBPath: pcbPath, Report: sb.String()}
	if p := strings.TrimSuffix(pcbPath, ".kicad_pcb") + "_pcb.png"; func() bool { _, err := os.Stat(p); return err == nil }() {
		res.PNGPath = p
	} else {
		res.Report += "render skipped: kicad-cli could not plot the board\n"
	}
	if len(defects) > 0 {
		return res, fmt.Errorf("board post-condition failed: %s", strings.Join(dedup(defects), "; "))
	}
	return res, nil
}

// writeBoard writes the board, has KiCad rewrite it in its canonical form
// (the cheapest proof that it parses, and what fills in every default we
// omit) and runs DRC with schematic parity on the result.
func writeBoard(cli *kicadcli.Runner, pcbPath string, placed []*pcbgen.Part, outline pcbgen.Box,
	nl *pcbgen.Netlist, routing *pcbgen.RouteResult, rules pcbgen.RouteRules) (*kicadcli.BoardReport, error) {
	board, err := pcbgen.BuildBoard(placed, outline, nl.PadNets(), routing, rules)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(pcbPath, []byte(board), 0o644); err != nil {
		return nil, err
	}
	if err := cli.Run("pcb", "upgrade", "--force", pcbPath); err != nil {
		return nil, fmt.Errorf("KiCad could not read the generated board: %w", err)
	}
	// The preview is drawn here, before the ground plane is filled: a filled
	// back layer paints over every back-layer track in the same colour.
	previewPath := strings.TrimSuffix(pcbPath, ".kicad_pcb") + "_pcb.png"
	os.Remove(previewPath)
	if png, err := renderBoardPNG(cli.CLIPath, pcbPath); err == nil {
		_ = os.WriteFile(previewPath, png, 0o644)
	}
	return cli.BoardDRC(pcbPath, routing != nil && routing.PlaneNet != "")
}

func exportNetlist(cli *kicadcli.Runner, schPath string) (*pcbgen.Netlist, error) {
	tmp, err := os.CreateTemp("", "mcp-kicad-*.net")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)
	if err := cli.Run("sch", "export", "netlist", "-o", tmpPath, schPath); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(tmpPath)
	if err != nil {
		return nil, err
	}
	return pcbgen.ParseNetlist(string(data))
}

// symbolSeeds maps every schematic symbol UUID to its position.
func symbolSeeds(schPath string) (map[string][2]float64, error) {
	data, err := os.ReadFile(schPath)
	if err != nil {
		return nil, err
	}
	sch, err := sexp.ParseSchematic(string(data))
	if err != nil {
		return nil, err
	}
	out := map[string][2]float64{}
	for _, n := range sch.Root().Children {
		if n.Head() != "symbol" {
			continue
		}
		at := sexp.FindList(n, "at")
		if at == nil || len(at.Children) < 3 {
			continue
		}
		x, _ := strconv.ParseFloat(at.Children[1].Value, 64)
		y, _ := strconv.ParseFloat(at.Children[2].Value, 64)
		out[sexp.StringValue(sexp.FindList(n, "uuid"), 1)] = [2]float64{x, y}
	}
	return out, nil
}

// seedOf averages the positions of a part's units: a dual op-amp drawn as two
// symbols belongs between them.
func seedOf(c pcbgen.Component, seeds map[string][2]float64) (float64, float64) {
	var x, y float64
	n := 0
	for _, u := range c.UUIDs {
		if s, ok := seeds[u]; ok {
			x += s[0]
			y += s[1]
			n++
		}
	}
	if n == 0 {
		return 0, 0
	}
	return x / float64(n), y / float64(n)
}

func loadBoardFootprint(libID string, searchPath []string) (*pcbgen.Footprint, error) {
	lib, name, ok := strings.Cut(libID, ":")
	if !ok {
		return nil, fmt.Errorf("footprint %q is not Library:Name", libID)
	}
	for _, dir := range searchPath {
		data, err := os.ReadFile(filepath.Join(dir, lib+".pretty", name+".kicad_mod"))
		if err == nil {
			return pcbgen.LoadFootprint(libID, string(data))
		}
	}
	return nil, fmt.Errorf("footprint %s not found in the installed or imported libraries", libID)
}

func dedup(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// renderBoardPNG plots the copper, silkscreen, courtyards and edge of the
// board to SVG (board area only) and rasterises it like the schematic preview.
func renderBoardPNG(kicadCLI, pcbPath string) ([]byte, error) {
	svgPath := strings.TrimSuffix(pcbPath, ".kicad_pcb") + "_pcb.svg"
	defer os.Remove(svgPath)
	cmd := exec.Command(kicadCLI, "pcb", "export", "svg",
		"--mode-single", "-o", svgPath,
		"--layers", "Edge.Cuts,F.Cu,B.Cu,F.SilkS,F.CrtYd",
		"--page-size-mode", "2", "--exclude-drawing-sheet",
		pcbPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("kicad-cli pcb export svg: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return svgToPNG(svgPath)
}

// CompilePCBArgs is the input of the compile_pcb tool.
type CompilePCBArgs struct {
	SchematicPath string `json:"schematic_path" jsonschema:"The .kicad_sch produced by compile_schematic. The board is written next to it as <name>.kicad_pcb"`
	Overwrite     bool   `json:"overwrite,omitempty" jsonschema:"Replace an existing .kicad_pcb. Off by default: the existing board may hold placement or routing done by hand in KiCad, and this discards it"`
	NoRoute       bool   `json:"no_route,omitempty" jsonschema:"Place only, leave every connection to route by hand"`
}

func (e *Env) handleCompilePCB(_ context.Context, _ *mcp.CallToolRequest, input CompilePCBArgs) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)
	if input.SchematicPath == "" {
		return toolText("error: schematic_path is required (the .kicad_sch from compile_schematic)"), nil, nil
	}
	out, err := e.CompilePCB(input.SchematicPath, input.Overwrite, !input.NoRoute)
	if err != nil && out == nil {
		return toolText(fmt.Sprintf("compile_pcb error: %v", err)), nil, nil
	}
	report := out.Report + "\nboard: " + out.PCBPath +
		"\nopen the .kicad_pro in KiCad to route it; after changing the schematic, use Tools > Update PCB from Schematic (F8) to keep your placement, or recompile with overwrite to start over"
	if err != nil {
		report = fmt.Sprintf("COMPILE FAILED: %v\n\n%s", err, report)
	}
	result := toolText(report)
	if out.PNGPath != "" {
		if png, readErr := os.ReadFile(out.PNGPath); readErr == nil {
			result.Content = append([]mcp.Content{&mcp.ImageContent{Data: png, MIMEType: "image/png"}}, result.Content...)
		}
	}
	return result, nil, nil
}

// errorTypes lists the error-level DRC violations of a board, deduplicated.
// Each entry names the type and the items involved, so the report says
// which pad, not only which rule.
func errorTypes(rep *kicadcli.BoardReport) []string {
	var out []string
	for _, v := range rep.Violations {
		if v.Severity != "error" {
			continue
		}
		e := v.Type
		if len(v.Items) > 0 {
			e += ": " + strings.Join(v.Items, " / ")
		}
		out = append(out, e)
	}
	return dedup(out)
}
