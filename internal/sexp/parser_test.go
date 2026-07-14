package sexp_test

import (
	"strings"
	"testing"

	"mcp-kicad/internal/sexp"
)

// --- Parser unit tests ---

func TestParseAtom(t *testing.T) {
	nodes, err := sexp.Parse("hello")
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].Value != "hello" {
		t.Fatalf("expected atom 'hello', got %+v", nodes)
	}
}

func TestParseString(t *testing.T) {
	nodes, err := sexp.Parse(`"hello world"`)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || !nodes[0].IsString {
		t.Fatal("expected string node")
	}
}

func TestParseStringEscape(t *testing.T) {
	nodes, err := sexp.Parse(`"say \"hi\""`)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].Value != `"say \"hi\""` {
		t.Fatalf("unexpected value: %q", nodes[0].Value)
	}
}

func TestParseSimpleList(t *testing.T) {
	nodes, err := sexp.Parse(`(version 20231120)`)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || !nodes[0].IsList() {
		t.Fatal("expected list node")
	}
	if nodes[0].Head() != "version" {
		t.Fatalf("expected head 'version', got %q", nodes[0].Head())
	}
}

func TestParseNested(t *testing.T) {
	input := `(kicad_sch (version 20231120) (generator eeschema))`
	nodes, err := sexp.Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	if nodes[0].Head() != "kicad_sch" {
		t.Fatal("expected kicad_sch head")
	}
	version := sexp.FindList(nodes[0], "version")
	if version == nil {
		t.Fatal("version node not found")
	}
	if sexp.AtomValue(version, 1) != "20231120" {
		t.Fatalf("unexpected version: %q", sexp.AtomValue(version, 1))
	}
}

func TestUnterminatedList(t *testing.T) {
	_, err := sexp.Parse(`(kicad_sch (version 20231120)`)
	if err == nil {
		t.Fatal("expected error for unterminated list")
	}
}

func TestUnterminatedString(t *testing.T) {
	_, err := sexp.Parse(`"no closing`)
	if err == nil {
		t.Fatal("expected error for unterminated string")
	}
}

// --- Writer tests ---

func TestWriteAtom(t *testing.T) {
	n := sexp.Atom("hello")
	if sexp.WriteNode(n) != "hello" {
		t.Fatalf("unexpected: %q", sexp.WriteNode(n))
	}
}

func TestWriteString(t *testing.T) {
	n := sexp.Str("hello world")
	if sexp.WriteNode(n) != `"hello world"` {
		t.Fatalf("unexpected: %q", sexp.WriteNode(n))
	}
}

func TestWriteList(t *testing.T) {
	n := sexp.List(sexp.Atom("version"), sexp.Atom("20231120"))
	out := sexp.WriteNode(n)
	if out != "(version 20231120)" {
		t.Fatalf("unexpected: %q", out)
	}
}

// --- Roundtrip tests ---

func TestRoundtripSimple(t *testing.T) {
	input := "(version 20231120)"
	nodes, err := sexp.Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	out := strings.TrimRight(sexp.Write(nodes), "\n")
	if out != input {
		t.Fatalf("roundtrip mismatch:\ngot:  %q\nwant: %q", out, input)
	}
}

// TestRoundtripMinimalSchematic tests parsing and re-serializing a minimal
// .kicad_sch snippet without file I/O.
func TestRoundtripMinimalSchematic(t *testing.T) {
	input := `(kicad_sch
  (version 20231120)
  (generator eeschema)
  (lib_symbols)
)`
	sch, err := sexp.ParseSchematic(input)
	if err != nil {
		t.Fatal(err)
	}
	if sch.Version() != "20231120" {
		t.Fatalf("wrong version: %q", sch.Version())
	}
}

// TestSchematicAddSymbol verifies that AddSymbol appends a node and
// the result is parseable.
func TestSchematicAddSymbol(t *testing.T) {
	input := `(kicad_sch (version 20231120) (lib_symbols))`
	sch, err := sexp.ParseSchematic(input)
	if err != nil {
		t.Fatal(err)
	}
	sym := sexp.NewSymbolInstance("Device:R", "R1", "100", "", 100.0, 100.0, 0, 1, nil, "", true, true, nil)
	sch.AddSymbol(sym)

	out := sch.Serialize()
	if !strings.Contains(out, "Device:R") {
		t.Fatal("serialized output does not contain added symbol")
	}
	// Re-parse to verify structural integrity.
	_, err = sexp.ParseSchematic(out)
	if err != nil {
		t.Fatalf("re-parse after AddSymbol failed: %v", err)
	}
}

