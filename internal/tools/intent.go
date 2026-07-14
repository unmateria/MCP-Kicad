package tools

import (
	"path/filepath"
	"sync"
)

// netlistIntent stores the LAST connect_netlist invocation per schematic path
// (absolute, lowercased). Relayout uses this to assign DAG sources by the
// USER'S original pin ordering instead of the schematic-add-order shuffle that
// TraceNets returns.
//
// Without intent, PlaceFlow picks the first pin in TraceNets' iteration order
// as the upstream source. That order is dictated by the order symbols were
// added to the schematic, which has nothing to do with signal flow — so an
// inverting amp becomes "U1 → R1, R2" when the user clearly meant
// "R1 → U1, R2".
//
// We store it in memory only; if the MCP server restarts the LLM has to call
// connect_netlist again before relayout to restore intent. That's an
// acceptable trade vs. mutating the .kicad_sch with a custom property.
var netlistIntent struct {
	sync.RWMutex
	byPath map[string][]NetConn
}

func init() {
	netlistIntent.byPath = make(map[string][]NetConn)
}

// rememberNetlistIntent records the connection list passed to connect_netlist
// so relayout can later replay the same pin ordering. Idempotent — overwrites
// any previous intent for that schematic.
func rememberNetlistIntent(schPath string, conns []NetConn) {
	abs, err := filepath.Abs(schPath)
	if err != nil {
		abs = schPath
	}
	key := canonicalPath(abs)
	// Deep-copy so future mutations of the caller's slice don't corrupt our store.
	cp := make([]NetConn, len(conns))
	for i, c := range conns {
		pins := make([]string, len(c.Pins))
		copy(pins, c.Pins)
		cp[i] = NetConn{Net: c.Net, Pins: pins}
	}
	netlistIntent.Lock()
	netlistIntent.byPath[key] = cp
	netlistIntent.Unlock()
}

// recallNetlistIntent returns the stored connection list for a schematic, or
// nil if none was stored.
func recallNetlistIntent(schPath string) []NetConn {
	abs, err := filepath.Abs(schPath)
	if err != nil {
		abs = schPath
	}
	key := canonicalPath(abs)
	netlistIntent.RLock()
	defer netlistIntent.RUnlock()
	return netlistIntent.byPath[key]
}

// canonicalPath lowercases the path on Windows to make the map key
// case-insensitive (the same .kicad_sch may be referenced with different
// casing across calls).
func canonicalPath(p string) string {
	// Cheap lowercase — schematic paths are ASCII in practice.
	b := []byte(p)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
