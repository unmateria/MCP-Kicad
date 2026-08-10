package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-kicad/internal/sexp"
)

// Fixtures: embedded lib_symbols with a horizontal Device:R (pins 1/2 at
// local ±2.54) and a Device:LED (K/A at local ∓2.54) so the series_led
// cluster detector fires on the connection table.
const wgLibSymbols = `
	(lib_symbols
		(symbol "Device:R"
			(symbol "R_1_1"
				(pin passive line (at -2.54 0 0) (length 2.54) (number "1" (effects (font (size 1.27 1.27)))) (name "~" (effects (font (size 1.27 1.27)))))
				(pin passive line (at 2.54 0 180) (length 2.54) (number "2" (effects (font (size 1.27 1.27)))) (name "~" (effects (font (size 1.27 1.27)))))
			)
		)
		(symbol "Device:LED"
			(symbol "LED_1_1"
				(pin passive line (at -2.54 0 0) (length 2.54) (number "1" (effects (font (size 1.27 1.27)))) (name "K" (effects (font (size 1.27 1.27)))))
				(pin passive line (at 2.54 0 180) (length 2.54) (number "2" (effects (font (size 1.27 1.27)))) (name "A" (effects (font (size 1.27 1.27)))))
			)
		)
	)`

func wgF(v float64) string {
	s := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.4f", v), "0"), ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

func wgPlaced(libID, ref string, cx, cy float64) string {
	return `
	(symbol (lib_id "` + libID + `") (at ` + wgF(cx) + ` ` + wgF(cy) + ` 0) (unit 1) (in_bom yes) (on_board yes) (uuid "` + ref + `-uuid")
		(property "Reference" "` + ref + `" (at ` + wgF(cx) + ` ` + wgF(cy-6) + ` 0) (effects (font (size 1.27 1.27))))
		(property "Value" "x" (at ` + wgF(cx) + ` ` + wgF(cy+6) + ` 0) (effects (font (size 1.27 1.27))))
	)`
}

func wgWriteSchematic(t *testing.T, body string) string {
	t.Helper()
	content := `(kicad_sch (version 20231120) (generator "test") (uuid "00000000-0000-4000-8000-000000000000")` +
		wgLibSymbols + body + `
)`
	p := filepath.Join(t.TempDir(), "wg.kicad_sch")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func wgConnect(t *testing.T, schPath string, conns []NetConn) string {
	t.Helper()
	env := &Env{}
	res, _, err := env.handleConnectNetlist(context.Background(), nil, connectNetlistInput{
		SchematicPath: schPath, Connections: conns, Strategy: "auto",
	})
	if err != nil {
		t.Fatalf("handleConnectNetlist: %v", err)
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

func wgTraceNamedNet(t *testing.T, schPath, netName string) sexp.Net {
	t.Helper()
	data, err := os.ReadFile(schPath)
	if err != nil {
		t.Fatal(err)
	}
	sch, err := sexp.ParseSchematic(string(data))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range sexp.TraceNets(sch) {
		if n.Name == netName {
			return n
		}
	}
	return sexp.Net{}
}

// TestWiregenPartialNetRouterCompletes covers the partial-consumption shape of
// the demo_voltage_regulator regression: a 3-pin net where the series_led
// generator wires only the D1↔R1 pair (R2 sits beyond maxSpan so wiregen
// declines it). The router MUST still connect R2's pin to the rest of the net
// — only pins consumed by wiregen are skipped, never the remainder.
func TestWiregenPartialNetRouterCompletes(t *testing.T) {
	// D1 at (25.4,50.8): A pin at (27.94,50.8). R1 at (33.02,50.8): pin 1 at
	// (30.48,50.8) — 2.54 mm from D1.A, straight run, wiregen fires.
	// R2 at (76.2,50.8): pin 1 at (73.66,50.8) — 45.7 mm from D1.A (> maxSpan),
	// wiregen declines; the router must wire it.
	body := wgPlaced("Device:LED", "D1", 25.4, 50.8) +
		wgPlaced("Device:R", "R1", 33.02, 50.8) +
		wgPlaced("Device:R", "R2", 76.2, 50.8)
	schPath := wgWriteSchematic(t, body)

	out := wgConnect(t, schPath, []NetConn{
		{Net: "LED_NODE", Pins: []string{"D1.A", "R1.1", "R2.1"}},
	})
	if !strings.Contains(out, "wiregen:") || !strings.Contains(out, "series_led") {
		t.Fatalf("expected wiregen series_led report in output, got:\n%s", out)
	}

	net := wgTraceNamedNet(t, schPath, "LED_NODE")
	refs := map[string]bool{}
	for _, p := range net.Pins {
		refs[p.String()] = true
	}
	if !refs["D1.A"] || !refs["R1.1"] || !refs["R2.1"] || net.Dangling {
		t.Fatalf("LED_NODE must contain D1.A, R1.1 AND R2.1 fully connected; got %+v (output:\n%s)", net, out)
	}
}

// TestWiregenFullNetKeepsDiscoveryLabel is the exact demo_voltage_regulator
// bug: a 2-pin net FULLY consumed by wiregen produces zero router segments, so
// the "one label per net" fallback used to be skipped — leaving the net
// unnamed. Downstream passes rebuild their connection list from NAMED nets
// only, so the net was silently dropped and its pins left dangling. The label
// must be present even when the router had nothing left to do.
func TestWiregenFullNetKeepsDiscoveryLabel(t *testing.T) {
	body := wgPlaced("Device:LED", "D1", 25.4, 50.8) +
		wgPlaced("Device:R", "R1", 33.02, 50.8)
	schPath := wgWriteSchematic(t, body)

	out := wgConnect(t, schPath, []NetConn{
		{Net: "LED_NODE", Pins: []string{"D1.A", "R1.1"}},
	})
	if !strings.Contains(out, "wiregen:") || !strings.Contains(out, "series_led") {
		t.Fatalf("expected wiregen series_led report, got:\n%s", out)
	}

	// The traced net must carry the LED_NODE name — which requires a label,
	// since wiregen wires alone cannot name a net.
	net := wgTraceNamedNet(t, schPath, "LED_NODE")
	if len(net.Pins) != 2 || net.Dangling {
		t.Fatalf("LED_NODE must exist as a named, fully-connected 2-pin net; got %+v (output:\n%s)", net, out)
	}

	// And the label node itself must be in the file.
	data, _ := os.ReadFile(schPath)
	sch, _ := sexp.ParseSchematic(string(data))
	found := false
	for _, c := range sch.Root().Children {
		if c.Head() == "label" && sexp.StringValue(c, 1) == "LED_NODE" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no LED_NODE label found — the net would be dropped (output:\n%s)", out)
	}
}

// uglyPath must scale with the run: a long cross-sheet wire may carry more
// corners, and a short same-side arc may exceed the detour ratio, without
// either being a snake. The flat limits turned one of two twin digit-select
// lines into a label pair while its twin stayed wired.
func TestUglyPathScalesWithLength(t *testing.T) {
	// 140 mm run with 4 bends: how a person draws a digit-select line.
	long := [][2]float64{{0, 0}, {10, 0}, {10, 10}, {80, 10}, {80, 5}, {140, 5}}
	if uglyPath(long) {
		t.Error("a 140 mm run with 4 corners is not a snake")
	}
	// C-shaped arc joining two pins 5 mm apart on the same symbol side:
	// 3x the Manhattan distance but only ~15 mm of wire.
	arc := [][2]float64{{0, 0}, {7.62, 0}, {7.62, 5.08}, {0, 5.08}}
	if uglyPath(arc) {
		t.Error("a short same-side C arc is not a snake")
	}
	// A genuine snake on a short hop must still be rejected.
	snake := [][2]float64{{0, 0}, {15, 0}, {15, 10}, {2, 10}, {2, 20}, {18, 20}, {18, 2.54}, {5.08, 2.54}}
	if !uglyPath(snake) {
		t.Error("a wandering 80 mm path for a 6 mm hop must stay ugly")
	}
}
