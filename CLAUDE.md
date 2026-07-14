# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Context

MCP (Model Context Protocol) server written in Go for Windows. It enables an LLM to design hardware in KiCad 9 by manipulating `.kicad_sch` and `.kicad_pcb` files directly and coordinating with external services.

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

# Canonical demos (drive the full MCP pipeline end-to-end)
go run ./cmd/demo_mcu_i2c
go run ./cmd/demo_voltage_regulator
go run ./cmd/demo_buck_converter
go run ./cmd/demo_full_board
go run ./cmd/demo_apply_template_opamp   # P6 stamping demo

# End-to-end smoke test (LED + NE5532 + NE555 power-dedup canary)
go run ./cmd/verify_e2e

# Goldens (P7) — bless current state explicitly; CI never blesses.
go run ./cmd/update_goldens
```

### Optional: ELK-based placement
The `internal/place2/elk` package shells out to a Node.js subprocess running
elkjs (Eclipse Layout Kernel). To enable it:

```bash
npm install -g elkjs
```

When Node or elkjs is unavailable the pipeline falls back to the legacy
`PlaceFlow` Sugiyama implementation; placement still works but ELK gives
visibly better port-aware layouts on multi-pin ICs.

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
| `internal/layout` | Legacy Sugiyama layered layout — still used by `relayout`; superseded by `internal/place2` |
| `internal/router` | Legacy A* orthogonal grid router (`astar.go`) — superseded by `internal/route2` |
| `internal/place2` | Next-gen placement pipeline (P1..P8): cluster detection → rules (Vcc top, GND bottom, signal flow L→R, rotation) → ELK layout → snap → route → decorate |
| `internal/place2/cluster` | Functional cluster detection (decoupling caps adjacent to IC, I²C pull-ups, LC filters, crystals + load caps, voltage dividers, op-amp feedback, headers) |
| `internal/place2/rules` | Human-convention rules: power rails above/below pin, bus alignment, signal flow, R/C/L rotation |
| `internal/place2/elk` | elkjs subprocess bridge for Sugiyama-with-ports layout (requires `npm install -g elkjs`); falls back to legacy PlaceFlow when Node.js or elkjs are missing |
| `internal/place2/templates` | Substructure library + `Stamp` API used by `apply_template`. Templates: op-amp non-inverting, voltage divider, MCU minimal, LM7805 regulator, I²C pull-ups. |
| `internal/place2/power` | Unified `#PWR` placer — pin-direction offset 2.54 mm, dedup by (libID, snapped position), bus alignment of same-rail symbols. Three previous power-placement sites in `tools/schematic.go` and `tools/netlist.go` converge here via `Env.NewPowerEmitter`. |
| `internal/place2/cluster/canonical` | Extra detectors registered via `init()`: `bypass_nonpower`, `series_led`, `oscillator_rc`, `feedback_divider`. Add new ones in `canonical/<kind>.go` and remember to extend `clusterapply.go::bboxAwareOffsetsCtx`. |
| `internal/testutil` | Golden-file utilities (UUID/date normalizer + metric tolerance compare). Used by `cmd/verify_e2e` and `cmd/update_goldens`. |
| `internal/route2/steiner.go` | Steiner trunk + collinear-group detector. Triggered in `tools/netlist.go::routeNets` for nets with ≥4 colinear pins (≥75% of net). |
| `internal/place2/metrics` | Objective layout-quality scoring (bends, crossings, wires-through-symbol, total wire length); used by `layout_metrics` tool and `cmd/measure_layout` |
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
| `relayout` | — | Apply Sugiyama layout to non-power symbols; removes existing wires |
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
1. create_schematic
2. batch: add all symbols (auto_place: true)
3. relayout
4. connect_netlist  ← full connection table, strategy: auto
5. export_schematic_image
6. validate_design ERC
```

### Not Yet Implemented

- `auto_route` — DSN → Freerouting → SES orchestration
- `export_fabrication` — Gerbers, drill files, and BOM
- PCBWay download support (token parsed from config but no client exists)
- KiCad official library auto-clone on first use

## Non-Negotiable Rules

- **No regex on KiCad files.** Use the S-expression AST parser (`internal/sexp`) for all reads and writes of `.kicad_sch` and `.kicad_pcb`. Parenthesis integrity is sacred.
- **Fail fast on config.** Validate `config.ini` at startup; call `os.Exit(1)` on any missing required path or key.
- **Idiomatic Go.** Standard Go style. Comments only where logic is non-obvious.
