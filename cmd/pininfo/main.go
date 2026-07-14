// Temporary utility to extract pin positions from a .kicad_sym library file.
package main

import (
	"fmt"
	"os"
	"strings"

	"mcp-kicad/internal/sexp"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: pininfo <library.kicad_sym> [SymbolName]")
		os.Exit(1)
	}
	libPath := os.Args[1]
	symName := ""
	if len(os.Args) >= 3 {
		symName = os.Args[2]
	}

	data, err := os.ReadFile(libPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read error:", err)
		os.Exit(1)
	}

	nodes, err := sexp.Parse(string(data))
	if err != nil {
		fmt.Fprintln(os.Stderr, "parse error:", err)
		os.Exit(1)
	}
	root := nodes[0] // kicad_symbol_lib

	for _, child := range root.Children {
		if !child.IsList() || child.Head() != "symbol" {
			continue
		}
		// Name may be a string node or atom
		name := sexp.StringValue(child, 1)
		if name == "" {
			name = sexp.AtomValue(child, 1)
		}

		if symName == "" {
			fmt.Printf("  %s\n", name)
			continue
		}
		if !strings.EqualFold(name, symName) {
			continue
		}
		fmt.Printf("=== Symbol: %s ===\n", name)
		printPins(child, "")
	}
}

func printPins(n *sexp.Node, indent string) {
	for _, child := range n.Children {
		if !child.IsList() {
			continue
		}
		if child.Head() == "pin" {
			var x, y, angle, pinNum, pinName string
			for _, pc := range child.Children {
				if !pc.IsList() {
					continue
				}
				switch pc.Head() {
				case "at":
					x = sexp.AtomValue(pc, 1)
					y = sexp.AtomValue(pc, 2)
					angle = sexp.AtomValue(pc, 3)
				case "number":
					pinNum = sexp.StringValue(pc, 1)
					if pinNum == "" {
						pinNum = sexp.AtomValue(pc, 1)
					}
				case "name":
					pinName = sexp.StringValue(pc, 1)
					if pinName == "" {
						pinName = sexp.AtomValue(pc, 1)
					}
				}
			}
			fmt.Printf("%spin %-3s (%-10s) at (%s, %s) angle=%s\n", indent, pinNum, pinName, x, y, angle)
		} else if child.Head() == "symbol" {
			subName := sexp.StringValue(child, 1)
			if subName == "" {
				subName = sexp.AtomValue(child, 1)
			}
			fmt.Printf("%s--- sub: %s ---\n", indent, subName)
			printPins(child, indent+"  ")
		}
	}
}
