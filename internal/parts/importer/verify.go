package importer

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"mcp-kicad/internal/fplib"
	"mcp-kicad/internal/sexp"
	"mcp-kicad/internal/symlib"
)

// Check is one verification step and how it went.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Fatal  bool   `json:"fatal,omitempty"` // a failure here cancels the import
	Detail string `json:"detail,omitempty"`
}

func checkOK(name, detail string) Check    { return Check{Name: name, OK: true, Detail: detail} }
func checkFatal(name, detail string) Check { return Check{Name: name, Fatal: true, Detail: detail} }
func checkWarn(name, detail string) Check  { return Check{Name: name, Detail: detail} }

// upgradeSymbolLib runs `kicad-cli sym upgrade` over a library and returns the
// rewritten bytes.
//
// This is the step that makes the whole importer trustworthy. It is not a
// formatting nicety: if KiCad cannot read a file back and write it out again,
// the file is not a valid library no matter how cleanly it parsed here. It
// also normalises the KiCad 5/6/7 symbols still circulating in the wild into
// the format the installed version wants, and replaces this repo's indentation
// heuristic with KiCad's own.
func upgradeSymbolLib(kicadCLI string, data []byte) ([]byte, error) {
	if kicadCLI == "" {
		return nil, fmt.Errorf("kicad-cli is not configured")
	}
	dir, err := os.MkdirTemp("", "mcp-kicad-symupgrade-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	in := filepath.Join(dir, "in.kicad_sym")
	out := filepath.Join(dir, "out.kicad_sym")
	if err := os.WriteFile(in, data, 0o644); err != nil {
		return nil, err
	}
	cmd := exec.Command(kicadCLI, "sym", "upgrade", "--output", out, "--force", in)
	if combined, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("%v: %s", err, strings.TrimSpace(string(combined)))
	}
	return os.ReadFile(out)
}

// upgradeFootprint runs `kicad-cli fp upgrade` over a one-footprint .pretty
// directory and returns the rewritten footprint.
func upgradeFootprint(kicadCLI string, data []byte, name string) ([]byte, error) {
	if kicadCLI == "" {
		return nil, fmt.Errorf("kicad-cli is not configured")
	}
	dir, err := os.MkdirTemp("", "mcp-kicad-fpupgrade-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	inPretty := filepath.Join(dir, "in.pretty")
	outPretty := filepath.Join(dir, "out.pretty")
	if err := os.MkdirAll(inPretty, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(inPretty, name+".kicad_mod"), data, 0o644); err != nil {
		return nil, err
	}
	cmd := exec.Command(kicadCLI, "fp", "upgrade", "--output", outPretty, "--force", inPretty)
	if combined, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("%v: %s", err, strings.TrimSpace(string(combined)))
	}
	return os.ReadFile(filepath.Join(outPretty, name+".kicad_mod"))
}

// probePins places the symbol in a scratch schematic and reads its pins back
// through the same machinery a real placement uses — sexp.ExtractSymbolDef-
// WithParents to embed, sexp.ReadSymbols to resolve.
//
// A symbol that parses, upgrades cleanly and still resolves to zero pins is
// the exact failure this whole package exists to catch: it looks installed,
// it appears in the chooser, and every net you write against it is silently
// unconnected.
func probePins(symFilePath, libName, partName string) ([]sexp.PinInfo, error) {
	content := fmt.Sprintf(`(kicad_sch (version 20231120) (generator "eeschema") (generator_version "9.0") (paper "A4")
  (uuid "%s")
  (lib_symbols)
  (sheet_instances
    (path "/" (page "1"))))
`, sexp.NewUUID())
	sch, err := sexp.ParseSchematic(content)
	if err != nil {
		return nil, err
	}
	defs, err := sexp.ExtractSymbolDefWithParents(symFilePath, libName, partName)
	if err != nil {
		return nil, err
	}
	for _, def := range defs {
		if !sch.HasLibSymbol(sexp.StringValue(def, 1)) {
			sch.AddLibSymbol(def)
		}
	}
	libID := libName + ":" + partName
	libDef := sch.FindLibDef(libID)
	if libDef == nil {
		return nil, fmt.Errorf("the symbol did not embed under %q", libID)
	}
	pinNums := sexp.ExtractPinNumbers(libDef, 1)
	sch.AddSymbol(sexp.NewSymbolInstance(libID, "XPROBE", "", "",
		0, 0, 0, 1, pinNums, sch.UUID(), false, false, libDef))

	for _, sym := range sexp.ReadSymbols(sch) {
		if sym.Reference == "XPROBE" {
			return sym.Pins, nil
		}
	}
	return nil, fmt.Errorf("the placed symbol produced no readable instance")
}

