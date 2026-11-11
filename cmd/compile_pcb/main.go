// compile_pcb builds an unrouted .kicad_pcb next to a compiled .kicad_sch
// without going through the MCP server.
//
//	go run ./cmd/compile_pcb [-overwrite] <schematic.kicad_sch>
package main

import (
	"flag"
	"fmt"
	"os"

	"mcp-kicad/internal/config"
	"mcp-kicad/internal/tools"
)

func main() {
	overwrite := flag.Bool("overwrite", false, "replace an existing .kicad_pcb")
	noRoute := flag.Bool("noroute", false, "place only, do not route")
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
	res, err := env.CompilePCB(flag.Arg(0), *overwrite, !*noRoute)
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
