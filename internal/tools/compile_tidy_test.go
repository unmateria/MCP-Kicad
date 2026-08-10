package tools

import (
	"path/filepath"
	"testing"

	"mcp-kicad/internal/compile"
	"mcp-kicad/internal/config"
	"mcp-kicad/internal/place2/textplace"
)

// tidyEnv builds an Env against the installed KiCad. The geometry pipeline
// needs the real symbol libraries; it does not need kicad-cli.
func tidyEnv(t *testing.T) *Env {
	t.Helper()
	cli := config.DetectKicadCLI()
	if cli == "" {
		t.Skip("KiCad not installed")
	}
	root := filepath.Dir(filepath.Dir(cli))
	return &Env{
		KicadSymbols:    filepath.Join(root, "share", "kicad", "symbols"),
		KicadFootprints: filepath.Join(root, "share", "kicad", "footprints"),
	}
}

func loadDesign(t *testing.T, name string) *compile.Design {
	t.Helper()
	d, err := compile.ParseDesignFile(filepath.Join("..", "..", "docs", "compiler", name))
	if err != nil {
		t.Skipf("%s: %v", name, err)
	}
	return d
}

func countCollisions(t *testing.T, e *Env, d *compile.Design) (total int, area float64) {
	t.Helper()
	sch, _, _, _, err := e.buildSchematic(d, buildOpts{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range textplace.Collisions(sch) {
		total++
		area += c.Area
	}
	return total, area
}

// The point of the pass: a sheet the compiler could already measure as
// crowded comes out less crowded, without anyone editing the source.
func TestTidy_ClearsTextTheCompilerMeasured(t *testing.T) {
	e := tidyEnv(t)
	d := loadDesign(t, "contador_9_0.design.json")

	before, beforeArea := countCollisions(t, e, d)
	if before == 0 {
		t.Skip("this design no longer has collisions to clear")
	}

	sch, _, defects, drops, err := e.buildSchematic(d, buildOpts{})
	if err != nil {
		t.Fatal(err)
	}
	sch, _, defects, note := e.tidy(d, sch, "", defects, drops)

	after := 0
	afterArea := 0.0
	for _, c := range textplace.Collisions(sch) {
		after++
		afterArea += c.Area
	}
	t.Logf("collisions %d → %d, area %.1f → %.1f mm2; %s", before, after, beforeArea, afterArea, note)

	if len(defects) > 0 {
		t.Fatalf("tidy must never accept a placement whose netlist fails: %v", defects)
	}
	if after > before {
		t.Errorf("tidy left the sheet more crowded: %d → %d", before, after)
	}
	if after == before && note != "" {
		t.Errorf("tidy reported a change (%q) that improved nothing", note)
	}
}

// A design that is already tidy must come out untouched, and say nothing.
func TestTidy_LeavesACleanSheetAlone(t *testing.T) {
	e := tidyEnv(t)
	d := loadDesign(t, "led_18650.design.json")

	before, _ := countCollisions(t, e, d)
	if before != 0 {
		t.Skipf("this design has %d collisions; it is not the clean case", before)
	}

	sch, _, defects, drops, err := e.buildSchematic(d, buildOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(drops) != 0 {
		// A dropped documentation label IS something to fix now: tidy is
		// expected to spend spacing to keep the net's name.
		t.Skipf("this design drops %d doc label(s); it is not the clean case", len(drops))
	}
	_, _, _, note := e.tidy(d, sch, "", defects, drops)
	if note != "" {
		t.Errorf("nothing to fix, yet tidy changed the placement: %q", note)
	}
}

// The search must not mutate the caller's design. It explores by copying;
// a leak here would make a compile depend on how many candidates it tried.
func TestTidy_DoesNotMutateTheSourceDesign(t *testing.T) {
	e := tidyEnv(t)
	d := loadDesign(t, "contador_9_0.design.json")

	type cellsOf struct {
		ref   string
		cells int
	}
	var before []cellsOf
	for _, b := range d.Blocks {
		for _, s := range b.Symbols {
			if s.Place != nil {
				before = append(before, cellsOf{s.Ref, s.Place.Cells})
			}
		}
	}

	sch, _, defects, drops, err := e.buildSchematic(d, buildOpts{})
	if err != nil {
		t.Fatal(err)
	}
	e.tidy(d, sch, "", defects, drops)

	i := 0
	for _, b := range d.Blocks {
		for _, s := range b.Symbols {
			if s.Place == nil {
				continue
			}
			if s.Place.Cells != before[i].cells {
				t.Errorf("%s.place.cells changed under the caller: %d → %d",
					s.Ref, before[i].cells, s.Place.Cells)
			}
			i++
		}
	}
}

// Two runs of the same source must produce the same spacing decisions.
// Placement that wandered between runs is the class of bug this repository
// already paid for once.
func TestTidy_IsDeterministic(t *testing.T) {
	e := tidyEnv(t)
	d := loadDesign(t, "contador_9_0.design.json")

	run := func() string {
		sch, _, defects, drops, err := e.buildSchematic(d, buildOpts{})
		if err != nil {
			t.Fatal(err)
		}
		_, _, _, note := e.tidy(d, sch, "", defects, drops)
		return note
	}
	first, second := run(), run()
	if first != second {
		t.Errorf("two runs disagreed:\n  %q\n  %q", first, second)
	}
}

// Spacing is a floor the author sets: the pass may open a gap, never close one.
func TestBumpCellsOnlyGrows(t *testing.T) {
	d := &compile.Design{Blocks: []compile.Block{{
		Name: "b",
		Symbols: []compile.Symbol{
			{Ref: "U1", Lib: "Device:R"},
			{Ref: "R1", Lib: "Device:R", Place: &compile.Place{Pin: "1", At: "U1.1", Dir: "left", Cells: 3}},
		},
	}}}
	clone := cloneDesign(d)
	if !bumpCells(clone, "R1", 2) {
		t.Fatal("bumpCells did not find R1")
	}
	if got := clone.Blocks[0].Symbols[1].Place.Cells; got != 5 {
		t.Errorf("cells = %d, want 5", got)
	}
	if got := d.Blocks[0].Symbols[1].Place.Cells; got != 3 {
		t.Errorf("the original was modified: cells = %d, want 3", got)
	}
	if bumpCells(clone, "U1", 1) {
		t.Error("a symbol without place must not be bumpable")
	}
}

// The score's axes must rank in the documented order: a kept net name
// (docDropped) outranks corners and wire but never buys a collision, more
// overlap area, or a load-bearing label.
func TestBetterThanRanksDocDrops(t *testing.T) {
	base := sheetScore{labels: 2, collisions: 3, area: 5, docDropped: 2, bends: 10, wireLen: 100}

	fewerDrops := base
	fewerDrops.docDropped = 1
	fewerDrops.bends = 20 // pays in corners
	if !fewerDrops.betterThan(base) {
		t.Error("keeping a net name must be worth extra corners")
	}

	dropForCollision := base
	dropForCollision.docDropped = 1
	dropForCollision.collisions = 4
	if dropForCollision.betterThan(base) {
		t.Error("keeping a net name must never buy a new collision")
	}

	dropForLabel := base
	dropForLabel.docDropped = 0
	dropForLabel.labels = 3
	if dropForLabel.betterThan(base) {
		t.Error("keeping a net name must never cost a load-bearing label")
	}

	dropForArea := base
	dropForArea.docDropped = 0
	dropForArea.area = 6
	if dropForArea.betterThan(base) {
		t.Error("keeping a net name must never buy more overlap area")
	}
}

// A dropped documentation label leaves no collision on the finished sheet, so
// the collision loop can never propose the spacing that would have saved it.
// The drop record must generate those candidates instead.
func TestTidyCandidatesFromLabelDrops(t *testing.T) {
	d := &compile.Design{
		Blocks: []compile.Block{{
			Name: "b",
			Symbols: []compile.Symbol{
				{Ref: "U1", Lib: "Device:R"},
				{Ref: "R1", Lib: "Device:R", Place: &compile.Place{Pin: "1", At: "U1.1", Dir: "left", Cells: 3}},
			},
		}},
		Nets: map[string][]string{"SDA": {"U1.1", "R1.1"}},
	}
	sch, err := newEmptySchematic()
	if err != nil {
		t.Fatal(err)
	}
	drops := []labelDrop{{Net: "SDA", With: "no-connect U1.2", NeedCells: 1}}
	cands := tidyCandidates(sch, d, map[string]int{}, drops)
	found := false
	for _, c := range cands {
		if c.kind == "space" && c.ref == "R1" && c.extra == 1 {
			found = true
		}
	}
	if !found {
		t.Errorf("no space candidate for R1 from the drop record; got %+v", cands)
	}

	// A drop no spacing could have saved must generate nothing.
	none := tidyCandidates(sch, d, map[string]int{}, []labelDrop{{Net: "SDA", NeedCells: 0}})
	if len(none) != 0 {
		t.Errorf("unsavable drop produced candidates: %+v", none)
	}
}

func TestRefFromWith(t *testing.T) {
	cases := map[string]string{
		"body U1":         "U1",
		"text C3":         "C3",
		"pin C2.1":        "C2",
		"pin number C2.1": "C2",
		"wire VIN":        "",
		"label SDA":       "",
		"no-connect":      "",
	}
	for in, want := range cases {
		if got := refFromWith(in); got != want {
			t.Errorf("refFromWith(%q) = %q, want %q", in, got, want)
		}
	}
}

// How much the two search axes actually have to offer on the reference corpus.
// Recorded as a test rather than a note because it is the kind of claim that
// silently stops being true.
func TestTidy_CandidateSupplyAcrossTheCorpus(t *testing.T) {
	e := tidyEnv(t)
	for _, name := range []string{
		"contador_9_0.design.json", "greenhouse_controller.design.json",
		"psu_12v_dual.design.json", "demo_full_board.design.json",
		"hbridge_dc_motor.design.json", "opto_relay_driver.design.json",
	} {
		d := loadDesign(t, name)
		sch, _, _, _, err := e.buildSchematic(d, buildOpts{})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		space := len(tidyCandidates(sch, d, map[string]int{}, nil))
		order := netOrderCandidates(sch, d)
		names := make([]string, 0, len(order))
		for _, c := range order {
			names = append(names, c.ref)
		}
		t.Logf("%-34s spacing=%d  reorder=%d %v", name, space, len(order), names)
	}
}
