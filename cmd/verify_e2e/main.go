// verify_e2e drives the new MCP handlers directly (without going through Claude
// Desktop or the MCP transport) to build the led_18650 reference circuit and
// run ERC. Used to confirm Phase 1+2+3 fixes end-to-end.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-kicad/internal/config"
	"mcp-kicad/internal/tools"
)

func textOf(r *mcp.CallToolResult) string {
	if r == nil {
		return ""
	}
	var sb strings.Builder
	for _, c := range r.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(t.Text)
		}
	}
	return sb.String()
}

func main() {
	cfg := config.Load("config.ini")
	env := &tools.Env{
		LibsRoot:        cfg.LibsRoot,
		KicadCLI:        cfg.KicadCLI,
		KicadSymbols:    cfg.KicadSymbols,
		KicadFootprints: cfg.KicadFootprints,
		OutputDir:       cfg.OutputDir,
		ConfigPath:      cfg.ConfigPath,
	}

	schDir, _ := filepath.Abs("projects/led_18650_v2")
	_ = os.RemoveAll(schDir)
	_ = os.MkdirAll(schDir, 0o755)
	schPath := filepath.Join(schDir, "led_18650.kicad_sch")

	ctx := context.Background()

	report := func(name, out string, err error) {
		fmt.Printf("\n=== %s ===\n", name)
		if err != nil {
			fmt.Println("ERROR:", err)
			os.Exit(1)
		}
		if len(out) > 1500 {
			out = out[:1500] + "\n... (truncated)"
		}
		fmt.Println(out)
	}

	// 1. create_schematic
	r, _, err := env.HandleCreateSchematicForTest(ctx, nil, tools.CreateSchematicArgs{SchematicPath: schPath})
	report("create_schematic", textOf(r), err)

	// 2. add_symbol × 3 with auto_place
	add := func(libID, ref, val string) {
		r, _, err := env.HandleAddSymbolForTest(ctx, nil, tools.AddSymbolArgs{
			SchematicPath: schPath, LibID: libID, Reference: ref, Value: val,
			MountType: "THT", AutoPlace: true,
		})
		report("add_symbol "+ref, textOf(r), err)
	}
	add("Device:Battery_Cell", "BT1", "18650")
	add("Device:R", "R1", "100")
	add("Device:LED", "D1", "LED_RED")

	// 3. connect_netlist
	r, _, err = env.HandleConnectNetlistForTest(ctx, nil, tools.ConnectNetlistArgs{
		SchematicPath: schPath,
		Connections: []tools.NetConn{
			{Net: "VBAT", Pins: []string{"BT1.+", "R1.1"}},
			{Net: "ANODE", Pins: []string{"R1.2", "D1.A"}},
			{Net: "GND", Pins: []string{"D1.K", "BT1.-"}},
		},
		Strategy: "auto",
	})
	report("connect_netlist", textOf(r), err)

	// 4. get_connectivity_summary
	r, _, err = env.HandleConnectivityForTest(ctx, nil, tools.ConnectivityArgs{SchematicPath: schPath})
	report("get_connectivity_summary", textOf(r), err)

	// 5. validate_design ERC
	r, _, err = env.HandleValidateForTest(ctx, nil, tools.ValidateArgs{SchematicPath: schPath, RunERC: true})
	report("validate_design ERC", textOf(r), err)

	// 6. Sanity grep for the old serialization bug.
	data, err := os.ReadFile(schPath)
	if err != nil {
		fmt.Println("read error:", err)
		os.Exit(1)
	}
	if hasDoubleQuoteBug(string(data)) {
		fmt.Println("\n❌ FAIL: schematic contains the pin-double-quote bug")
		os.Exit(1)
	}
	fmt.Println("\n✅ LED schematic clean of double-quote bug")
	fmt.Println("led schematic written to:", schPath)

	// === Test #2: NE5532 stereo dual unity-gain buffer ===
	// Exercises multi-unit IC handling (units A/B/C), MST routing on the GND net
	// (4 pins: 2 op-amp inputs + 2 decoupling caps), add_power_rail auto-rotation
	// for V+ / V-, and short detection (must not trigger).
	fmt.Println("\n\n#### NE5532 stereo buffer ####")
	ne5532Dir, _ := filepath.Abs("projects/ne5532_buf")
	_ = os.RemoveAll(ne5532Dir)
	_ = os.MkdirAll(ne5532Dir, 0o755)
	ne5532Path := filepath.Join(ne5532Dir, "ne5532.kicad_sch")

	r, _, err = env.HandleCreateSchematicForTest(ctx, nil, tools.CreateSchematicArgs{SchematicPath: ne5532Path})
	report("create_schematic", textOf(r), err)

	// 3 units of NE5532 (A=in 1+2+3, B=in 5+6+7, C=power 4+8) + 2 decoupling caps.
	addOpamp := func(unit int) {
		r, _, err := env.HandleAddSymbolForTest(ctx, nil, tools.AddSymbolArgs{
			SchematicPath: ne5532Path, LibID: "Amplifier_Operational:NE5532",
			Reference: "U1", Value: "NE5532", MountType: "THT", AutoPlace: true, Unit: unit,
		})
		report(fmt.Sprintf("add_symbol U1 unit %d", unit), textOf(r), err)
	}
	addOpamp(1)
	addOpamp(2)
	addOpamp(3)

	addCap := func(ref string) {
		r, _, err := env.HandleAddSymbolForTest(ctx, nil, tools.AddSymbolArgs{
			SchematicPath: ne5532Path, LibID: "Device:C",
			Reference: ref, Value: "100n", MountType: "THT", AutoPlace: true,
		})
		report("add_symbol "+ref, textOf(r), err)
	}
	addCap("C1") // decoupling for V+
	addCap("C2") // decoupling for V-

	// Net topology — both halves wired as unity-gain followers with grounded inputs.
	// GND has 4 pins → exercises MST greedy routing.
	r, _, err = env.HandleConnectNetlistForTest(ctx, nil, tools.ConnectNetlistArgs{
		SchematicPath: ne5532Path,
		Connections: []tools.NetConn{
			{Net: "OUT_A", Pins: []string{"U1.1.1", "U1.1.2"}},  // feedback A
			{Net: "OUT_B", Pins: []string{"U1.2.7", "U1.2.6"}},  // feedback B
			{Net: "GND", Pins: []string{"U1.1.3", "U1.2.5", "C1.2", "C2.1"}},
			{Net: "VPP", Pins: []string{"U1.3.8", "C1.1"}},  // V+ rail
			{Net: "VMM", Pins: []string{"U1.3.4", "C2.2"}},  // V- rail
		},
		Strategy: "auto",
	})
	report("connect_netlist (NE5532)", textOf(r), err)

	// Add power rails — should auto-rotate.
	addPower := func(libID, from string) {
		// add_power_rail goes through the same handler as add_symbol via execOne.
		r, _, err := env.HandleAddPowerRailForTest(ctx, nil, tools.AddPowerRailArgs{
			SchematicPath: ne5532Path, LibID: libID, From: from,
		})
		report("add_power_rail "+libID+" → "+from, textOf(r), err)
	}
	addPower("power:GND", "U1.1.3")
	addPower("power:+12V", "U1.3.8")
	addPower("power:-12V", "U1.3.4")

	// Apply Sugiyama re-layout — must reorganize multi-unit U1 + caps without
	// dropping electrical connectivity.
	r, _, err = env.HandleRelayoutForTest(ctx, nil, tools.RelayoutArgs{SchematicPath: ne5532Path})
	report("relayout (NE5532)", textOf(r), err)

	r, _, err = env.HandleConnectivityForTest(ctx, nil, tools.ConnectivityArgs{SchematicPath: ne5532Path})
	report("get_connectivity_summary (NE5532)", textOf(r), err)

	r, _, err = env.HandleValidateForTest(ctx, nil, tools.ValidateArgs{SchematicPath: ne5532Path})
	report("validate_design ERC after relayout (NE5532)", textOf(r), err)

	data, _ = os.ReadFile(ne5532Path)
	if hasDoubleQuoteBug(string(data)) {
		fmt.Println("\n❌ FAIL: NE5532 schematic contains the pin-double-quote bug")
		os.Exit(1)
	}
	fmt.Println("\n✅ NE5532 schematic clean of double-quote bug")
	fmt.Println("NE5532 schematic written to:", ne5532Path)

	// === Test #3: NE555 astable canary — power dedup must be zero ===
	fmt.Println("\n\n#### NE555 astable (power-dedup canary) ####")
	ne555Dir, _ := filepath.Abs("projects/ne555_astable_v2")
	_ = os.RemoveAll(ne555Dir)
	_ = os.MkdirAll(ne555Dir, 0o755)
	ne555Path := filepath.Join(ne555Dir, "ne555.kicad_sch")

	r, _, err = env.HandleCreateSchematicForTest(ctx, nil, tools.CreateSchematicArgs{SchematicPath: ne555Path})
	report("create_schematic", textOf(r), err)
	addAt := func(libID, ref, val string) {
		r, _, err := env.HandleAddSymbolForTest(ctx, nil, tools.AddSymbolArgs{
			SchematicPath: ne555Path, LibID: libID, Reference: ref, Value: val,
			MountType: "THT", AutoPlace: true,
		})
		report("add_symbol "+ref, textOf(r), err)
	}
	addAt("Timer:NE555P", "U1", "NE555")
	addAt("Device:R", "R1", "10k")
	addAt("Device:R", "R2", "47k")
	addAt("Device:C", "C1", "10n")
	addAt("Device:C", "C2", "10n")
	addAt("Device:LED", "D1", "LED")
	addAt("Device:R", "R3", "330")

	r, _, err = env.HandleConnectNetlistForTest(ctx, nil, tools.ConnectNetlistArgs{
		SchematicPath: ne555Path,
		Connections: []tools.NetConn{
			{Net: "VCC", Pins: []string{"U1.VCC", "U1.RESET", "R1.1"}},
			{Net: "GND", Pins: []string{"U1.GND", "C1.2", "C2.2", "D1.K"}},
			{Net: "DISCH", Pins: []string{"U1.DISCH", "R1.2", "R2.1"}},
			{Net: "THR", Pins: []string{"U1.THR", "U1.TRIG", "R2.2", "C1.1"}},
			{Net: "CTL", Pins: []string{"U1.CONT", "C2.1"}},
			{Net: "OUT", Pins: []string{"U1.OUT", "R3.1"}},
			{Net: "ANODE", Pins: []string{"R3.2", "D1.A"}},
		},
		Strategy: "auto",
	})
	report("connect_netlist (NE555)", textOf(r), err)

	r, _, err = env.HandleRelayoutForTest(ctx, nil, tools.RelayoutArgs{SchematicPath: ne555Path})
	report("relayout (NE555)", textOf(r), err)

	r, _, err = env.HandleValidateForTest(ctx, nil, tools.ValidateArgs{SchematicPath: ne555Path, RunERC: true})
	report("validate_design ERC (NE555)", textOf(r), err)

	// Power-dedup assertion: count distinct (libID, snap-grid pos) buckets vs raw count.
	data, _ = os.ReadFile(ne555Path)
	if dups := countDuplicatePower(string(data)); dups > 0 {
		fmt.Printf("\n❌ FAIL: NE555 has %d duplicate #PWR symbols at same coord\n", dups)
		os.Exit(1)
	}
	fmt.Println("\n✅ NE555 has zero duplicate #PWR symbols (P1 dedup OK)")
	fmt.Println("NE555 schematic written to:", ne555Path)
}