// comparePinsAndPads checks that the symbol's electrical pins line up with the
// footprint's numbered pads.
//
// Deliberately a warning and never a hard failure. Stacked pins (a power pin
// repeated on several units), thermal pads numbered like signals and multi-pad
// nets all break the equality legitimately, and refusing those parts would
// reject a large slice of what is worth importing.
func comparePinsAndPads(pins []sexp.PinInfo, fp *fplib.FP) Check {
	if fp == nil {
		return checkWarn("symbol/footprint agreement", "no footprint to compare against")
	}
	pinSet := map[string]bool{}
	for _, p := range pins {
		if p.Number != "" {
			pinSet[p.Number] = true
		}
	}
	padSet := map[string]bool{}
	for _, n := range fp.PadNumbers() {
		padSet[n] = true
	}

	var onlyPins, onlyPads []string
	for n := range pinSet {
		if !padSet[n] {
			onlyPins = append(onlyPins, n)
		}
	}
	for n := range padSet {
		if !pinSet[n] {
			onlyPads = append(onlyPads, n)
		}
	}
	sort.Strings(onlyPins)
	sort.Strings(onlyPads)

	detail := fmt.Sprintf("%d pins, %d numbered pads", len(pinSet), len(padSet))
	if mech := fp.MechanicalPads(); mech > 0 {
		detail += fmt.Sprintf(" (+%d mechanical)", mech)
	}
	if len(onlyPins) == 0 && len(onlyPads) == 0 {
		return checkOK("symbol/footprint agreement", detail+", every number matches")
	}
	if len(onlyPins) > 0 {
		detail += fmt.Sprintf("; pins with no pad: %s", strings.Join(onlyPins, ","))
	}
	if len(onlyPads) > 0 {
		detail += fmt.Sprintf("; pads with no pin: %s", strings.Join(onlyPads, ","))
	}
	return checkWarn("symbol/footprint agreement", detail+" — check this against the datasheet before routing")
}

// renderSymbolSVG asks KiCad to draw the symbol, so a human can see in one
// second what no geometric check would catch: crossed pins, a body drawn over
// its own labels, a part that is simply not the part it claims to be.
func renderSymbolSVG(kicadCLI, symFilePath, outDir string) (string, error) {
	if kicadCLI == "" {
		return "", fmt.Errorf("kicad-cli is not configured")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}
	cmd := exec.Command(kicadCLI, "sym", "export", "svg", "--output", outDir, symFilePath)
	if combined, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(string(combined)))
	}
	return firstSVG(outDir)
}

// renderFootprintSVG does the same for the footprint.
func renderFootprintSVG(kicadCLI, prettyDir, name, outDir string) (string, error) {
	if kicadCLI == "" {
		return "", fmt.Errorf("kicad-cli is not configured")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}
	// Copper, paste and silkscreen only. The fabrication layer carries the
	// Value text, which is the footprint's full name — forty-odd characters
	// at 1 mm next to a 4 mm package, so the picture becomes a caption with
	// some pads hiding under it.
	cmd := exec.Command(kicadCLI, "fp", "export", "svg",
		"--output", outDir, "--layers", "F.Cu,F.Paste,F.SilkS",
		"--footprint", name, prettyDir)
	if combined, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(string(combined)))
	}
	return firstSVG(outDir)
}

// firstSVG returns the newest .svg in dir. kicad-cli names its output after
// the symbol, and the name it picks is not always the one we asked for.
func firstSVG(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".svg") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("kicad-cli wrote no SVG into %s", dir)
	}
	sort.Strings(names)
	return filepath.Join(dir, names[0]), nil
}

// symbolFromLibBytes parses a one-symbol library and returns its only symbol.
func symbolFromLibBytes(data []byte) (*symlib.Lib, *sexp.Node, error) {
	lib, err := symlib.Parse(data)
	if err != nil {
		return nil, nil, err
	}
	syms := lib.Symbols()
	if len(syms) != 1 {
		return nil, nil, fmt.Errorf("expected exactly one symbol, found %d", len(syms))
	}
	return lib, syms[0], nil
}
