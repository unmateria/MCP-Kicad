// Command measure_text prints the text collisions left in finished
// schematics — the objective version of "does this look readable".
package main

import (
	"fmt"
	"os"

	"mcp-kicad/internal/place2/textplace"
	"mcp-kicad/internal/sexp"
)

func main() {
	for _, path := range os.Args[1:] {
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Println("read:", err)
			continue
		}
		sch, err := sexp.ParseSchematic(string(data))
		if err != nil {
			fmt.Println("parse:", err)
			continue
		}
		cols := textplace.Collisions(sch)
		total := 0.0
		for _, c := range cols {
			total += c.Area
		}
		fmt.Printf("\n%s: %d collision(s), %.1f mm2 total\n", path, len(cols), total)
		for i, c := range cols {
			if i >= 40 {
				fmt.Printf("  ... %d more\n", len(cols)-40)
				break
			}
			fmt.Println("  ", c)
		}
	}
}