// countDuplicatePower scans the .kicad_sch text and reports how many `power:*`
// symbol instances share the same (lib_id, snapped position) bucket as another.
func countDuplicatePower(content string) int {
	type key struct{ lib, pos string }
	seen := map[key]int{}
	lines := strings.Split(content, "\n")
	var curLib string
	var curAt string
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "(symbol ") {
			curLib, curAt = "", ""
		}
		if strings.HasPrefix(t, "(lib_id \"power:") {
			curLib = t
		}
		if strings.HasPrefix(t, "(at ") && curLib != "" && curAt == "" {
			curAt = t
			seen[key{curLib, curAt}]++
		}
	}
	dup := 0
	for _, c := range seen {
		if c > 1 {
			dup += c - 1
		}
	}
	return dup
}

// hasDoubleQuoteBug returns true if the file contains the legacy bug pattern
// where pin numbers, path UUIDs, labels or uuid values were re-quoted on
// serialization, producing (pin "\"N\"" ...) or (path "/\"<uuid>\"" ...).
// Description fields legitimately contain \" (e.g. `... name \"GND\"`), so a
// blanket strings.Contains check produces false positives.
func hasDoubleQuoteBug(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, `(pin "\"`) ||
			strings.HasPrefix(t, `(path "/\"`) ||
			strings.HasPrefix(t, `(label "\"`) ||
			strings.HasPrefix(t, `(uuid "\"`) {
			return true
		}
	}
	return false
}
