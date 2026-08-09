package kicadcli

import (
	"strings"
	"testing"
)

// A library missing from KiCad's table is a configuration gap, not a broken
// symbol. It used to be reported as an MCP bug because its ERC type contains
// "lib_symbol", which sent every import_part user chasing a phantom.
func TestClassify_UnregisteredLibraryIsNotAnMCPBug(t *testing.T) {
	got := classify(Violation{
		Type:        "lib_symbol_issue",
		Description: "The current configuration does not include the symbol library 'MCP_Imported'",
	})
	if got.Category != CategoryFixable {
		t.Errorf("category = %v, want CategoryFixable", got.Category)
	}
	if !strings.Contains(got.Hint, "register_library") {
		t.Errorf("hint should point at registration, got %q", got.Hint)
	}
}

// The rule above must not swallow the genuine malformed-symbol case it sits
// in front of.
func TestClassify_MalformedSymbolIsStillAnMCPBug(t *testing.T) {
	got := classify(Violation{
		Type:        "lib_symbol_mismatch",
		Description: "Symbol 'Device:R' has no parent for its extends reference",
	})
	if got.Category != CategoryMCPBug {
		t.Errorf("category = %v, want CategoryMCPBug", got.Category)
	}
}
