package main
import ("fmt";"os";"mcp-kicad/internal/sexp")
func main() {
  data, _ := os.ReadFile(os.Args[1])
  sch, _ := sexp.ParseSchematic(string(data))
  for _, n := range sexp.TraceNets(sch) {
    var pins []string
    for _, p := range n.Pins { pins = append(pins, p.String()) }
    fmt.Printf("  %s: %v\n", n.Name, pins)
  }
}
