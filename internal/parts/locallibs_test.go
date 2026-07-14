package parts

import (
	"os"
	"path/filepath"
	"testing"
)

const minimalSymLib = `(kicad_symbol_lib (version 20220914)
  (symbol "R" (pin passive line (at 0 0 270))))`

func TestLocalSearch_InvalidQuery(t *testing.T) {
	tmp := t.TempDir()
	_, err := LocalSearch(tmp, "NoColon")
	if err == nil {
		t.Fatal("expected error for query without ':'")
	}
}

func TestLocalSearch_NotFound(t *testing.T) {
	tmp := t.TempDir()
	_, err := LocalSearch(tmp, "Device:R")
	if err == nil {
		t.Fatal("expected error when library does not exist")
	}
}

func TestLocalSearch_FoundInKicadOfficial(t *testing.T) {
	tmp := t.TempDir()
	symDir := filepath.Join(tmp, "kicad-official", "kicad-symbols")
	if err := os.MkdirAll(symDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(symDir, "Device.kicad_sym"), []byte(minimalSymLib), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := LocalSearch(tmp, "Device:R")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.PartName != "R" {
		t.Errorf("expected PartName %q, got %q", "R", result.PartName)
	}
	if result.Source != "local-official" {
		t.Errorf("expected source %q, got %q", "local-official", result.Source)
	}
}

func TestLocalSearch_FoundInAlternate(t *testing.T) {
	tmp := t.TempDir()
	altDir := filepath.Join(tmp, "alternate")
	if err := os.MkdirAll(altDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(altDir, "Device.kicad_sym"), []byte(minimalSymLib), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := LocalSearch(tmp, "Device:R")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.PartName != "R" {
		t.Errorf("expected PartName %q, got %q", "R", result.PartName)
	}
	if result.Source != "local-alternate" {
		t.Errorf("expected source %q, got %q", "local-alternate", result.Source)
	}
}

func TestFootprintSearch_Found(t *testing.T) {
	tmp := t.TempDir()
	fpDir := filepath.Join(tmp, "kicad-official", "kicad-footprints", "Resistor_SMD.pretty")
	if err := os.MkdirAll(fpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fpDir, "R_0402.kicad_mod"), []byte("(module R_0402)"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := FootprintSearch(tmp, "Resistor_SMD:R_0402")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.PartName != "R_0402" {
		t.Errorf("expected PartName %q, got %q", "R_0402", result.PartName)
	}
}

func TestFootprintSearch_NotFound(t *testing.T) {
	tmp := t.TempDir()
	_, err := FootprintSearch(tmp, "Resistor_SMD:R_0402")
	if err == nil {
		t.Fatal("expected error when footprint does not exist")
	}
}
