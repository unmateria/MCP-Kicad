package tools

import (
	"bytes"
	"context"
	"encoding/json"
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

// PCBOptions are the knobs of CompilePCB.
type PCBOptions struct {
	Overwrite bool               // replace an existing board
	Route     bool               // route it, not only place it
	NetWidths map[string]float64 // track width per net name, mm
	// SingleSided routes everything on B.Cu, with wire jumpers where it
	// must, and grows through-hole pads for milling. Only through-hole parts.
	SingleSided bool
	// Track, PowerTrack and Clearance override the copper rules, mm; 0 keeps
	// the default (single-sided: 1.0 / 2.0 / 0.6, sized for a home CNC).
	Track, PowerTrack, Clearance float64
}

// rules turns the options into router rules.
func (opt PCBOptions) rules() pcbgen.RouteRules {
	r := pcbgen.DefaultRouteRules
	if opt.SingleSided {
		r.Track, r.PowerTrack, r.Clearance = 1.0, 2.0, 0.6
		// A via is a jumper's hole: drilled for a 0.6 mm wire, with a ring a
		// milled board can solder.
		r.Via, r.Drill, r.Edge, r.HoleToHole = 2.4, 0.9, 1.0, 0.5
		r.SingleSided = true
	}
	if opt.Track > 0 {
		r.Track = opt.Track
	}
	if opt.PowerTrack > 0 {
		r.PowerTrack = opt.PowerTrack
	}
	if opt.Clearance > 0 {
		r.Clearance = opt.Clearance
	}
	r.NetWidths = opt.NetWidths
	return r
}

// millingPad is the smallest through-hole pad on a single-sided board.
const millingPad = 2.4

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
func (e *Env) CompilePCB(schPath string, opt PCBOptions) (*PCBResult, error) {
	if e.KicadCLI == "" {
		return nil, fmt.Errorf("compile_pcb needs kicad-cli: it reads the netlist and checks the board with KiCad itself")
	}
	absSch, err := filepath.Abs(schPath)
	if err != nil {
		return nil, err
	}
	pcbPath := strings.TrimSuffix(absSch, ".kicad_sch") + ".kicad_pcb"
	if _, err := os.Stat(pcbPath); err == nil && !opt.Overwrite {
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

	rules := opt.rules()
	var missing, smd []string
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
			if opt.SingleSided {
				// Room between parts for one wide track and its clearances.
				grow := (rules.Track + 2*rules.Clearance) / 2
				if fp, err = fp.ForMilling(millingPad, rules.Clearance, grow); err != nil {
					return nil, fmt.Errorf("%s: %w", c.Ref, err)
				}
			}
			cache[c.Footprint] = fp
		}
		if opt.SingleSided && hasSMDPad(fp) {
			smd = append(smd, c.Ref+" ("+c.Footprint+")")
		}
		p := &pcbgen.Part{Comp: c, FP: fp}
		p.SeedX, p.SeedY = seedOf(c, seeds)
		placed = append(placed, p)
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("no footprint on %s: declare \"footprint\" on those symbols in the source and recompile the schematic", strings.Join(missing, ", "))
	}
	if len(smd) > 0 {
		sort.Strings(smd)
		return nil, fmt.Errorf("single_sided takes through-hole parts only; these have SMD pads: %s — give them a through-hole footprint in the source and recompile the schematic", strings.Join(smd, ", "))
	}
	if len(placed) == 0 {
		return nil, fmt.Errorf("the schematic has no parts that go on a board")
	}

	if rules.Clearance != pcbgen.DefaultRouteRules.Clearance || rules.Track != pcbgen.DefaultRouteRules.Track {
		// DRC judges by the project's netclass: it has to carry the rules the
		// board was routed to, or KiCad checks a 0.6 mm board against 0.2.
		if err := setProjectNetclass(strings.TrimSuffix(pcbPath, ".kicad_pcb")+".kicad_pro", rules); err != nil {
			return nil, err
		}
	}
	pr := pcbgen.Place(placed, nl.Nets)
	var routing *pcbgen.RouteResult
	if opt.Route {
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
	if opt.SingleSided {
		fmt.Fprintf(&sb, "single-sided: every track on B.Cu, through-hole pads grown to %.1f mm where their neighbours allow\n", millingPad)
	}
	if routing != nil && opt.SingleSided {
		fmt.Fprintf(&sb, "routing: %d of %d connections, %d wire jumper(s) (F.Cu track + silkscreen line, holes %.1f mm), %d iteration(s); track %.2f mm, clearance %.2f mm\n",
			routing.Routed, routing.Connections, len(routing.Vias)/2, rules.Drill, routing.Iterations, rules.Track, rules.Clearance)
	} else if routing != nil {
		fmt.Fprintf(&sb, "routing: %d of %d connections, %d via(s), %d iteration(s); track %.2f mm, clearance %.2f mm\n",
			routing.Routed, routing.Connections, len(routing.Vias), routing.Iterations, rules.Track, rules.Clearance)
	}
	if routing != nil {
		if len(routing.WideNets) > 0 {
			fmt.Fprintf(&sb, "  wide tracks: %s\n", strings.Join(routing.WideNets, ", "))
		}
		if len(routing.Narrowed) > 0 {
			fmt.Fprintf(&sb, "  did not fit at power width, routed at %.2f mm: %s\n", rules.Track, strings.Join(routing.Narrowed, ", "))
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
		var left []string
		for _, n := range routing.Unrouted {
			// The pour finishes what the tracks left of its own net; KiCad's
			// unconnected count below is the authority on whether it did.
			if n == routing.PlaneNet && len(rep.Unconnected) == 0 {
				fmt.Fprintf(&sb, "  %s: the ground plane completes the connections tracks did not make\n", n)
				continue
			}
			left = append(left, n)
		}
		if len(left) > 0 {
			fmt.Fprintf(&sb, "  left for you to route: %s\n", strings.Join(left, ", "))
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

func hasSMDPad(fp *pcbgen.Footprint) bool {
	for _, ps := range fp.PadShapes {
		if ps.Drill == 0 && (ps.Front || ps.Back) {
			return true
		}
	}
	return false
}

// setProjectNetclass writes the board's copper rules into the Default
// netclass of the .kicad_pro, so KiCad's DRC and its interactive router use
// them. The rest of the file is kept; a missing file is left to KiCad.
func setProjectNetclass(proPath string, r pcbgen.RouteRules) error {
	data, err := os.ReadFile(proPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	var pro map[string]any
	if err := json.Unmarshal(data, &pro); err != nil {
		return fmt.Errorf("%s: %w", proPath, err)
	}
	ns, _ := pro["net_settings"].(map[string]any)
	classes, _ := ns["classes"].([]any)
	for _, c := range classes {
		cls, ok := c.(map[string]any)
		if !ok || cls["name"] != "Default" {
			continue
		}
		cls["clearance"], cls["track_width"] = r.Clearance, r.Track
		cls["via_diameter"], cls["via_drill"] = r.Via, r.Drill
		var out bytes.Buffer
		enc := json.NewEncoder(&out)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(pro); err != nil {
			return err
		}
		return os.WriteFile(proPath, out.Bytes(), 0o644)
	}
	return nil
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
	SchematicPath string             `json:"schematic_path" jsonschema:"The .kicad_sch produced by compile_schematic. The board is written next to it as <name>.kicad_pcb"`
	Overwrite     bool               `json:"overwrite,omitempty" jsonschema:"Replace an existing .kicad_pcb. Off by default: the existing board may hold placement or routing done by hand in KiCad, and this discards it"`
	NoRoute       bool               `json:"no_route,omitempty" jsonschema:"Place only, leave every connection to route by hand"`
	NetWidths     map[string]float64 `json:"net_widths,omitempty" jsonschema:"Track width in mm per net, for nets that carry current: by net name (e.g. {\"HEATER\": 2.0}) or by any pin on the net as REF.pin (e.g. {\"F1.1\": 2.0}), which also reaches nets KiCad named itself. Without it supply and ground nets get 0.5 mm and the rest 0.25 mm. A requested width is never narrowed: a net that does not fit is left unrouted and reported"`
	SingleSided   bool               `json:"single_sided,omitempty" jsonschema:"Single-sided board for home making (CNC milling, toner transfer): through-hole parts only, every track on B.Cu where they are soldered, wire jumpers over the top only where a connection cannot be made (drawn as F.Cu tracks plus silkscreen lines, holes as vias), through-hole pads grown to 2.4 mm where neighbours allow, more room between parts. Defaults to track 1.0, power 2.0, clearance 0.6 mm"`
	TrackWidth    float64            `json:"track_width,omitempty" jsonschema:"Signal track width in mm (default 0.25; 1.0 with single_sided)"`
	PowerWidth    float64            `json:"power_width,omitempty" jsonschema:"Supply and ground track width in mm, where the pads allow it (default 0.5; 2.0 with single_sided)"`
	Clearance     float64            `json:"clearance,omitempty" jsonschema:"Copper-to-copper clearance in mm (default 0.2; 0.6 with single_sided). Written into the project's Default netclass so KiCad's DRC checks the board against it"`
}

func (e *Env) handleCompilePCB(_ context.Context, _ *mcp.CallToolRequest, input CompilePCBArgs) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)
	if input.SchematicPath == "" {
		return toolText("error: schematic_path is required (the .kicad_sch from compile_schematic)"), nil, nil
	}
	out, err := e.CompilePCB(input.SchematicPath, PCBOptions{Overwrite: input.Overwrite, Route: !input.NoRoute, NetWidths: input.NetWidths,
		SingleSided: input.SingleSided, Track: input.TrackWidth, PowerTrack: input.PowerWidth, Clearance: input.Clearance})
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
