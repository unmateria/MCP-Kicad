// compile_pcb builds an unrouted .kicad_pcb next to a compiled .kicad_sch
// without going through the MCP server.
//
//	go run ./cmd/compile_pcb [-overwrite] <schematic.kicad_sch>
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"mcp-kicad/internal/config"
	"mcp-kicad/internal/tools"
)

func main() {
	overwrite := flag.Bool("overwrite", false, "replace an existing .kicad_pcb")
	noRoute := flag.Bool("noroute", false, "place only, do not route")
	widths := flag.String("widths", "", "track width per net, e.g. HEATER=2,+BATT=2")
	single := flag.Bool("single", false, "single-sided board for home making: tracks on B.Cu, wire jumpers, milling pads")
	track := flag.Float64("track", 0, "signal track width, mm (0: default)")
	power := flag.Float64("power", 0, "supply track width, mm (0: default)")
	clearance := flag.Float64("clearance", 0, "copper clearance, mm (0: default)")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: compile_pcb [-overwrite] <schematic.kicad_sch>")
		os.Exit(2)
	}
	cfg := config.Load("config.ini")
	env := &tools.Env{
		KicadCLI:        cfg.KicadCLI,
		KicadSymbols:    cfg.KicadSymbols,
		KicadFootprints: cfg.KicadFootprints,
		LibsRoot:        cfg.LibsRoot,
		OutputDir:       cfg.OutputDir,
	}
	nw := map[string]float64{}
	for _, kv := range strings.Split(*widths, ",") {
		name, val, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		w, err := strconv.ParseFloat(val, 64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "compile_pcb: width %q: %v\n", kv, err)
			os.Exit(2)
		}
		nw[strings.TrimSpace(name)] = w
	}
	res, err := env.CompilePCB(flag.Arg(0), tools.PCBOptions{Overwrite: *overwrite, Route: !*noRoute, NetWidths: nw,
		SingleSided: *single, Track: *track, PowerTrack: *power, Clearance: *clearance})
	if res != nil {
		fmt.Print(res.Report)
		fmt.Println("board:", res.PCBPath)
		if res.PNGPath != "" {
			fmt.Println("png:", res.PNGPath)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "compile_pcb:", err)
		os.Exit(1)
	}
}
