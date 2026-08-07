// compile builds a .kicad_sch from a declarative .design.json source without
// going through the MCP server. Iteration loop: edit source → run this →
// inspect PNG/report.
//
//	go run ./cmd/compile <design.json> [-o out.kicad_sch]
package main

import (
	"flag"
	"fmt"
	"os"

	"mcp-kicad/internal/config"
	"mcp-kicad/internal/tools"
)

func main() {
	out := flag.String("o", "", "output .kicad_sch path (default: <design dir>/<project>.kicad_sch)")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: compile [-o out.kicad_sch] <design.json>")
		os.Exit(2)
	}

	cfg := config.Load("config.ini")
	env := &tools.Env{
		KicadCLI:        cfg.KicadCLI,
		KicadSymbols:    cfg.KicadSymbols,
		KicadFootprints: cfg.KicadFootprints,
		LibsRoot:        cfg.LibsRoot,
		OutputDir:       cfg.OutputDir,
		ConfigPath:      cfg.ConfigPath,
	}

	res, err := env.CompileDesign(flag.Arg(0), *out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "compile:", err)
		os.Exit(1)
	}
	fmt.Println(res.Report)
	fmt.Println("schematic:", res.SchematicPath)
	if res.PNGPath != "" {
		fmt.Println("png:", res.PNGPath)
	}
}
