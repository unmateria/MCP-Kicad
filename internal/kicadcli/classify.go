package kicadcli

import "strings"

// Violation is an ERC/DRC violation with type, description, and severity.
type Violation struct {
	Type        string // machine-readable from JSON "type" field
	Description string // human-readable from JSON "description" field
	Severity    string // "error" or "warning"
}

// ViolationCategory classifies who is responsible for the issue.
type ViolationCategory int

const (
	// CategoryFixable: the LLM can resolve this (unconnected pin, missing ref…).
	CategoryFixable ViolationCategory = iota
	// CategoryMCPBug: the MCP produced invalid output (broken symbol def, etc.).
	CategoryMCPBug
	// CategoryInfo: informational, does not block the design.
	CategoryInfo
)

// ClassifiedViolation pairs a Violation with its category and a short hint.
type ClassifiedViolation struct {
	Violation
	Category ViolationCategory
	Hint     string
}

// prefix returns the tag string for the category.
func (c ViolationCategory) prefix() string {
	switch c {
	case CategoryMCPBug:
		return "[MCP BUG]"
	case CategoryFixable:
		return "[FIXABLE]"
	default:
		return "[INFO]"
	}
}

// ClassifyViolations categorizes violations and attaches hints.
// stderrOutput is the raw stderr from kicad-cli (may contain structural errors).
func ClassifyViolations(violations []Violation, stderrOutput string) []ClassifiedViolation {
	var result []ClassifiedViolation

	// Stderr errors mean kicad-cli couldn't process the file at all — always MCP bug.
	if msg := strings.TrimSpace(stderrOutput); msg != "" {
		result = append(result, ClassifiedViolation{
			Violation: Violation{Type: "cli_error", Description: msg, Severity: "error"},
			Category:  CategoryMCPBug,
			Hint:      "kicad-cli rejected the file — likely a malformed symbol definition produced by the MCP",
		})
	}

	for _, v := range violations {
		result = append(result, classify(v))
	}
	return result
}

// FormatViolations returns a compact multi-line string for LLM consumption.
func FormatViolations(violations []ClassifiedViolation) string {
	if len(violations) == 0 {
		return "ERC: OK"
	}
	var sb strings.Builder
	bugs, fixable, info := 0, 0, 0
	for _, v := range violations {
		switch v.Category {
		case CategoryMCPBug:
			bugs++
		case CategoryFixable:
			fixable++
		default:
			info++
		}
	}
	sb.WriteString("ERC violations:\n")
	for _, v := range violations {
		hint := ""
		if v.Hint != "" {
			hint = " → " + v.Hint
		}
		sb.WriteString("  " + v.Category.prefix() + " " + v.Description + hint + "\n")
	}
	if bugs > 0 {
		sb.WriteString("⚠ MCP bugs detected — please report the above [MCP BUG] entries with the schematic file\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

func classify(v Violation) ClassifiedViolation {
	t := strings.ToLower(v.Type)
	d := strings.ToLower(v.Description)

	// A library the schematic references but KiCad's tables do not list. Its
	// ERC type contains "lib_symbol", so without this rule it lands in the
	// malformed-definition bucket below and gets reported as an MCP bug —
	// which it is not: the symbol is fine, KiCad simply has not been told
	// where the library lives.
	if containsAny(d, "does not include the symbol library", "is not enabled in the current configuration",
		"library is not enabled", "symbol library is not") {
		return ClassifiedViolation{v, CategoryFixable,
			"KiCad's symbol library table does not list this library. import_part registers what it " +
				"installs; for a library you added by hand, use register_library against KiCad's " +
				"sym-lib-table. The schematic itself is fine — it embeds the symbol — so this only " +
				"affects opening it in the GUI"}
	}

	// --- MCP bugs: malformed symbol definitions ---
	if containsAny(d, "no parent", "extends", "unknown pin type",
		"expecting input", "malformed", "invalid symbol") {
		return ClassifiedViolation{v, CategoryMCPBug,
			"Symbol definition error in lib_symbols — MCP failed to embed the symbol correctly"}
	}
	if containsAny(t, "lib_pin", "lib_symbol", "duplicate_pin") {
		return ClassifiedViolation{v, CategoryMCPBug,
			"Duplicate or invalid pin in embedded symbol definition — MCP bug"}
	}

	// --- Fixable: LLM can correct these ---
	if containsAny(t, "pin_not_connected", "wire_dangling", "no_connect") ||
		containsAny(d, "pin unconnected", "wire not connected", "not connected") {
		return ClassifiedViolation{v, CategoryFixable,
			"List this pin in the source's `nets`, or in `no_connect` if it is meant to be unused, then recompile"}
	}
	if containsAny(t, "missing_power_flag", "power_pin_not_driven") ||
		containsAny(d, "power pin", "power flag") {
		// Do NOT tell people to add the rail to power_nets. That switches the
		// net to the per-pin power policy — a power symbol on every pin and no
		// routing at all — which shreds a net like +VRAW that genuinely needs
		// wires between a bridge, a capacitor and a regulator. A session
		// followed that advice and got a SPLIT netlist for its trouble.
		return ClassifiedViolation{v, CategoryFixable,
			"KiCad wants a driver on this rail. compile_schematic adds a PWR_FLAG automatically; " +
				"if this one is fed by a rectifier or a connector rather than a regulator output, " +
				"the warning is expected and harmless. Do NOT add the rail to power_nets to silence it — " +
				"that switches the whole net to one-symbol-per-pin and stops it being wired at all"}
	}
	if containsAny(t, "duplicate_reference", "no_reference", "empty_reference") ||
		containsAny(d, "duplicate reference", "missing reference", "no reference") {
		return ClassifiedViolation{v, CategoryFixable,
			"Fix the reference designator for the affected symbol"}
	}
	if containsAny(t, "missing_value") || containsAny(d, "missing value") {
		return ClassifiedViolation{v, CategoryFixable,
			"Set a value for the component (e.g. 100 for a resistor)"}
	}
	if containsAny(t, "bus_", "hier_") {
		return ClassifiedViolation{v, CategoryFixable,
			"Check bus or hierarchical connections"}
	}

	// --- Default: informational ---
	return ClassifiedViolation{v, CategoryInfo, ""}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
