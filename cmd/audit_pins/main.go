// audit_pins reports, for every symbol in a .design.json source, which of its
// pins the source connects and what each pin's electrical type is.
//
// It exists because of a defect nobody's tests could have caught: the buck
// converter demo hung its inductor off the LM2596's ~ON/OFF pin — an INPUT —
// and left pin 2, the switching OUTPUT, dangling. Every automated check passed,
// because the compiler implements the netlist you declare and that netlist was
// declared wrong. Two readers on the KiCad forum spotted it in seconds.
//
// This is a reading aid, not a rule engine. It flags the shape of that mistake
// — an unused output next to a loaded input — and prints the rest for a human
// to look at. Deciding whether a circuit is correct is not something this
// program can do.
//
//	go run ./cmd/audit_pins docs/compiler/*.design.json
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mcp-kicad/internal/config"
	"mcp-kicad/internal/sexp"
)

type design struct {
	Project string `json:"project"`
	Blocks  []struct {
		Symbols []struct {
			Ref string `json:"ref"`
			Lib string `json:"lib"`
		} `json:"symbols"`
	} `json:"blocks"`
	Nets map[string][]string `json:"nets"`
	// no_connect is either a list of "REF.pin" tokens or a map of REF → policy
	// ("unused" marks every spare pin of that symbol), so it is read raw.
	NoConnect json.RawMessage `json:"no_connect"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: audit_pins <design.json>...")
		os.Exit(2)
	}
	cfg := config.Load("config.ini")
	problems := 0

	for _, path := range os.Args[1:] {
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			continue
		}
		var d design
		if err := json.Unmarshal(data, &d); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			continue
		}

		// Which "REF.pin" tokens the source mentions anywhere.
		used := map[string]bool{}
		for _, pins := range d.Nets {
			for _, p := range pins {
				used[p] = true
			}
		}
		var ncList []string
		var ncMap map[string]string
		if len(d.NoConnect) > 0 {
			if json.Unmarshal(d.NoConnect, &ncList) == nil {
				for _, p := range ncList {
					used[p] = true
				}
			} else if json.Unmarshal(d.NoConnect, &ncMap) != nil {
				fmt.Fprintf(os.Stderr, "%s: unreadable no_connect\n", path)
			}
		}

		fmt.Printf("\n=== %s (%s) ===\n", d.Project, filepath.Base(path))
		for _, b := range d.Blocks {
			for _, s := range b.Symbols {
				pins, err := symbolPins(cfg.KicadSymbols, s.Lib)
				if err != nil {
					fmt.Printf("  %-5s %-40s  (%v)\n", s.Ref, s.Lib, err)
					continue
				}
				// Only multi-pin actives are worth the noise; a resistor's two
				// passive pins tell you nothing.
				interesting := false
				for _, p := range pins {
					if p.Electrical != "passive" {
						interesting = true
					}
				}
				if !interesting || len(pins) < 3 {
					continue
				}

				// A symbol whose spare pins are declared unused wholesale has
				// nothing left to flag: the author already said so.
				blanket := ncMap[s.Ref] != ""

				var unusedOut, usedIn []string
				fmt.Printf("  %s  %s\n", s.Ref, s.Lib)
				for _, p := range pins {
					name := p.Name
					if name == "" || name == "~" {
						name = p.Number
					}
					mark := "   "
					switch {
					case used[s.Ref+"."+name], used[s.Ref+"."+p.Number]:
						mark = " * "
					default:
						mark = " . "
					}
					fmt.Printf("   %s pin %-3s %-14s %s\n", mark, p.Number, name, p.Electrical)

					connected := used[s.Ref+"."+name] || used[s.Ref+"."+p.Number]
					switch p.Electrical {
					case "output", "open_collector", "open_emitter", "tri_state", "power_out":
						if !connected {
							unusedOut = append(unusedOut, fmt.Sprintf("%s(%s)", name, p.Number))
						}
					case "input":
						if connected {
							usedIn = append(usedIn, fmt.Sprintf("%s(%s)", name, p.Number))
						}
					}
				}
				if len(unusedOut) > 0 && len(usedIn) > 0 && !blanket {
					problems++
					fmt.Printf("   !! output pin(s) %s are unconnected while input pin(s) %s carry the circuit.\n",
						strings.Join(unusedOut, ", "), strings.Join(usedIn, ", "))
					fmt.Printf("      That is the shape of the LM2596 defect. Check it against the datasheet.\n")
				}
			}
		}
	}
	fmt.Printf("\n%d symbol(s) worth a second look.\n", problems)
}

// symbolPins resolves a lib_id's pins through the same flattening the
// compiler uses, so a derived symbol reports the pins it inherits.
func symbolPins(symbolsDir, libID string) ([]sexp.PinInfo, error) {
	parts := strings.SplitN(libID, ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("not a lib_id")
	}
	file := filepath.Join(symbolsDir, parts[0]+".kicad_sym")
	defs, err := sexp.ExtractSymbolDefWithParents(file, parts[0], parts[1])
	if err != nil {
		return nil, err
	}
	sch, err := sexp.ParseSchematic(fmt.Sprintf(
		`(kicad_sch (version 20231120) (generator "audit") (paper "A4") (uuid "%s") (lib_symbols) (sheet_instances (path "/" (page "1"))))`,
		sexp.NewUUID()))
	if err != nil {
		return nil, err
	}
	for _, def := range defs {
		if !sch.HasLibSymbol(sexp.StringValue(def, 1)) {
			sch.AddLibSymbol(def)
		}
	}
	libDef := sch.FindLibDef(libID)
	if libDef == nil {
		return nil, fmt.Errorf("did not embed")
	}
	sch.AddSymbol(sexp.NewSymbolInstance(libID, "XAUDIT", "", "", 0, 0, 0, 1,
		sexp.ExtractPinNumbers(libDef, 1), sch.UUID(), false, false, libDef))
	for _, sym := range sexp.ReadSymbols(sch) {
		if sym.Reference == "XAUDIT" {
			pins := sym.Pins
			sort.Slice(pins, func(i, j int) bool { return pins[i].Number < pins[j].Number })
			return pins, nil
		}
	}
	return nil, fmt.Errorf("no instance")
}
