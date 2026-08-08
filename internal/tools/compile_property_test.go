package tools

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"mcp-kicad/internal/place2/gate"
	"mcp-kicad/internal/sexp"
)

// The compiler's promises are not properties of the seven reference circuits
// — they are supposed to hold for ANY source. Hand-written circuits only
// sample the space one point at a time, and each one costs an author's
// afternoon, so this generates sources instead and asserts the invariants on
// every one of them:
//
//	a source either compiles or is REJECTED with an error — never a panic;
//	a schematic that compiles has zero geometric violations (gate);
//	a schematic that compiles implements the netlist it declared.
//
// Every case is derived from a fixed seed, so a failure reproduces exactly:
// go test ./internal/tools/ -run TestCompilePropertiesOnGeneratedDesigns

type fuzzPart struct {
	prefix string
	lib    string
	pins   [2]string
}

var fuzzParts = []fuzzPart{
	{"R", "Device:R", [2]string{"1", "2"}},
	{"C", "Device:C", [2]string{"1", "2"}},
	{"L", "Device:L", [2]string{"1", "2"}},
	{"D", "Device:D", [2]string{"A", "K"}},
	{"D", "Device:LED", [2]string{"A", "K"}},
}

var fuzzDirs = []string{"left", "right", "up", "down"}
var fuzzRots = []int{0, 90, 180, 270}

// generateDesign builds one random but VALID-by-construction source: the
// placement is a tree (every symbol anchors to a pin of an earlier one), which
// is what the format requires, and no pin is claimed by two nets.
func generateDesign(seed int64) map[string]any {
	rnd := rand.New(rand.NewSource(seed))

	n := 3 + rnd.Intn(6) // 3..8 symbols
	type placed struct {
		ref  string
		part fuzzPart
	}
	var syms []map[string]any
	var placedRefs []placed

	for i := 0; i < n; i++ {
		part := fuzzParts[rnd.Intn(len(fuzzParts))]
		ref := fmt.Sprintf("%s%d", part.prefix, i+1)

		sym := map[string]any{"ref": ref, "lib": part.lib, "value": "x"}
		if rot := fuzzRots[rnd.Intn(len(fuzzRots))]; rot != 0 {
			sym["rot"] = rot
		}
		if rnd.Intn(4) == 0 {
			sym["mirror"] = true
		}
		if i > 0 {
			anchor := placedRefs[rnd.Intn(len(placedRefs))]
			sym["place"] = map[string]any{
				"pin":   part.pins[rnd.Intn(2)],
				"at":    anchor.ref + "." + anchor.part.pins[rnd.Intn(2)],
				"dir":   fuzzDirs[rnd.Intn(len(fuzzDirs))],
				"cells": 3 + rnd.Intn(4), // 3..6
			}
		}
		syms = append(syms, sym)
		placedRefs = append(placedRefs, placed{ref: ref, part: part})
	}

	// Pair pins into nets, never twice and never both ends of one part (which
	// would be a short across the component rather than a circuit).
	type pinRef struct {
		ref, pin string
	}
	var free []pinRef
	for _, p := range placedRefs {
		free = append(free, pinRef{p.ref, p.part.pins[0]}, pinRef{p.ref, p.part.pins[1]})
	}
	rnd.Shuffle(len(free), func(i, j int) { free[i], free[j] = free[j], free[i] })

	nets := map[string][]string{}
	used := map[pinRef]bool{}
	netIdx := 0
	for i := 0; i < len(free); i++ {
		a := free[i]
		if used[a] {
			continue
		}
		for j := i + 1; j < len(free); j++ {
			b := free[j]
			if used[b] || b.ref == a.ref {
				continue
			}
			name := fmt.Sprintf("N%d", netIdx)
			if netIdx == 0 {
				name = "GND"
			}
			nets[name] = []string{a.ref + "." + a.pin, b.ref + "." + b.pin}
			used[a], used[b] = true, true
			netIdx++
			break
		}
	}

	design := map[string]any{
		"version": 1,
		"project": fmt.Sprintf("fuzz_%d", seed),
		"sheet":   "auto",
		"blocks": []map[string]any{
			{"name": "gen", "symbols": syms},
		},
		"nets": nets,
	}
	if _, ok := nets["GND"]; ok {
		design["power_nets"] = map[string]string{"GND": "power:GND"}
	}
	return design
}

