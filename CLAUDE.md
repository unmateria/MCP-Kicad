# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Context

MCP (Model Context Protocol) server written in Go for Windows. It enables an LLM to design hardware in KiCad 10 by manipulating `.kicad_sch` and `.kicad_pcb` files directly and coordinating with external services.

**Wire-quality strategy (2026-07):** wires only exist where their geometry is trivial and verifiable — baked template geometry (`place2/templates`), closed-form cluster formulas (`place2/wiregen`), or router output that survives the geometric gate (`place2/gate`). Everything else becomes net labels; power nets are never routed (one power symbol per pin). The gate guarantees zero cross-net crossings, zero wires through symbol bodies, zero collinear overlaps in every output.

## Documentation

 Kicad documentation is located at:
 https://dev-docs.kicad.org/en/file-formats/sexpr-intro/index.html y
 https://dev-docs.kicad.org/en/file-formats/sexpr-schematic/index.html

## Build & Run Commands

```bash
# Build
go build -o mcp-kicad.exe ./cmd/server

# Run
./mcp-kicad.exe

# Run all tests
go test ./...

# Run a single package's tests
go test ./internal/sexp/...
go test ./internal/router/...

# Run a specific test
go test ./internal/sexp/... -run TestParseSchematic

# Lint
golangci-lint run

# Utility: inspect pin positions in a .kicad_sym file
go run ./cmd/pininfo <library.kicad_sym> [SymbolName]

# Measure layout quality (bends, crossings, wires-through-symbol)
go run ./cmd/measure_layout <project.kicad_sch>

# Compile a declarative source into a schematic (the main iteration loop)
go run ./cmd/compile -o out.kicad_sch docs/compiler/<x>.design.json

# Template stamping demo
go run ./cmd/demo_apply_template_opamp

# End-to-end smoke test (compiles led_18650 + ne5532_buf + ne555_astable)
go run ./cmd/verify_e2e
```

### Canonical circuits

The seven `.design.json` sources in `docs/compiler/` are the reference corpus —
every pipeline change must be checked against all of them:

```
demo_full_board   demo_voltage_regulator   demo_mcu_i2c   demo_buck_converter
led_18650         ne5532_buf               ne555_astable
```

The format is documented in `docs/compiler/FORMAT.md`.

## Architecture

The server exposes MCP tools that an LLM calls to design PCBs. The tool implementations follow this pipeline:

**Validate → Import → Edit AST → Verify**

1. Check if component exists in `libs/` or global KiCad libraries before downloading.
2. If external, fetch from SnapEDA (credentials from `config.ini`).
3. Edit `.kicad_sch` / `.kicad_pcb` files via S-expression AST parser — never regex.
4. Verify via `kicad-cli` ERC/DRC reports.

### Key Packages

