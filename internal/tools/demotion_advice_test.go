package tools

import (
	"strconv"
	"strings"
	"testing"

	"mcp-kicad/internal/sexp"
)

// Two parts whose pins point away from each other cannot be joined by a
// straight or L-shaped wire at ANY distance — both wires leave in the wrong
// direction and one has to loop back, which the gate refuses. A session spent
// four recompiles trying 3, 6, 12 and 26 cells on exactly this before working
// out that spacing was never the variable.
func TestDemotionAdviceNamesTheRealFix(t *testing.T) {
	const lib = `
	(lib_symbols
		(symbol "Device:R"
			(symbol "R_1_1"
				(pin passive line (at -2.54 0 0) (length 2.54) (number "1" (effects (font (size 1.27 1.27)))) (name "~" (effects (font (size 1.27 1.27)))))
				(pin passive line (at 2.54 0 180) (length 2.54) (number "2" (effects (font (size 1.27 1.27)))) (name "~" (effects (font (size 1.27 1.27)))))
			)
		)
	)`
	// R1 on the left, R2 on the right. The wire joins R1's LEFT-facing pin to
	// R2's RIGHT-facing pin: each points away from the other.
	res := func(ref string, x float64) string {
		return `
		(symbol (lib_id "Device:R") (at ` + fmtF(x) + ` 50 0) (unit 1) (in_bom yes) (on_board yes) (uuid "` + ref + `-u")
			(property "Reference" "` + ref + `" (at ` + fmtF(x) + ` 46 0) (effects (font (size 1.27 1.27))))
			(property "Value" "10k" (at ` + fmtF(x) + ` 54 0) (effects (font (size 1.27 1.27)))))`
	}
	src := `(kicad_sch (version 20231120) (generator "test")
		(uuid "00000000-0000-4000-8000-000000000000")` + lib +
		res("R1", 30) + res("R2", 80) + `
		(wire (pts (xy 27.46 50) (xy 82.54 50)) (stroke (width 0) (type default)) (uuid "w1")))`

	sch, err := sexp.ParseSchematic(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var netName string
	for _, n := range sexp.TraceNets(sch) {
		if len(n.Pins) == 2 {
			netName = n.Name
		}
	}
	if netName == "" {
		t.Fatal("fixture did not produce a two-pin net")
	}

	advice := demotionAdvice(sch, netName)
	if advice == "" {
		t.Fatal("pins facing away from each other produced no advice; the author would keep tuning cells forever")
	}
	if !strings.Contains(advice, "rotate") && !strings.Contains(advice, "mirror") {
		t.Errorf("advice should point at rotation/mirror, got %q", advice)
	}
	t.Logf("advice: %s", advice)
}

func fmtF(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
