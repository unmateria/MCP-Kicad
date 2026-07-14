package main

import (
	"fmt"
	"os"
	"mcp-kicad/internal/config"
	"mcp-kicad/internal/tools"
)

func main() {
	if len(os.Args) < 2 { fmt.Println("usage: render_sch <sch>"); os.Exit(1) }
	sch := os.Args[1]
	cli := config.DetectKicadCLI()
	if cli == "" {
		fmt.Println("err: kicad-cli not found")
		os.Exit(1)
	}
	png, err := tools.RenderSchematicPNG(sch, cli, ".")
	if err != nil { fmt.Println("err:", err); os.Exit(1) }
	out := sch + ".png"
	os.WriteFile(out, png, 0o644)
	fmt.Println("wrote", out, len(png), "bytes")
}