| Package | Responsibility |
|---|---|
| `cmd/server` | Entry point, config validation, MCP server bootstrap |
| `cmd/pininfo` | Utility to extract pin positions from `.kicad_sym` files |
| `internal/config` | Reads `config.ini` via `gopkg.in/ini.v1`; auto-detects KiCad install; `os.Exit(1)` on bad config |
| `internal/sexp` | S-expression parser/writer for KiCad files (the AST engine); includes pin resolution (`pins.go`) and netlist tracer (`nets.go`) |
| `internal/compile` | Declarative design compiler: parses `.design.json`, resolves pin-anchored placement into absolute coordinates, produces the layout consumed by `tools/compile.go` |
| `internal/router` | A* orthogonal grid router (`astar.go`) used by `connect_netlist` and the compiler's routing pass |
| `internal/place2/cluster` | Functional cluster detection (decoupling caps adjacent to IC, I²C pull-ups, LC filters, crystals + load caps, voltage dividers, op-amp feedback, headers) |
| `internal/place2/textplace` | Field autoplacer: repositions reference/value text and flips net labels so they clear symbol bodies and wires. KiCad draws a field at (field angle + symbol rotation): fields on 90/270 symbols carry a compensating 90. |
| `internal/place2/weld` | Label-pair upgrader: same-net islands joined only by labels get a real wire (straight/L/Z) when a clean corridor exists, validated against `gate.Check`; redundant labels removed, one kept as net documentation. Runs after the gate in `compile_schematic`. |
| `internal/place2/templates` | Substructure library + `Stamp` API used by `apply_template`. Templates: op-amp non-inverting, voltage divider, MCU minimal, LM7805 regulator, I²C pull-ups. |
| `internal/place2/power` | Unified `#PWR` placer — pin-direction offset 2.54 mm, dedup by (libID, snapped position), bus alignment of same-rail symbols. Three previous power-placement sites in `tools/schematic.go` and `tools/netlist.go` converge here via `Env.NewPowerEmitter`. |
| `internal/place2/cluster/canonical` | Extra detectors registered via `init()`: `bypass_nonpower`, `series_led`, `oscillator_rc`, `feedback_divider`. Add new ones in `canonical/<kind>.go`. |
| `internal/route2/steiner.go` | Steiner trunk + collinear-group detector. Triggered in `tools/netlist.go::routeNets` for nets with ≥4 colinear pins (≥75% of net). |
| `internal/place2/metrics` | Objective layout-quality scoring (bends, crossings, wires-through-symbol, total wire length); used by `layout_metrics` tool and `cmd/measure_layout` |
| `internal/place2/gate` | Geometric quality gate: `Enforce()` demotes any net whose wiring crosses another net, cuts a symbol body or overlaps collinearly → wires replaced by net labels (connectivity preserved). Runs at the end of `compile_schematic` and `connect_netlist`. |
| `internal/place2/wiregen` | Closed-form cluster wiring (decoupling, 2-pin satellites, dividers, crystal load caps): straight or single-L with clear-corridor preconditions; applies or declines, never searches. Pre-pass in `compile_schematic` and `connect_netlist`. `allowMoves` exists but is off in production. |
| `internal/sexp/normalize.go` + `internal/tools/sheetfit.go` | Final rigid translation of all content into the sheet's usable area (≥12.7 mm margins, avoids title block); auto-upgrades paper A4→A3 |
| `internal/route2` | Next-gen routing layer: A*++ fallback with angular heuristic + cross-prevention; libavoid cgo bindings stubbed for future |
| `internal/tools` | One file per MCP tool group, implements tool logic |
| `internal/kicadcli` | Wrapper around `kicad-cli.exe` subprocess calls; violation classifier |
| `internal/parts` | SnapEDA HTTP client + local/global library search + default footprint table |
| `libs/` | Downloaded component symbols and footprints |

### Configuration (`config.ini`)

All paths are optional — `kicad_cli` is auto-detected by scanning `C:\Program Files\KiCad\` for version dirs, then `PATH`. Server calls `os.Exit(1)` only if auto-detection also fails.

```ini
[paths]
kicad_cli = C:\Program Files\KiCad\10.0\bin\kicad-cli.exe
kicad_symbols = C:\Program Files\KiCad\10.0\share\kicad\symbols
kicad_footprints = C:\Program Files\KiCad\10.0\share\kicad\footprints
libs_root = libs                           # default: "libs"
output_dir = C:\claude\outputs             # default: C:\claude\outputs; writable by set_output_dir
freerouting = C:\path\to\freerouting.jar   # optional

[api_keys]
snapeda = YOUR_TOKEN   # optional; client not created if empty or "YOUR_TOKEN"
pcbway = YOUR_TOKEN    # optional; reserved for future use
```

### Tool Registration Pattern

All tools share an `Env` struct wired at startup:

```go
type Env struct {
    LibsRoot        string               // "libs"
    KicadCLI        string               // path to kicad-cli.exe
    KicadSymbols    string               // global symbol library dir
    KicadFootprints string               // global footprint library dir
    SnapEDA         *parts.SnapEDAClient // nil if not configured
    OutputDir       string               // directory where generated files are written
    ConfigPath      string               // absolute path to config.ini (for write-back)
}
```

Handler signature (input struct uses `json` + `jsonschema` tags for schema inference):

```go
func (e *Env) handleXxx(ctx context.Context, _ *mcp.CallToolRequest, input InputType) (*mcp.CallToolResult, any, error)
```

Return tool errors as text results via `toolText(msg)`, not as Go errors.

### MCP SDK (v1.4.0)

```go
// Server + transport
s := mcp.NewServer(&mcp.Implementation{Name: "...", Version: "..."}, nil)
s.Run(context.Background(), &mcp.StdioTransport{})   // NOT mcp.NewStdioTransport()

// Tool registration
mcp.AddTool(s, &mcp.Tool{Name: "...", Description: "..."}, env.handleXxx)