// fuzzEnv builds an Env that compiles without kicad-cli: no ERC run and no
// PNG render, which is what makes running hundreds of these affordable. The
// symbol library is the real one, so the geometry under test is the real
// geometry.
func fuzzEnv(t *testing.T) *Env {
	t.Helper()
	candidates, _ := filepath.Glob(filepath.Join("C:\\", "Program Files", "KiCad", "*", "share", "kicad", "symbols"))
	for _, dir := range candidates {
		if _, err := os.Stat(filepath.Join(dir, "Device.kicad_sym")); err == nil {
			return &Env{KicadSymbols: dir, LibsRoot: filepath.Join("..", "..", "libs")}
		}
	}
	t.Skip("no KiCad symbol library found; skipping generated-design properties")
	return nil
}

func TestCompilePropertiesOnGeneratedDesigns(t *testing.T) {
	env := fuzzEnv(t)

	// t.TempDir is wiped even when the test fails, which is exactly when the
	// generated source and schematic are worth looking at. Point FUZZ_OUT at
	// a directory to keep them.
	dir := os.Getenv("FUZZ_OUT")
	if dir == "" {
		dir = t.TempDir()
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("FUZZ_OUT: %v", err)
	}

	const cases = 60

	// Zero, and held there. This started as a ratchet at 6 — designs the
	// compiler refused because its own placement shorted two declared nets —
	// and the cause turned out to be one rule missing in three places: a wire
	// must never touch a pin of another net. Once the A*, its forced entry/exit
	// stubs and the welder all honoured it, every case went away.
	const knownShorts = 0

	compiled, rejected, shorted := 0, 0, 0

	for seed := int64(1); seed <= cases; seed++ {
		design := generateDesign(seed)
		data, err := json.MarshalIndent(design, "", "  ")
		if err != nil {
			t.Fatalf("seed %d: marshal: %v", seed, err)
		}
		srcPath := filepath.Join(dir, fmt.Sprintf("fuzz_%d.design.json", seed))
		if err := os.WriteFile(srcPath, data, 0o644); err != nil {
			t.Fatalf("seed %d: write: %v", seed, err)
		}
		outPath := filepath.Join(dir, fmt.Sprintf("fuzz_%d.kicad_sch", seed))

		res, err := env.CompileDesign(srcPath, outPath)
		if err != nil && res == nil {
			// Refusing a source is a legitimate outcome — anchors that overlap,
			// pins that touch across nets. What matters is that it is reported
			// rather than crashed on, or emitted as if it were fine.
			rejected++
			continue
		}
		if len(res.NetDefects) > 0 {
			// The compiler noticed and refused, so nothing wrong escapes; it
			// just could not lay this one out correctly. Tracked as a number
			// (see knownShorts) instead of hidden.
			shorted++
			t.Logf("seed %d: %v", seed, res.NetDefects)
			continue
		}
		compiled++

		out, err := os.ReadFile(outPath)
		if err != nil {
			t.Fatalf("seed %d: read output: %v", seed, err)
		}
		sch, err := sexp.ParseSchematic(string(out))
		if err != nil {
			t.Fatalf("seed %d: the compiler emitted an unparseable schematic: %v", seed, err)
		}
		if v := gate.Check(sch); len(v) > 0 {
			t.Errorf("seed %d: gate violations survived Enforce: %+v\nsource: %s", seed, v, data)
		}
	}

	t.Logf("%d clean, %d refused as invalid, %d shorted by our own placement, out of %d generated designs",
		compiled, rejected, shorted, cases)
	if compiled == 0 {
		t.Fatal("no generated design compiled cleanly — the generator is not exercising the compiler")
	}
	if shorted != knownShorts {
		t.Errorf("placement shorted %d generated designs, want %d: no wire may ever land on a pin of another net",
			shorted, knownShorts)
	}
}
