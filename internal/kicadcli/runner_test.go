package kicadcli

import (
	"strings"
	"testing"
)

func TestParseERCOutput(t *testing.T) {
	input := `{
  "violations": [
    {"description": "Pin unconnected"},
    {"description": "Wire not connected"}
  ]
}`
	got := ParseERCOutput(input)
	if len(got) != 2 {
		t.Fatalf("expected 2 violations, got %d: %v", len(got), got)
	}
	if got[0] != "Pin unconnected" {
		t.Errorf("expected %q, got %q", "Pin unconnected", got[0])
	}
	if got[1] != "Wire not connected" {
		t.Errorf("expected %q, got %q", "Wire not connected", got[1])
	}
}

func TestParseERCOutput_Empty(t *testing.T) {
	input := `{"violations": []}`
	got := ParseERCOutput(input)
	if len(got) != 0 {
		t.Fatalf("expected 0 violations, got %d: %v", len(got), got)
	}
}

func TestParseDRCOutput(t *testing.T) {
	input := `{
  "violations": [
    {"description": "Clearance violation"}
  ]
}`
	got := ParseDRCOutput(input)
	if len(got) != 1 {
		t.Fatalf("expected 1 violation, got %d", len(got))
	}
	if got[0] != "Clearance violation" {
		t.Errorf("expected %q, got %q", "Clearance violation", got[0])
	}
}

func TestExtractViolations_MalformedJSON(t *testing.T) {
	inputs := []string{
		`{not valid json`,
		``,
		`{"description": }`,
		strings.Repeat("x", 10000),
	}
	for _, in := range inputs {
		got := parseViolations(in)
		// Must not panic; result can be anything.
		_ = got
	}
}