// Text result helper
&mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "..."}}}
```

### sexp Engine Key Types

```go
// Core node
type Node struct { Value string; Children []*Node; IsString bool }

// Parse / serialize
Parse(input string) ([]*Node, error)
Write(nodes []*Node) string
WriteNode(n *Node) string

// Schema-level parsers
ParseSchematic(input string) (*Schematic, error)
ParsePCB(input string) (*PCB, error)

// Node constructors
Atom(v string) *Node          // unquoted atom
Str(v string) *Node           // quoted string (auto-escapes)
List(children ...*Node) *Node // list node

// Schematic node constructors
NewSymbolInstance(libID, ref, val, footprint string, x, y, rot float64, unit int, pins []string, schUUID string, inBom, onBoard bool) *Node
NewWire(x1, y1, x2, y2 float64) *Node
NewNoConnect(x, y float64) *Node
NewJunction(x, y float64) *Node
NewNetLabel(name string, x, y, angle float64) *Node

// PCB helpers
NewEdgeCutsRect(x, y, w, h float64) []*Node  // returns 4 gr_line nodes

// Tree queries
FindList(parent *Node, name string) *Node
FindAllLists(parent *Node, name string) []*Node
AtomValue(parent *Node, i int) string
StringValue(parent *Node, i int) string
```

**Pin/net helpers (pins.go, nets.go):**

```go
// Pin position resolution — transforms local symbol coords → absolute schematic coords
ReadSymbols(sch *Schematic) []SchematicSymbol
FindPinPosition(sch *Schematic, refPin string) (x, y float64, ok bool) // e.g. "R1.1"

// Netlist tracer — union-find connecting wires + net labels
TraceNets(sch *Schematic) []Net  // Net{Name, Pins []PinRef, Dangling bool}

// Bounding box + segment collision helpers
SymbolBBox(sym SchematicSymbol) (x1, y1, x2, y2 float64)
SegmentCrossesBox(ax, ay, bx, by, rx1, ry1, rx2, ry2 float64) bool
```

**A* router (`internal/router`):**

```go
// Build obstacle grid from placed symbols + existing wires.
// Hard obstacles: symbol BODY interiors (inset from pin tips by 2.54 mm so pin-tip
//   cells are always outside the blocked area — the A* can start/end at any pin).
// Soft obstacles: existing wires (+20 cost).
NewRouter(syms []sexp.SchematicSymbol, existingWires []*sexp.Node) *Router

// Find obstacle-avoiding orthogonal path. Returns nil when blocked or route > MaxRouteLen mm.
// Output is a minimal slice of grid-snapped waypoints (collinear points merged).
(r *Router) Route(x1, y1, x2, y2 float64) [][2]float64

// Mark a routed path as soft obstacles for subsequent Route calls.
(r *Router) MarkWire(path [][2]float64)

// Grid constants
router.MaxRouteLen = 300.0  // mm — routes longer than this → nil (fall back to labels)

