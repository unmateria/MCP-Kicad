// export_fab writes the fabrication files of a board without going through
// the MCP server.
//
//	go run ./cmd/export_fab [-allow-unrouted] <board.kicad_pcb>
package main

import (
	"flag"
	"fmt"
	"os"

	"mcp-kicad/internal/config"
	"mcp-kicad/internal/tools"
)

func main() {
	allow := flag.Bool("allow-unrouted", false, "export even with connections left unrouted")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: export_fab [-allow-unrouted] <board.kicad_pcb>")
		os.Exit(2)
	}
	cfg := config.Load("config.ini")
	env := &tools.Env{KicadCLI: cfg.KicadCLI, KicadSymbols: cfg.KicadSymbols, KicadFootprints: cfg.KicadFootprints, LibsRoot: cfg.LibsRoot}
	res, err := env.ExportFabrication(flag.Arg(0), *allow)
	if err != nil {
		fmt.Fprintln(os.Stderr, "export_fab:", err)
		os.Exit(1)
	}
	fmt.Print(res.Report)
}