// TestExtractPinNumbersUnquoted verifies that pin numbers parsed from a
// kicad_sym definition are returned without surrounding quotes — and that
// when fed into NewSymbolInstance the resulting (pin "N" ...) entry has a
// single layer of quoting (regression for the (pin "\"1\"" ...) bug).
func TestExtractPinNumbersUnquoted(t *testing.T) {
	libDef := `(symbol "R"
  (symbol "R_0_1")
  (symbol "R_1_1"
    (pin passive line (at 0 3.81 270) (length 1.27)
      (name "~") (number "1"))
    (pin passive line (at 0 -3.81 90) (length 1.27)
      (name "~") (number "2"))))`
	nodes, err := sexp.Parse(libDef)
	if err != nil {
		t.Fatal(err)
	}
	nums := sexp.ExtractPinNumbers(nodes[0], 1)
	if len(nums) != 2 || nums[0] != "1" || nums[1] != "2" {
		t.Fatalf("expected [1 2], got %#v", nums)
	}

	sym := sexp.NewSymbolInstance("Device:R", "R1", "100", "", 50, 50, 0, 1, nums, "deadbeef", true, true, nil)
	out := sexp.WriteNode(sym)
	if strings.Contains(out, `\"`) {
		t.Fatalf("symbol serialization contains escaped quotes (double-quoted bug):\n%s", out)
	}
	if !strings.Contains(out, `(pin "1"`) || !strings.Contains(out, `(pin "2"`) {
		t.Fatalf("expected (pin \"1\" ...) and (pin \"2\" ...), got:\n%s", out)
	}
}

// TestPinDirection verifies that PinInfo.Direction is computed in screen
// coordinates (0=east, 90=north, 180=west, 270=south) and rotates with the
// symbol's rotation.
func TestPinDirection(t *testing.T) {
	libDef := `(symbol "R"
  (symbol "R_1_1"
    (pin passive line (at 0 3.81 270) (length 1.27)
      (name "~") (number "1"))
    (pin passive line (at 0 -3.81 90) (length 1.27)
      (name "~") (number "2"))))`
	nodes, err := sexp.Parse(libDef)
	if err != nil {
		t.Fatal(err)
	}
	libSyms := sexp.List(sexp.Atom("lib_symbols"), nodes[0])

	// Build a fake schematic with one R at (100,100) rot=0, then rot=90.
	for _, tc := range []struct {
		rot       float64
		wantDir1  float64 // pin 1 (above body, points up in unrotated)
		wantDir2  float64 // pin 2 (below body, points down in unrotated)
	}{
		{0, 90, 270},   // pin 1 north, pin 2 south
		{90, 180, 0},   // CCW 90 → pin 1 west, pin 2 east
		{180, 270, 90}, // CCW 180 → pin 1 south, pin 2 north
		{270, 0, 180},  // CCW 270 → pin 1 east, pin 2 west
	} {
		// Build a placed symbol instance and resolve via ReadSymbols.
		instSrc := `(kicad_sch
  ` + sexp.WriteNode(libSyms) + `
  (symbol (lib_id "Device:R")
    (at 100 100 ` + numStr(tc.rot) + `) (unit 1)
    (property "Reference" "R1" (at 100 100 0))
    (property "Value" "100" (at 100 100 0))
  ))`
		// Rename top-level symbol to qualified "Device:R" to match lib_id lookup.
		instSrc = replaceFirst(instSrc, `"R"`, `"Device:R"`)

		sch, err := sexp.ParseSchematic(instSrc)
		if err != nil {
			t.Fatalf("rot=%v parse: %v", tc.rot, err)
		}
		syms := sexp.ReadSymbols(sch)
		if len(syms) != 1 {
			t.Fatalf("rot=%v expected 1 symbol, got %d", tc.rot, len(syms))
		}
		var p1, p2 sexp.PinInfo
		for _, p := range syms[0].Pins {
			switch p.Number {
			case "1":
				p1 = p
			case "2":
				p2 = p
			}
		}
		if p1.Direction != tc.wantDir1 {
			t.Errorf("rot=%v pin1 direction: got %v want %v", tc.rot, p1.Direction, tc.wantDir1)
		}
		if p2.Direction != tc.wantDir2 {
			t.Errorf("rot=%v pin2 direction: got %v want %v", tc.rot, p2.Direction, tc.wantDir2)
		}
	}
}

// numStr is a tiny helper for the test to avoid importing fmt.
func numStr(f float64) string {
	if f == float64(int(f)) {
		return strconvI(int(f))
	}
	return strconvI(int(f)) // tests only use integer rotations
}

func strconvI(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [12]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

func replaceFirst(s, old, new string) string {
	if i := strings.Index(s, old); i >= 0 {
		return s[:i] + new + s[i+len(old):]
	}
	return s
}

// TestPCBEdgeCuts verifies that NewEdgeCutsRect produces gr_line nodes
// that serialize and re-parse correctly.
func TestPCBEdgeCuts(t *testing.T) {
	input := `(kicad_pcb (version 20221018))`
	pcb, err := sexp.ParsePCB(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range sexp.NewEdgeCutsRect(0, 0, 50, 30) {
		pcb.AddGrLine(line)
	}
	out := pcb.Serialize()
	if !strings.Contains(out, "Edge.Cuts") {
		t.Fatal("serialized output does not contain Edge.Cuts")
	}
	_, err = sexp.ParsePCB(out)
	if err != nil {
		t.Fatalf("re-parse after edge cuts failed: %v", err)
	}
}
