package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-kicad/internal/compile"
	"mcp-kicad/internal/place2/cluster"
	"mcp-kicad/internal/place2/gate"
	"mcp-kicad/internal/place2/metrics"
	"mcp-kicad/internal/place2/templates"
	"mcp-kicad/internal/place2/textplace"
	"mcp-kicad/internal/place2/weld"
	"mcp-kicad/internal/place2/wiregen"
	"mcp-kicad/internal/router"
	"mcp-kicad/internal/sexp"
)

// CompileResult is the outcome of compiling one .design.json source.
type CompileResult struct {
	SchematicPath string
	PNGPath       string
	Report        string
	NetDefects    []NetDefect // non-empty means the emitted netlist != the declared one
}

// CompileDesign compiles a .design.json source into a complete schematic:
// parse → resolve placement → stamp templates + emit symbols → wiregen →
// route/label → gate → power flags → no_connect → sheet fit → ERC → render.
// outSchPath == "" defaults to <design dir>/<project>.kicad_sch.
func (e *Env) CompileDesign(designPath, outSchPath string) (*CompileResult, error) {
	d, err := compile.ParseDesignFile(designPath)
	if err != nil {
		return nil, err
	}
	for _, blk := range d.Blocks {
		for _, s := range blk.Symbols {
			if s.Mirror {
				return nil, fmt.Errorf("block %s: symbol %s: mirror is not supported yet", blk.Name, s.Ref)
			}
		}
	}
	if outSchPath == "" {
		outSchPath = filepath.Join(filepath.Dir(designPath), d.Project+".kicad_sch")
	}
	absOut, err := filepath.Abs(outSchPath)
	if err != nil {
		return nil, err
	}

	sg, err := e.newLibGeom()
	if err != nil {
		return nil, err
	}
	layout, err := compile.Resolve(d, sg, tmplGeom{})
	if err != nil {
		return nil, err
	}

	sch, err := newEmptySchematic()
	if err != nil {
		return nil, err
	}
	if d.Sheet == "A3" {
		sch.SetPaper("A3")
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "compile %s: %d blocks\n", d.Project, len(layout.Blocks))

	blockByName := make(map[string]compile.Block, len(d.Blocks))
	for _, blk := range d.Blocks {
		blockByName[blk.Name] = blk
	}

	for _, pb := range layout.Blocks {
		blk := blockByName[pb.Name]
		if blk.Template != "" {
			tpl, err := templates.Get(blk.Template)
			if err != nil {
				return nil, fmt.Errorf("block %s: %w", pb.Name, err)
			}
			pinMap := map[string]string{}
			for _, ep := range tpl.ExternalPins {
				if net, ok := blk.Connect[ep.Label]; ok {
					pinMap[ep.From] = net
				}
			}
			res, err := templates.Stamp(sch, tpl, templates.StampOptions{
				Anchor:    [2]float64{pb.OriginX, pb.OriginY},
				RefMap:    blk.Refs,
				PinMap:    pinMap,
				EmbedFunc: func(libID string) error { return e.embedLibSymbol(sch, libID) },
				PowerLibFor: func(net string) string {
					if lib, ok := d.PowerNets[net]; ok {
						return lib
					}
					return netNameToPowerLibID(net)
				},
			})
			if err != nil {
				return nil, fmt.Errorf("block %s: stamp %s: %w", pb.Name, blk.Template, err)
			}
			fmt.Fprintf(&sb, "  block %-8s template %s: %d symbols, %d wires\n",
				pb.Name, blk.Template, len(res.PlacedRefs), res.WiresAdded)
		} else {
			for _, ps := range pb.Symbols {
				if err := e.embedLibSymbol(sch, ps.LibID); err != nil {
					return nil, fmt.Errorf("block %s: %s: %w", pb.Name, ps.Ref, err)
				}
				unit := ps.Unit
				if unit < 1 {
					unit = 1
				}
				pinNums := extractPinNumbers(sch, ps.LibID, unit)
				libDef := sch.FindLibDef(ps.LibID)
				sch.AddSymbol(sexp.NewSymbolInstance(ps.LibID, ps.Ref, ps.Value, "",
					ps.X, ps.Y, float64(ps.Rot), unit, pinNums, sch.UUID(), true, true, libDef))
				sexp.FixLabelPositions(sch, ps.Ref)
			}
			fmt.Fprintf(&sb, "  block %-8s %d symbols\n", pb.Name, len(pb.Symbols))
		}
	}

	// Connection table, net names sorted for determinism. Power nets whose
	// name routeNets already recognises flow through it (per-pin power policy);
	// the rest of the declared power nets are emitted here from the explicit
	// power_nets mapping and skipped by the router.
	netNames := make([]string, 0, len(d.Nets))
	for name := range d.Nets {
		netNames = append(netNames, name)
	}
	sort.Strings(netNames)

	manualPower := map[string]string{}
	pwrNames := make([]string, 0, len(d.PowerNets))
	for name := range d.PowerNets {
		pwrNames = append(pwrNames, name)
	}
	sort.Strings(pwrNames)
	for _, name := range pwrNames {
		if netNameToPowerLibID(name) == "" {
			manualPower[name] = d.PowerNets[name]
		}
	}

	// Single-pin signal nets connect by name against a label somewhere else
	// (typically a template's external pin): place the label directly. Routing
	// them is impossible and leaving them bare would get them auto-no_connected.
	var conns []NetConn
	for _, name := range netNames {
		if _, manual := manualPower[name]; manual {
			continue
		}
		pins := d.Nets[name]
		if len(pins) == 1 && netNameToPowerLibID(name) == "" {
			pin, ok := sexp.FindPin(sch, pins[0])
			if !ok {
				return nil, fmt.Errorf("net %s: pin %s not found", name, pins[0])
			}
			sch.AddLabel(sexp.NewNetLabel(name, pin.X, pin.Y, labelRotForDir(pin.Direction)))
			fmt.Fprintf(&sb, "  %-20s [label]  %s — single-pin net labeled\n", name, pins[0])
			continue
		}
		conns = append(conns, NetConn{Net: name, Pins: pins})
	}

	// Wiregen pre-pass: closed-form cluster wiring, no repositioning (the
	// author owns placement in this pipeline, always).
	var compForNet map[string]map[string]int
	wiregenWires := 0
	wNets, sNets := buildWiregenInputs(sch, conns)
	clusters := cluster.Detect(sexp.ReadSymbols(sch), sNets)
	wres := wiregen.ApplyOpts(sch, clusters, wNets, false)
	if !wres.Empty() {
		compForNet = buildCompForNet(wres.Pairs)
		wiregenWires = len(wres.Wires)
		fmt.Fprintf(&sb, "  %s\n", wres.ReportLine())
	}

	if len(manualPower) > 0 {
		em := e.NewPowerEmitter(sch)
		for _, name := range pwrNames {
			libID, ok := manualPower[name]
			if !ok {
				continue
			}
			for _, pin := range d.Nets[name] {
				if msg, ok, _ := em.Emit(libID, pin); !ok {
					fmt.Fprintf(&sb, "  power %s at %s: %s\n", libID, pin, msg)
				}
			}
		}
	}

	rt := router.NewRouter(sexp.ReadSymbols(sch), sch.Wires())
	totalWires, totalLabels, totalErrors := e.routeNets(sch, rt, conns, "auto", compForNet, &sb)
	totalWires += wiregenWires

	gateResult := gate.Enforce(sch)
	// Weld after the gate: label pairs that survive with a clean corridor
	// between them become real wires — humans read wires, not tag pairs.
	// Every candidate is validated against gate.Check, so this can never
	// reintroduce a violation the gate just removed.
	weldResult := weld.Weld(sch)
	e.ensurePowerFlags(sch)
	fmt.Fprintf(&sb, "\n%s\n", gateResult.String())
	if weldResult.Welded+weldResult.LabelsRemoved > 0 {
		fmt.Fprintf(&sb, "%s\n", weldResult.String())
	}
	fmt.Fprintf(&sb, "routing: %d wire segments, %d labels, %d errors\n", totalWires, totalLabels, totalErrors)

	if stripped := stripQuietLabels(sch, d); len(stripped) > 0 {
		fmt.Fprintf(&sb, "quiet nets: label removed for %s\n", strings.Join(stripped, ", "))
	}

	autoNC := e.applyNoConnects(sch, d, &sb)
	if len(autoNC) > 0 {
		fmt.Fprintf(&sb, "auto no_connect (%d pins): %s\n", len(autoNC), strings.Join(autoNC, " "))
	}

	if moved, flipped := textplace.Autoplace(sch); moved+flipped > 0 {
		fmt.Fprintf(&sb, "textplace: %d fields repositioned, %d labels flipped\n", moved, flipped)
	}
	if cols := textplace.Collisions(sch); len(cols) > 0 {
		total := 0.0
		for _, c := range cols {
			total += c.Area
		}
		fmt.Fprintf(&sb, "text: %d residual collision(s), %.1f mm2 — worst %s\n", len(cols), total, cols[0])
	}

	if note := fitToSheet(sch); note != "" {
		fmt.Fprintf(&sb, "sheet: %s\n", note)
	}

	// The compiler's post-condition, checked on the finished schematic: what
	// we emitted implements exactly the netlist the source declared. The
	// geometric gate cannot stand in for this — a wire landing on a foreign
	// pin makes two nets one, which is geometrically consistent and silent.
	defects := VerifyNetlist(sch, d)
	switch {
	case len(d.Nets) == 0:
		fmt.Fprintf(&sb, "netlist: nothing to verify — the source declares no nets\n")
	case len(defects) == 0:
		fmt.Fprintf(&sb, "netlist: verified — %d declared nets implemented exactly\n", len(d.Nets))
	default:
		fmt.Fprintf(&sb, "netlist: FAILED — %d defect(s)\n", len(defects))
		for _, def := range defects {
			fmt.Fprintf(&sb, "  %s\n", def)
		}
	}

	if err := os.MkdirAll(filepath.Dir(absOut), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(absOut, []byte(sch.Serialize()), 0o644); err != nil {
		return nil, err
	}

	m := metrics.Compute(sch)
	fmt.Fprintf(&sb, "\nmetrics:\n%s", m.String())

	if erc := e.AutoValidateSCH(absOut); erc != "" {
		fmt.Fprintf(&sb, "\nERC:\n%s\n", erc)
	}

	pngPath := ""
	if e.KicadCLI != "" {
		if png, err := RenderSchematicPNG(absOut, e.KicadCLI, e.OutputDir); err == nil {
			pngPath = strings.TrimSuffix(absOut, ".kicad_sch") + ".png"
			if err := os.WriteFile(pngPath, png, 0o644); err != nil {
				pngPath = ""
			}
		} else {
			fmt.Fprintf(&sb, "render skipped: %v\n", err)
		}
	}

	return &CompileResult{SchematicPath: absOut, PNGPath: pngPath, Report: sb.String(), NetDefects: defects}, nil
}

// stripQuietLabels removes the documentation labels of nets whose source
// name starts with "_": connectivity-only junction names (the node between
// a resistor and its LED) that a hand-drawn schematic never prints. A label
// is only removed when the net stays one electrical piece without it, so a
// gate-demoted quiet net keeps its (required) labels.
func stripQuietLabels(sch *sexp.Schematic, d *compile.Design) []string {
	names := make([]string, 0, len(d.Nets))
	for name := range d.Nets {
		if strings.HasPrefix(name, "_") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	var stripped []string
	root := sch.Root()
	for _, name := range names {
		var kept, removed []*sexp.Node
		for _, c := range root.Children {
			if c.Head() == "label" && sexp.StringValue(c, 1) == name {
				removed = append(removed, c)
			} else {
				kept = append(kept, c)
			}
		}
		if len(removed) == 0 {
			continue
		}
		root.Children = kept
		if netIntact(sch, d.Nets[name]) {
			stripped = append(stripped, name)
			continue
		}
		root.Children = append(root.Children, removed...)
	}
	return stripped
}

// netIntact reports whether every listed "REF.pin" still shares one traced
// net — the test that a quiet net survives losing its labels.
func netIntact(sch *sexp.Schematic, pins []string) bool {
	if len(pins) < 2 {
		return true
	}
	for _, net := range sexp.TraceNets(sch) {
		found := 0
		for _, want := range pins {
			ref, pin, ok := strings.Cut(want, ".")
			if !ok {
				return false
			}
			for _, p := range net.Pins {
				if p.Reference == ref && (p.PinNumber == pin || p.PinName == pin) {
					found++
					break
				}
			}
		}
		if found == len(pins) {
			return true
		}
	}
	return false
}

// applyNoConnects places explicit no_connect markers plus, for references
// declared "unused", one marker on every pin left untouched by nets, wires,
// labels and power symbols. Returns the auto-marked pin list for the report.
func (e *Env) applyNoConnects(sch *sexp.Schematic, d *compile.Design, sb *strings.Builder) []string {
	for _, refPin := range d.NoConnect.Pins {
		if x, y, ok := sexp.FindPinPosition(sch, refPin); ok {
			sch.AddNoConnect(sexp.NewNoConnect(x, y))
		} else {
			fmt.Fprintf(sb, "no_connect: pin %s not found\n", refPin)
		}
	}
	var auto []string
	if len(d.NoConnect.Unused) == 0 {
		return auto
	}
	unused := map[string]bool{}
	for _, ref := range d.NoConnect.Unused {
		unused[ref] = true
	}
	connSet := sexp.ConnectedPins(sch)
	ncSet := sexp.NoConnectPointSet(sch)
	for _, sym := range sexp.ReadSymbols(sch) {
		if !unused[sym.Reference] {
			continue
		}
		for _, pin := range sym.Pins {
			key := [2]float64{sexp.Round2(pin.X), sexp.Round2(pin.Y)}
			if connSet[key] || ncSet[key] {
				continue
			}
			sch.AddNoConnect(sexp.NewNoConnect(pin.X, pin.Y))
			ncSet[key] = true
			auto = append(auto, sym.Reference+"."+pin.Number)
		}
	}
	return auto
}

// CompileSchematicArgs is the MCP input for compile_schematic.
type CompileSchematicArgs struct {
	DesignPath string `json:"design_path" jsonschema:"Path to the .design.json declarative source (see docs/compiler/FORMAT.md)"`
	OutputPath string `json:"output_path,omitempty" jsonschema:"Optional output .kicad_sch path; default <design dir>/<project>.kicad_sch"`
}

func (e *Env) handleCompileSchematic(_ context.Context, _ *mcp.CallToolRequest, input CompileSchematicArgs) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)
	if input.DesignPath == "" {
		return toolText("error: design_path is required"), nil, nil
	}
	out, err := e.CompileDesign(input.DesignPath, input.OutputPath)
	if err != nil {
		return toolText(fmt.Sprintf("compile error: %v", err)), nil, nil
	}
	report := out.Report + "\nschematic: " + out.SchematicPath
	return e.withInlinePNG(toolText(report), out.SchematicPath), nil, nil
}

// RegisterCompileTools registers the design-compiler tool set.
func RegisterCompileTools(s *mcp.Server, env *Env) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "compile_schematic",
		Description: "Compile a declarative .design.json source (blocks, pin-anchored placement, nets) into a complete .kicad_sch with wiring, power symbols, no_connects, ERC and a rendered preview. The source file is the editing surface: to change the schematic, edit the source and recompile. Read design_guide BEFORE authoring a source.",
	}, WrapTool(env.Log, "compile_schematic", env.handleCompileSchematic))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "design_guide",
		Description: "Read-only: the human-schematic design guide for .design.json authors — layout conventions (signal flow, power rails, decoupling farms), wire-vs-label criteria, spacing recipes in grid cells, and the iteration protocol. Read it before writing or editing a design source.",
	}, WrapTool(env.Log, "design_guide", env.handleDesignGuide))
}
