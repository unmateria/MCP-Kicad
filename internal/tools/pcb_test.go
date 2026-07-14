package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const minimalPCB = `(kicad_pcb (version 20221018) (generator "test"))`

// pcbWithFootprint has a footprint whose reference is set via (property "Reference" ...).
const pcbWithFootprint = `(kicad_pcb (version 20221018) (generator "test")
  (footprint "Device:R" (at 100 100 0) (property "Reference" "R1")))`

func TestHandleModifyPCBLayout_MoveFootprint(t *testing.T) {
	tmp := t.TempDir()
	pcbPath := filepath.Join(tmp, "test.kicad_pcb")
	if err := os.WriteFile(pcbPath, []byte(pcbWithFootprint), 0o644); err != nil {
		t.Fatal(err)
	}

	env := &Env{}
	res, _, err := env.handleModifyPCBLayout(context.Background(), nil, modifyPCBLayoutInput{
		PCBPath:   pcbPath,
		Action:    "move_footprint",
		Reference: "R1",
		X:         50,
		Y:         60,
		Angle:     90,
	})
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "updated") {
		t.Errorf("expected 'updated' in response, got: %q", text)
	}

	data, _ := os.ReadFile(pcbPath)
	content := string(data)
	if !strings.Contains(content, "50") || !strings.Contains(content, "60") {
		t.Errorf("expected updated coordinates in file:\n%s", content)
	}
}

func TestHandleModifyPCBLayout_DefineEdgeCuts(t *testing.T) {
	tmp := t.TempDir()
	pcbPath := filepath.Join(tmp, "test.kicad_pcb")
	if err := os.WriteFile(pcbPath, []byte(minimalPCB), 0o644); err != nil {
		t.Fatal(err)
	}

	env := &Env{}
	res, _, err := env.handleModifyPCBLayout(context.Background(), nil, modifyPCBLayoutInput{
		PCBPath:  pcbPath,
		Action:   "define_edge_cuts",
		X:        0,
		Y:        0,
		Width:    100,
		Height:   80,
	})
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "updated") {
		t.Errorf("expected 'updated' in response, got: %q", text)
	}

	data, _ := os.ReadFile(pcbPath)
	content := string(data)
	count := strings.Count(content, "gr_line")
	if count != 4 {
		t.Errorf("expected 4 gr_line nodes, got %d:\n%s", count, content)
	}
}

func TestHandleModifyPCBLayout_InvalidEdgeCuts(t *testing.T) {
	tmp := t.TempDir()
	pcbPath := filepath.Join(tmp, "test.kicad_pcb")
	if err := os.WriteFile(pcbPath, []byte(minimalPCB), 0o644); err != nil {
		t.Fatal(err)
	}

	env := &Env{}
	res, _, err := env.handleModifyPCBLayout(context.Background(), nil, modifyPCBLayoutInput{
		PCBPath: pcbPath,
		Action:  "define_edge_cuts",
		Width:   0, // invalid
		Height:  80,
	})
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "error") {
		t.Errorf("expected error for width=0, got: %q", text)
	}
}

func TestHandleModifyPCBLayout_InvalidAction(t *testing.T) {
	tmp := t.TempDir()
	pcbPath := filepath.Join(tmp, "test.kicad_pcb")
	if err := os.WriteFile(pcbPath, []byte(minimalPCB), 0o644); err != nil {
		t.Fatal(err)
	}

	env := &Env{}
	res, _, err := env.handleModifyPCBLayout(context.Background(), nil, modifyPCBLayoutInput{
		PCBPath: pcbPath,
		Action:  "explode",
	})
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "error") {
		t.Errorf("expected error for unknown action, got: %q", text)
	}
}
