package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const minimalSchematic = `(kicad_sch (version 20231120) (generator "test"))`

// mockDeviceLib is a minimal Device.kicad_sym sufficient for test symbol embedding.
const mockDeviceLib = `(kicad_symbol_lib (version 20231120) (generator "kicad_symbol_editor")
  (symbol "R"
    (pin_numbers (hide yes))
    (pin_names (offset 0))
    (exclude_from_sim no)
    (in_bom yes)
    (on_board yes)
    (symbol "R_0_1"
      (polyline (pts (xy -1.016 0) (xy 1.016 0)) (stroke (width 0.254) (type default)) (fill (type none)))
    )
    (symbol "R_1_1"
      (pin passive line (at 0 1.016 270) (length 0)
        (name "~" (effects (font (size 1.27 1.27))))
        (number "1" (effects (font (size 1.27 1.27))))
      )
      (pin passive line (at 0 -1.016 90) (length 0)
        (name "~" (effects (font (size 1.27 1.27))))
        (number "2" (effects (font (size 1.27 1.27))))
      )
    )
  )
)`

// mockEnv creates a test Env with a temporary symbol library directory.
func mockEnv(t *testing.T) *Env {
	t.Helper()
	symDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(symDir, "Device.kicad_sym"), []byte(mockDeviceLib), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Env{KicadSymbols: symDir}
}

func TestHandleModifySchematic_AddSymbol(t *testing.T) {
	tmp := t.TempDir()
	schPath := filepath.Join(tmp, "test.kicad_sch")
	if err := os.WriteFile(schPath, []byte(minimalSchematic), 0o644); err != nil {
		t.Fatal(err)
	}

	env := mockEnv(t)
	res, _, err := env.handleModifySchematic(context.Background(), nil, modifySchematicInput{
		SchematicPath: schPath,
		Action:        "add_symbol",
		LibID:         "Device:R",
		Reference:     "R1",
		MountType:     "THT",
		X:             10,
		Y:             20,
	})
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "added") {
		t.Errorf("expected 'added' in response, got: %q", text)
	}

	data, _ := os.ReadFile(schPath)
	if !strings.Contains(string(data), "Device:R") {
		t.Errorf("expected symbol lib_id in file, got:\n%s", data)
	}
}

func TestHandleModifySchematic_AddWire(t *testing.T) {
	tmp := t.TempDir()
	schPath := filepath.Join(tmp, "test.kicad_sch")
	if err := os.WriteFile(schPath, []byte(minimalSchematic), 0o644); err != nil {
		t.Fatal(err)
	}

	env := &Env{}
	res, _, err := env.handleModifySchematic(context.Background(), nil, modifySchematicInput{
		SchematicPath: schPath,
		Action:        "add_wire",
		X:             0,
		Y:             0,
		X2:            10,
		Y2:            0,
	})
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "wire") {
		t.Errorf("expected 'wire' in response, got: %q", text)
	}

	data, _ := os.ReadFile(schPath)
	if !strings.Contains(string(data), "wire") {
		t.Errorf("expected wire node in file, got:\n%s", data)
	}
}

func TestHandleModifySchematic_InvalidAction(t *testing.T) {
	tmp := t.TempDir()
	schPath := filepath.Join(tmp, "test.kicad_sch")
	if err := os.WriteFile(schPath, []byte(minimalSchematic), 0o644); err != nil {
		t.Fatal(err)
	}

	env := &Env{}
	res, _, err := env.handleModifySchematic(context.Background(), nil, modifySchematicInput{
		SchematicPath: schPath,
		Action:        "delete_everything",
	})
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "unknown action") {
		t.Errorf("expected 'unknown action' in response, got: %q", text)
	}
}

func TestHandleModifySchematic_MissingLibID(t *testing.T) {
	tmp := t.TempDir()
	schPath := filepath.Join(tmp, "test.kicad_sch")
	if err := os.WriteFile(schPath, []byte(minimalSchematic), 0o644); err != nil {
		t.Fatal(err)
	}

	env := &Env{}
	res, _, err := env.handleModifySchematic(context.Background(), nil, modifySchematicInput{
		SchematicPath: schPath,
		Action:        "add_symbol",
		// LibID intentionally omitted
		Reference: "R1",
	})
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "error") {
		t.Errorf("expected error for missing lib_id, got: %q", text)
	}
}

// TestAddSymbol_TildePinsSuggestNumbers verifies that when both pins are named "~"
// (e.g. resistors and capacitors), the from= suggestion uses pin numbers, not "~".
func TestAddSymbol_TildePinsSuggestNumbers(t *testing.T) {
	tmp := t.TempDir()
	schPath := filepath.Join(tmp, "test.kicad_sch")
	schContent := `(kicad_sch (version 20231120) (generator "test") (uuid "test-uuid") (lib_symbols) (sheet_instances (path "/test-uuid" (page "1"))))`
	if err := os.WriteFile(schPath, []byte(schContent), 0o644); err != nil {
		t.Fatal(err)
	}

	env := mockEnv(t)
	res, _, err := env.handleModifySchematic(context.Background(), nil, modifySchematicInput{
		SchematicPath: schPath,
		Action:        "add_symbol",
		LibID:         "Device:R",
		Reference:     "R1",
		MountType:     "THT",
		X:             50,
		Y:             50,
	})
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, res)
	// Both pins of Device:R are named "~"; from= suggestions must use numbers.
	if strings.Contains(text, "from=R1.~") {
		t.Errorf("pin suggestion must not use '~' name when pins are unnamed; got:\n%s", text)
	}
	if !strings.Contains(text, "from=R1.1") || !strings.Contains(text, "from=R1.2") {
		t.Errorf("expected from=R1.1 and from=R1.2 in response; got:\n%s", text)
	}
}
