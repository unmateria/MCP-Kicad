package compile

import (
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseDesignFileDemoFullBoard(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "compiler", "demo_full_board.design.json")
	d, err := ParseDesignFile(path)
	if err != nil {
		t.Fatalf("ParseDesignFile: %v", err)
	}

	if d.Version != 1 || d.Project != "demo_full_board" || d.Sheet != "auto" {
		t.Errorf("header = %d/%q/%q, want 1/demo_full_board/auto", d.Version, d.Project, d.Sheet)
	}
	if len(d.Blocks) != 5 {
		t.Fatalf("len(Blocks) = %d, want 5", len(d.Blocks))
	}

	if d.Blocks[0].Name != "input" || len(d.Blocks[0].Symbols) != 1 || d.Blocks[0].Symbols[0].Ref != "J1" {
		t.Errorf("block 0 = %q, want input with J1", d.Blocks[0].Name)
	}

	power := d.Blocks[1]
	if power.Name != "power" || power.Template != "voltage_regulator_linear" {
		t.Errorf("block 1 = %q/%q, want power/voltage_regulator_linear", power.Name, power.Template)
	}
	wantRefs := map[string]string{
		"REG": "U2", "C_IN_BYP": "C4", "C_IN_BULK": "C5",
		"C_OUT_BYP": "C6", "C_OUT_BULK": "C7", "R_LED": "R3", "LED": "D1",
	}
	if !maps.Equal(power.Refs, wantRefs) {
		t.Errorf("power refs = %v, want %v", power.Refs, wantRefs)
	}
	if !maps.Equal(power.Connect, map[string]string{"VIN": "VIN", "VOUT": "+5V"}) {
		t.Errorf("power connect = %v", power.Connect)
	}

	mcu := d.Blocks[2]
	if mcu.Name != "mcu" || len(mcu.Symbols) != 7 {
		t.Fatalf("block 2 = %q with %d symbols, want mcu with 7", mcu.Name, len(mcu.Symbols))
	}
	if mcu.Symbols[0].Ref != "U1" || mcu.Symbols[0].Place != nil {
		t.Errorf("mcu anchor = %q place=%v, want U1 with no place", mcu.Symbols[0].Ref, mcu.Symbols[0].Place)
	}
	c1 := mcu.Symbols[1]
	if c1.Ref != "C1" || c1.Place == nil {
		t.Fatalf("mcu symbol 1 = %+v, want C1 with place", c1)
	}
	if *c1.Place != (Place{Pin: "1", At: "U1.VCC", Dir: "left", Cells: 8}) {
		t.Errorf("C1 place = %+v", *c1.Place)
	}

	if d.Blocks[3].Name != "i2c" || d.Blocks[3].Template != "i2c_pullups" {
		t.Errorf("block 3 = %q/%q, want i2c/i2c_pullups", d.Blocks[3].Name, d.Blocks[3].Template)
	}
	if d.Blocks[4].Name != "output" || len(d.Blocks[4].Symbols) != 1 || d.Blocks[4].Symbols[0].Ref != "J2" {
		t.Errorf("block 4 = %q, want output with J2", d.Blocks[4].Name)
	}

	if !slices.Equal(d.NoConnect.Unused, []string{"U1"}) {
		t.Errorf("NoConnect.Unused = %v, want [U1]", d.NoConnect.Unused)
	}
	if len(d.NoConnect.Pins) != 0 {
		t.Errorf("NoConnect.Pins = %v, want empty", d.NoConnect.Pins)
	}
	if len(d.Nets) != 7 {
		t.Errorf("len(Nets) = %d, want 7", len(d.Nets))
	}
	if !slices.Equal(d.Nets["XTAL1"], []string{"U1.7", "Y1.1", "C8.1"}) {
		t.Errorf("Nets[XTAL1] = %v", d.Nets["XTAL1"])
	}
	if len(d.PowerNets) != 2 || d.PowerNets["+5V"] != "power:+5V" || d.PowerNets["GND"] != "power:GND" {
		t.Errorf("PowerNets = %v", d.PowerNets)
	}
	if !slices.EqualFunc(d.Arrange, [][]string{{"input", "power"}, {"mcu", "i2c", "output"}}, slices.Equal) {
		t.Errorf("Arrange = %v", d.Arrange)
	}
}

