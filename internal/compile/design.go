// Package compile turns a declarative .design.json source into a complete
// KiCad schematic. The source file is the editing surface; the generated
// .kicad_sch is a deterministic build artifact that is never edited by hand.
// Format specification: docs/compiler/FORMAT.md.
package compile

// Design mirrors the .design.json source format, version 1.
type Design struct {
	Version     int                 `json:"version"`
	Project     string              `json:"project"`
	Description string              `json:"description,omitempty"`
	Sheet       string              `json:"sheet,omitempty"` // "A4", "A3" or "auto" (default "auto")
	Blocks      []Block             `json:"blocks"`
	Arrange     [][]string          `json:"arrange,omitempty"`    // rows top→bottom, blocks left→right
	Nets        map[string][]string `json:"nets,omitempty"`       // net name -> "REF.pin" list
	PowerNets   map[string]string   `json:"power_nets,omitempty"` // net name -> power symbol lib_id
	NoConnect   NoConnect           `json:"no_connect,omitempty"`
}

// Block is either a template instance (Template != "") or an explicit block
// (Symbols non-empty). Never both.
type Block struct {
	Name     string            `json:"name"`
	Template string            `json:"template,omitempty"`
	Refs     map[string]string `json:"refs,omitempty"`    // template role -> real reference
	Connect  map[string]string `json:"connect,omitempty"` // template external label -> global net
	Symbols  []Symbol          `json:"symbols,omitempty"`
}

// Symbol is one placed component inside an explicit block. The first symbol
// of a block is the anchor: it sits at the block origin and has Place == nil.
type Symbol struct {
	Ref    string `json:"ref"`
	Lib    string `json:"lib"` // KiCad lib_id, e.g. "Device:C"
	Value  string `json:"value,omitempty"`
	Place  *Place `json:"place,omitempty"`
	Rot    *int   `json:"rot,omitempty"` // 0/90/180/270; nil = canonical default per class
	Mirror bool   `json:"mirror,omitempty"`
}

// Place anchors one pin of this symbol an exact number of grid cells away
// from a pin of a previously declared symbol in the same block. Placement is
// a tree: evaluation order is declaration order and can never fail to resolve.
type Place struct {
	Pin   string `json:"pin"`   // own pin (number or name)
	At    string `json:"at"`    // target "REF.pin"
	Dir   string `json:"dir"`   // "left" | "right" | "up" | "down"
	Cells int    `json:"cells"` // integer grid cells of 2.54 mm, >= 1
}

// NoConnect accepts two JSON shapes (custom unmarshalling in parse.go):
// an array of explicit "REF.pin" entries, or an object mapping a reference
// to the literal "unused", meaning every pin of that reference not claimed
// by nets or template internals gets a no_connect marker.
type NoConnect struct {
	Pins   []string
	Unused []string // references marked "unused", sorted
}
