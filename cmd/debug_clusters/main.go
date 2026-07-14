package main

import (
	"fmt"
	"os"
	"mcp-kicad/internal/place2/cluster"
	"mcp-kicad/internal/sexp"
)

func main() {
	data, _ := os.ReadFile(os.Args[1])
	sch, err := sexp.ParseSchematic(string(data))
	if err != nil { fmt.Println(err); return }
	syms := sexp.ReadSymbols(sch)
	nets := sexp.TraceNets(sch)
	fmt.Println("=== Nets ===")
	for _, n := range nets {
		var pins []string
		for _, p := range n.Pins { pins = append(pins, p.String()) }
		fmt.Printf("  %s: %v\n", n.Name, pins)
	}
	fmt.Println("=== Clusters ===")
	clusters := cluster.Detect(syms, nets)
	for _, c := range clusters {
		fmt.Printf("  %s anchor=%s refs=%v\n", c.Kind, c.Anchor, c.Refs)
	}
}