func TestParseDesignRejects(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "version not 1",
			src: `{"version":2,"project":"p","blocks":[
				{"name":"b","symbols":[{"ref":"U1","lib":"Device:R"}]}]}`,
			want: "version 2",
		},
		{
			name: "project with path separator",
			src: `{"version":1,"project":"sub/p","blocks":[
				{"name":"b","symbols":[{"ref":"U1","lib":"Device:R"}]}]}`,
			want: `project "sub/p"`,
		},
		{
			name: "unknown sheet",
			src: `{"version":1,"project":"p","sheet":"A5","blocks":[
				{"name":"b","symbols":[{"ref":"U1","lib":"Device:R"}]}]}`,
			want: `sheet "A5"`,
		},
		{
			name: "duplicate ref across explicit block and template refs",
			src: `{"version":1,"project":"p","blocks":[
				{"name":"b","symbols":[{"ref":"U1","lib":"Device:R"}]},
				{"name":"t","template":"i2c_pullups","refs":{"R_SDA":"U1"}}]}`,
			want: `reference "U1" is already used by block "b"`,
		},
		{
			name: "anchor declared later in the same block",
			src: `{"version":1,"project":"p","blocks":[{"name":"b","symbols":[
				{"ref":"U1","lib":"Device:R"},
				{"ref":"C1","lib":"Device:C","place":{"pin":"1","at":"C2.1","dir":"left","cells":1}},
				{"ref":"C2","lib":"Device:C","place":{"pin":"1","at":"U1.1","dir":"left","cells":1}}]}]}`,
			want: `anchors to "C2"`,
		},
		{
			name: "anchor in another block",
			src: `{"version":1,"project":"p","blocks":[
				{"name":"b1","symbols":[{"ref":"U1","lib":"Device:R"}]},
				{"name":"b2","symbols":[
					{"ref":"C1","lib":"Device:C"},
					{"ref":"C2","lib":"Device:C","place":{"pin":"1","at":"U1.1","dir":"left","cells":1}}]}]}`,
			want: `anchors to "U1", which is not a symbol declared earlier in block "b2"`,
		},
		{
			name: "invalid dir",
			src: `{"version":1,"project":"p","blocks":[{"name":"b","symbols":[
				{"ref":"U1","lib":"Device:R"},
				{"ref":"C1","lib":"Device:C","place":{"pin":"1","at":"U1.1","dir":"diagonal","cells":1}}]}]}`,
			want: `place.dir "diagonal"`,
		},
		{
			name: "cells zero",
			src: `{"version":1,"project":"p","blocks":[{"name":"b","symbols":[
				{"ref":"U1","lib":"Device:R"},
				{"ref":"C1","lib":"Device:C","place":{"pin":"1","at":"U1.1","dir":"left","cells":0}}]}]}`,
			want: "place.cells 0",
		},
		{
			name: "rot 45",
			src: `{"version":1,"project":"p","blocks":[{"name":"b","symbols":[
				{"ref":"U1","lib":"Device:R","rot":45}]}]}`,
			want: "rot 45",
		},
		{
			name: "template and symbols together",
			src: `{"version":1,"project":"p","blocks":[
				{"name":"b","template":"i2c_pullups","symbols":[{"ref":"U1","lib":"Device:R"}]}]}`,
			want: `block "b": has both "template" and "symbols"`,
		},
		{
			name: "first symbol has place",
			src: `{"version":1,"project":"p","blocks":[{"name":"b","symbols":[
				{"ref":"U1","lib":"Device:R","place":{"pin":"1","at":"U9.1","dir":"left","cells":1}}]}]}`,
			want: `symbol "U1": is the block anchor and must not have "place"`,
		},
		{
			name: "second symbol has no place",
			src: `{"version":1,"project":"p","blocks":[{"name":"b","symbols":[
				{"ref":"U1","lib":"Device:R"},
				{"ref":"C1","lib":"Device:C"}]}]}`,
			want: `symbol "C1": needs "place"`,
		},
		{
			name: "malformed lib id",
			src: `{"version":1,"project":"p","blocks":[{"name":"b","symbols":[
				{"ref":"U1","lib":"DeviceR"}]}]}`,
			want: `lib "DeviceR"`,
		},
		{
			name: "pin in two nets",
			src: `{"version":1,"project":"p","blocks":[{"name":"b","symbols":[
				{"ref":"U1","lib":"Device:R"}]}],
				"nets":{"A":["U1.1"],"B":["U1.1"]}}`,
			want: `net "B": pin "U1.1" is already claimed by net "A"`,
		},
		{
			name: "label_nets names an undeclared net",
			src: `{"version":1,"project":"p","blocks":[{"name":"b","symbols":[
				{"ref":"U1","lib":"Device:R"}]}],
				"nets":{"A":["U1.1"]},"label_nets":["B"]}`,
			want: `label_nets: "B" is not a declared net`,
		},
		{
			name: "label_nets names a power net",
			src: `{"version":1,"project":"p","blocks":[{"name":"b","symbols":[
				{"ref":"U1","lib":"Device:R"}]}],
				"nets":{"GND":["U1.1"]},"power_nets":{"GND":"power:GND"},"label_nets":["GND"]}`,
			want: `label_nets: "GND" is a power net`,
		},
		{
			name: "arrange references unknown block",
			src: `{"version":1,"project":"p","blocks":[{"name":"b","symbols":[
				{"ref":"U1","lib":"Device:R"}]}],
				"arrange":[["b","mcuu"]]}`,
			want: `arrange row 1: unknown block "mcuu"`,
		},
		{
			name: "arrange lists a block twice",
			src: `{"version":1,"project":"p","blocks":[{"name":"b","symbols":[
				{"ref":"U1","lib":"Device:R"}]}],
				"arrange":[["b"],["b"]]}`,
			want: `arrange row 2: block "b" is listed more than once`,
		},
		{
			name: "pin twice in the same net",
			src: `{"version":1,"project":"p","blocks":[{"name":"b","symbols":[
				{"ref":"U1","lib":"Device:R"}]}],
				"nets":{"A":["U1.1","U1.1"]}}`,
			want: `net "A": pin "U1.1" is listed twice`,
		},
		{
			name: "unknown ref in nets",
			src: `{"version":1,"project":"p","blocks":[{"name":"b","symbols":[
				{"ref":"U1","lib":"Device:R"}]}],
				"nets":{"A":["Q9.1"]}}`,
			want: `unknown reference "Q9"`,
		},
		{
			name: "power net not declared anywhere",
			src: `{"version":1,"project":"p","blocks":[{"name":"b","symbols":[
				{"ref":"U1","lib":"Device:R"}]}],
				"power_nets":{"+3V3":"power:+3V3"}}`,
			want: `power_nets "+3V3"`,
		},
		{
			name: "power net lib id without power prefix",
			src: `{"version":1,"project":"p","blocks":[{"name":"b","symbols":[
				{"ref":"U1","lib":"Device:R"}]}],
				"nets":{"GND":["U1.1"]},
				"power_nets":{"GND":"Device:GND"}}`,
			want: `"Device:GND" must have the form "power:Name"`,
		},
		{
			name: "no_connect pin also in a net",
			src: `{"version":1,"project":"p","blocks":[{"name":"b","symbols":[
				{"ref":"U1","lib":"Device:R"}]}],
				"nets":{"N1":["U1.1"]},
				"no_connect":["U1.1"]}`,
			want: `no_connect: pin "U1.1" is also connected by net "N1"`,
		},
		{
			name: "no_connect unknown ref",
			src: `{"version":1,"project":"p","blocks":[{"name":"b","symbols":[
				{"ref":"U1","lib":"Device:R"}]}],
				"no_connect":{"U7":"unused"}}`,
			want: `no_connect: "U7" is marked "unused"`,
		},
		{
			name: "no_connect object value other than unused",
			src: `{"version":1,"project":"p","blocks":[{"name":"b","symbols":[
				{"ref":"U1","lib":"Device:R"}]}],
				"no_connect":{"U1":"spare"}}`,
			want: `no_connect: reference "U1"`,
		},
		{
			name: "no blocks",
			src:  `{"version":1,"project":"p","blocks":[]}`,
			want: "blocks: at least one block is required",
		},
		{
			name: "duplicate block name",
			src: `{"version":1,"project":"p","blocks":[
				{"name":"b","symbols":[{"ref":"U1","lib":"Device:R"}]},
				{"name":"b","symbols":[{"ref":"U2","lib":"Device:R"}]}]}`,
			want: `block "b": duplicate block name`,
		},
		{
			name: "block with neither template nor symbols",
			src:  `{"version":1,"project":"p","blocks":[{"name":"b"}]}`,
			want: `block "b": needs either "template" or "symbols"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := ParseDesign([]byte(tc.src))
			if err == nil {
				t.Fatalf("ParseDesign accepted an invalid design: %+v", d)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestParseDesignNoConnectShapes(t *testing.T) {
	const base = `{"version":1,"project":"p","blocks":[{"name":"b","symbols":[
		{"ref":"U1","lib":"Device:R"},
		{"ref":"U2","lib":"Device:R","place":{"pin":"1","at":"U1.1","dir":"left","cells":2}}]}]`

	t.Run("array", func(t *testing.T) {
		d, err := ParseDesign([]byte(base + `,"no_connect":["U1.3","U1.9"]}`))
		if err != nil {
			t.Fatalf("ParseDesign: %v", err)
		}
		if !slices.Equal(d.NoConnect.Pins, []string{"U1.3", "U1.9"}) {
			t.Errorf("Pins = %v, want [U1.3 U1.9]", d.NoConnect.Pins)
		}
		if len(d.NoConnect.Unused) != 0 {
			t.Errorf("Unused = %v, want empty", d.NoConnect.Unused)
		}
	})

	t.Run("object", func(t *testing.T) {
		d, err := ParseDesign([]byte(base + `,"no_connect":{"U2":"unused","U1":"unused"}}`))
		if err != nil {
			t.Fatalf("ParseDesign: %v", err)
		}
		if !slices.Equal(d.NoConnect.Unused, []string{"U1", "U2"}) {
			t.Errorf("Unused = %v, want sorted [U1 U2]", d.NoConnect.Unused)
		}
		if len(d.NoConnect.Pins) != 0 {
			t.Errorf("Pins = %v, want empty", d.NoConnect.Pins)
		}
	})

	t.Run("absent", func(t *testing.T) {
		d, err := ParseDesign([]byte(base + `}`))
		if err != nil {
			t.Fatalf("ParseDesign: %v", err)
		}
		if len(d.NoConnect.Pins) != 0 || len(d.NoConnect.Unused) != 0 {
			t.Errorf("NoConnect = %+v, want zero value", d.NoConnect)
		}
	})

	t.Run("wrong json type", func(t *testing.T) {
		if _, err := ParseDesign([]byte(base + `,"no_connect":"U1"}`)); err == nil {
			t.Fatal("ParseDesign accepted a string no_connect")
		}
	})
}

func TestParseDesignErrorsAreDeterministic(t *testing.T) {
	src := []byte(`{"version":1,"project":"p","blocks":[{"name":"b","symbols":[
		{"ref":"U1","lib":"Device:R"}]}],
		"nets":{"A":["Z1.1"],"B":["Z2.1"],"C":["Z3.1"],"D":["Z4.1"]},
		"power_nets":{"P1":"bad","P2":"bad","P3":"bad"}}`)

	_, first := ParseDesign(src)
	if first == nil {
		t.Fatal("ParseDesign accepted an invalid design")
	}
	for i := 0; i < 20; i++ {
		_, err := ParseDesign(src)
		if err == nil || err.Error() != first.Error() {
			t.Fatalf("run %d produced a different error:\n%v\nwant:\n%v", i, err, first)
		}
	}
}

func TestParseDesignFileMissing(t *testing.T) {
	if _, err := ParseDesignFile(filepath.Join("testdata", "does_not_exist.design.json")); err == nil {
		t.Fatal("ParseDesignFile accepted a missing file")
	}
}
