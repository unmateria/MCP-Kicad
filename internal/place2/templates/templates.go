// Package templates exposes a small library of canonical sub-circuit
// "substructures" the LLM can stamp into a schematic. Each template is a
// JSON file in embed/ describing components, nets, and the external pins
// the surrounding circuit needs to drive.
//
// Templates are a HIGH-LEVERAGE LLM hint: rather than reinventing the
// non-inverting op-amp pattern from scratch, the model issues
// `apply_template` and gets a known-good layout-and-netlist seed.
package templates

import (
	"embed"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
)

//go:embed embed/*.json
var fs embed.FS

// Component describes one symbol in a template instance.
type Component struct {
	RefPattern string  `json:"ref_pattern"` // "?" placeholder, replaced at apply time
	LibID      string  `json:"lib_id"`
	Value      string  `json:"value"`
	RelX       float64 `json:"rel_x"`
	RelY       float64 `json:"rel_y"`
	Rotation   float64 `json:"rotation"`
	Unit       int     `json:"unit,omitempty"`
	Role       string  `json:"role,omitempty"` // stable name used in nets/external_pins
	// SameRefAs names an earlier component's role whose reference designator this
	// component must reuse. Used to place a second unit of a multi-unit symbol
	// (e.g. an op-amp's power unit) under the same reference as its first unit.
	SameRefAs string `json:"same_ref_as,omitempty"`
}

// Wire is one baked orthogonal wire segment, in coordinates relative to the
// template origin (same convention as Component rel_x/rel_y).
type Wire struct {
	X1 float64 `json:"x1"`
	Y1 float64 `json:"y1"`
	X2 float64 `json:"x2"`
	Y2 float64 `json:"y2"`
}

// Junction is a baked solder dot where three or more wire ends meet, relative
// to the template origin.
type Junction struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Label is a baked net label placed at a fixed point relative to the template
// origin. Used for the sub-circuit's signal I/O (VIN, VOUT, SDA…). Two labels
// with the same name — here or in the surrounding circuit — are one net.
type Label struct {
	Name     string  `json:"name"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Rotation float64 `json:"rotation,omitempty"`
}

// Net is one electrical net inside the template. Pins are role-qualified
// (e.g. "Rf.1") so the template can refer to components symbolically.
type Net struct {
	Name string   `json:"name"`
	Pins []string `json:"pins"`
}

// ExternalPin describes a pin the surrounding circuit MUST connect.
type ExternalPin struct {
	Label    string `json:"label"`
	From     string `json:"from"`
	Describe string `json:"describe"`
}

// Template is one complete substructure spec.
//
// Wires, Junctions and Labels hold the BAKED drawing geometry: the sub-circuit
// is wired once, by hand, in coordinates relative to the template origin, and
// copied verbatim at stamp time. Nothing about the wiring is computed when a
// template is stamped, so a stamped template cannot look bad. Nets is the
// declared connectivity contract that the baked geometry must satisfy (verified
// in templates_test.go).
type Template struct {
	Name         string        `json:"name"`
	Description  string        `json:"description"`
	Components   []Component   `json:"components"`
	Nets         []Net         `json:"nets"`
	ExternalPins []ExternalPin `json:"external_pins"`
	Wires        []Wire        `json:"wires,omitempty"`
	Junctions    []Junction    `json:"junctions,omitempty"`
	Labels       []Label       `json:"labels,omitempty"`
}

// List returns every available template's name and one-line description.
func List() ([]Template, error) {
	entries, err := fs.ReadDir("embed")
	if err != nil {
		return nil, err
	}
	var templates []Template
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		t, err := load(e.Name())
		if err != nil {
			return nil, err
		}
		templates = append(templates, t)
	}
	sort.Slice(templates, func(i, j int) bool { return templates[i].Name < templates[j].Name })
	return templates, nil
}

// Get loads a template by canonical name (without extension).
func Get(name string) (Template, error) {
	return load(name + ".json")
}

func load(file string) (Template, error) {
	data, err := fs.ReadFile(path.Join("embed", file))
	if err != nil {
		return Template{}, fmt.Errorf("templates: read %s: %w", file, err)
	}
	var t Template
	if err := json.Unmarshal(data, &t); err != nil {
		return Template{}, fmt.Errorf("templates: parse %s: %w", file, err)
	}
	if t.Name == "" {
		t.Name = strings.TrimSuffix(file, ".json")
	}
	return t, nil
}