// KEY BUG (fixed): SymbolBBox pads OUTWARD by 2.54 mm, putting pin tips INSIDE the
// obstacle. symbolBodyBBox (internal) pads INWARD so pins are always outside.
// Do NOT use SymbolBBox for hard obstacles in the router.
```

File I/O pattern: `os.ReadFile` → `ParseSchematic`/`ParsePCB` → mutate AST → `Serialize()` → `os.WriteFile`.

## MCP Tools

### Implemented

| Tool | File | Actions / Behavior |
|---|---|---|
| `get_project_info` | `validate.go` | Returns config paths, output_dir, and SnapEDA status |
| `get_output_dir` | `validate.go` | Returns current output_dir + last 5 files in it |
| `set_output_dir` | `validate.go` | Sets output_dir, creates it, persists to config.ini immediately |
| `check_component_existence` | `components.go` | Local libs → global KiCad libs → SnapEDA fallback (up to 5 results) |
| `fetch_external_part` | `components.go` | Downloads symbol + footprint to `libs/downloaded/{dest}/` |
| `register_library` | `components.go` | Appends entry to `sym-lib-table` or `fp-lib-table` |
| `compile_schematic` | `compile.go` | Compile a `.design.json` source into a complete `.kicad_sch` (placement, wiring, power symbols, no_connects, gate, weld, textplace, ERC, PNG preview). The primary authoring path. |
| `design_guide` | `design_guide.go` | Read-only: human-schematic conventions and spacing recipes for `.design.json` authors (embedded `design_guide.md`). Read before authoring a source. |
| `modify_schematic` | `schematic.go` | See actions below |
| `read_schematic` | `schematic.go` | Lists placed symbols + pin positions; ASCII layout grid; connectivity status |
| `get_connectivity_summary` | `schematic.go` | Full netlist audit: unconnected pins, dangling nets, pin counts |
| `connect_netlist` | `netlist.go` | Route full netlist in one call; A* routing with label fallback |
| `modify_pcb_layout` | `pcb.go` | Actions: `move_footprint`, `define_edge_cuts` |
| `validate_design` | `validate.go` | Runs ERC and/or DRC via `kicad-cli`; returns classified violations |
| `export_schematic_image` | `export.go` | Renders schematic to PNG via `kicad-cli` SVG → headless browser; output goes to `OutputDir` |
| `cluster_components` | `cluster_components.go` | Read-only — list functional clusters detected (decoupling, pull-ups, LC, crystal, divider, header, op-amp feedback) |
| `list_templates` | `templates.go` | List sub-circuit templates (op-amp non-inverting, voltage divider, MCU minimal, LM7805, I²C pull-ups) |
| `layout_metrics` | `templates.go` | Read-only — metric set (bends, crossings, wires-through-symbol, total wire length, density) for one schematic |

**`modify_schematic` actions:**

| Action | Key Parameters | Description |
|---|---|---|
| `create_schematic` | `schematic_path` | Create minimal valid `.kicad_sch` |
| `add_symbol` | `lib_id, reference, value, x, y, mount_type, footprint, unit, auto_place, rotation` | Place component; auto-embeds symbol def from global lib |
| `connect_pins` | `from, to, via` | Draw wire(s) with smart L-routing; avoids symbol bodies; falls back to A* when both L-orientations blocked |
| `disconnect_pin` | `from` | Remove all wires/no_connect markers touching a pin; use to undo wrong connections |
| `add_wire` | `x, y, x2, y2` | Manual wire segment |
| `no_connect` | `from` or `x, y` | Mark pin intentionally unconnected (suppresses ERC) |
| `junction` | `x, y` | Solder dot at T/X-intersection |
| `add_label` | `name, x, y, rotation` | Net label (two same-name labels = connected) |
| `batch` | `operations` list | Execute multiple actions in one write; stops on first error |

**`connect_netlist` — preferred for routing full designs:**

```
strategy: auto  (default) — A* routing; label fallback when route fails or > 150 mm
strategy: wire  — A* only; warns on failure, does not label
strategy: label — always place net labels, never wires (fastest, zero crossing risk)
```

Daisy-chains pins within each net (pin[0]→pin[1]→…). Auto-inserts junctions at T-intersections. After each segment marks the path as a soft obstacle for the next.

**LLM workflow (recommended):**
```
1. Write / edit the .design.json source (see docs/compiler/FORMAT.md)
2. compile_schematic  ← one call: placement, wiring, power, gate, ERC, PNG
3. Look at the PNG and the ERC section of the report
4. Iterate on the source and recompile
```

### Not Yet Implemented

- `auto_route` — DSN → Freerouting → SES orchestration
- `export_fabrication` — Gerbers, drill files, and BOM
- PCBWay download support (token parsed from config but no client exists)
- KiCad official library auto-clone on first use

## Non-Negotiable Rules

- **No regex on KiCad files.** Use the S-expression AST parser (`internal/sexp`) for all reads and writes of `.kicad_sch` and `.kicad_pcb`. Parenthesis integrity is sacred.
- **Determinism in the placement/routing path.** Never `range` over a Go map in cluster detection, net tracing, placement or routing code — sort keys first (this was the root cause of months of heisenbugs; see `cluster.refOrder` and the sorted net naming in `sexp/nets.go`).
- **kicad-cli reports go to a file.** `kicad-cli sch erc/drc` writes its JSON report ONLY to the `-o` file, never stdout. Parsing stdout silently yields zero violations (this bug made ERC blind for months).
- **Fail fast on config.** Validate `config.ini` at startup; call `os.Exit(1)` on any missing required path or key.
- **The generated `.kicad_sch` is an artifact.** Edit the `.design.json` source and recompile; never hand-edit the destination file.
- **Idiomatic Go.** Standard Go style. Comments only where logic is non-obvious.
