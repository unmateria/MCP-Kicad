// Package parts provides component search and retrieval across all library sources.
package parts

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ComponentResult holds a found component location.
type ComponentResult struct {
	// Source is "local-official", "local-alternate", or "snapeda".
	Source string
	// Path is the absolute path to the file (symbol or footprint).
	Path string
	// LibName is the library name (e.g. "Device").
	LibName string
	// PartName is the part name within the library (e.g. "R").
	PartName string
}

// LocalSearch searches for a component in the locally cloned KiCad libraries.
//
// libsRoot should be the path to the libs/ directory (e.g. "libs/kicad-official/kicad-symbols").
// query is in the form "LibName:PartName" (e.g. "Device:R") for symbols,
// or just a footprint name for footprints.
func LocalSearch(libsRoot, query string) (*ComponentResult, error) {
	parts := strings.SplitN(query, ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("locallibs: query must be in LibName:PartName format, got %q", query)
	}
	libName, partName := parts[0], parts[1]

	// Search in kicad-symbols (.kicad_sym files).
	symPath := filepath.Join(libsRoot, "kicad-official", "kicad-symbols", libName+".kicad_sym")
	if fileExists(symPath) {
		if containsPart(symPath, partName) {
			return &ComponentResult{
				Source:   "local-official",
				Path:     symPath,
				LibName:  libName,
				PartName: partName,
			}, nil
		}
	}

	// Search in alternate library if present.
	altPath := filepath.Join(libsRoot, "alternate", libName+".kicad_sym")
	if fileExists(altPath) {
		if containsPart(altPath, partName) {
			return &ComponentResult{
				Source:   "local-alternate",
				Path:     altPath,
				LibName:  libName,
				PartName: partName,
			}, nil
		}
	}

	return nil, fmt.Errorf("locallibs: component %q not found in local libraries", query)
}

// FootprintSearch searches for a footprint file in local libraries.
//
// query format: "LibName:FootprintName" (e.g. "Resistor_SMD:R_0402").
func FootprintSearch(libsRoot, query string) (*ComponentResult, error) {
	parts := strings.SplitN(query, ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("locallibs: query must be LibName:FootprintName, got %q", query)
	}
	libName, fpName := parts[0], parts[1]

	fpPath := filepath.Join(libsRoot, "kicad-official", "kicad-footprints", libName+".pretty", fpName+".kicad_mod")
	if fileExists(fpPath) {
		return &ComponentResult{
			Source:   "local-official",
			Path:     fpPath,
			LibName:  libName,
			PartName: fpName,
		}, nil
	}

	return nil, fmt.Errorf("locallibs: footprint %q not found in local libraries", query)
}

// GlobalSearch searches for a component in the KiCad global symbol library directory.
// globalSymbolsDir is the path to the symbols folder of the KiCad installation,
// e.g. "C:/Program Files/KiCad/10.0/share/kicad/symbols".
func GlobalSearch(globalSymbolsDir, query string) (*ComponentResult, error) {
	parts := strings.SplitN(query, ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("locallibs: query must be in LibName:PartName format, got %q", query)
	}
	libName, partName := parts[0], parts[1]
	symPath := filepath.Join(globalSymbolsDir, libName+".kicad_sym")
	if fileExists(symPath) && containsPart(symPath, partName) {
		return &ComponentResult{
			Source:   "kicad-global",
			Path:     symPath,
			LibName:  libName,
			PartName: partName,
		}, nil
	}
	return nil, fmt.Errorf("locallibs: component %q not found in KiCad global library", query)
}

// ListLibraries returns the names of all .kicad_sym files in globalSymbolsDir,
// optionally filtered by a substring match on the library name.
func ListLibraries(globalSymbolsDir, filter string) ([]string, error) {
	entries, err := os.ReadDir(globalSymbolsDir)
	if err != nil {
		return nil, fmt.Errorf("locallibs: cannot read %s: %w", globalSymbolsDir, err)
	}
	filter = strings.ToLower(filter)
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".kicad_sym") {
			continue
		}
		libName := strings.TrimSuffix(e.Name(), ".kicad_sym")
		if filter == "" || strings.Contains(strings.ToLower(libName), filter) {
			names = append(names, libName)
		}
	}
	return names, nil
}

// SearchSymbols returns all symbol names inside a .kicad_sym file that contain
// the given substring (case-insensitive). libName is e.g. "Device".
// Returns results as "LibName:PartName" strings.
func SearchSymbols(globalSymbolsDir, libName, partFilter string) ([]string, error) {
	symPath := filepath.Join(globalSymbolsDir, libName+".kicad_sym")
	data, err := os.ReadFile(symPath)
	if err != nil {
		return nil, fmt.Errorf("locallibs: cannot read %s: %w", symPath, err)
	}
	filter := strings.ToLower(partFilter)
	var results []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, `(symbol "`) {
			continue
		}
		// Extract name between first pair of quotes after "(symbol "
		rest := line[len(`(symbol "`):]
		end := strings.Index(rest, `"`)
		if end < 0 {
			continue
		}
		name := rest[:end]
		// Skip sub-units (contain underscore followed by digits, e.g. "R_0_1")
		if isSubUnit(name) {
			continue
		}
		if filter == "" || strings.Contains(strings.ToLower(name), filter) {
			results = append(results, libName+":"+name)
		}
	}
	return results, nil
}

// FuzzySearchGlobal scans all .kicad_sym files in globalSymbolsDir for symbols
// matching partFilter (case-insensitive), looking at the symbol NAME, its
// ki_keywords and its Description. Returns up to maxResults entries as
// "LibName:PartName" — with the description appended when the match came from
// text rather than the name, since that is what tells you which of a dozen
// candidates you want.
//
// Searching only names made whole categories invisible: a session looking for
// a Schottky diode got nothing back and had to list all 300+ symbols in the
// Diode library and pick one from memory. The word "Schottky" appears in the
// keywords of the right parts and nowhere in their names.
func FuzzySearchGlobal(globalSymbolsDir, partFilter string, maxResults int) []string {
	entries, err := os.ReadDir(globalSymbolsDir)
	if err != nil {
		return nil
	}
	filter := strings.ToLower(partFilter)
	seen := map[string]bool{}

	// Name matches first, then text matches, and at most a couple per library.
	// Without the cap a keyword search drowns: "Schottky" appears in every
	// 74LS part (Low-power Schottky is the logic family) and eight variants of
	// one buffer pushed the actual Schottky diodes off the list.
	const perLib = 2
	var byName, byText []string
	countByLib := map[string]int{}

	add := func(lib, id, desc string, nameMatch bool) {
		if seen[id] || countByLib[lib] >= perLib {
			return
		}
		seen[id] = true
		countByLib[lib]++
		if desc != "" {
			id += "  — " + desc
		}
		if nameMatch {
			byName = append(byName, id)
			return
		}
		byText = append(byText, id)
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".kicad_sym") {
			continue
		}
		libName := strings.TrimSuffix(e.Name(), ".kicad_sym")
		data, err := os.ReadFile(filepath.Join(globalSymbolsDir, e.Name()))
		if err != nil {
			continue
		}

		current, currentDesc := "", ""
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)

			if strings.HasPrefix(line, `(symbol "`) {
				name := quotedAfter(line, `(symbol "`)
				if name == "" || isSubUnit(name) {
					continue
				}
				current, currentDesc = name, ""
				if strings.Contains(strings.ToLower(name), filter) {
					add(libName, libName+":"+name, "", true)
				}
				continue
			}
			if current == "" {
				continue
			}
			// Description is worth remembering even when the keywords match,
			// so the caller sees what the part actually is.
			if strings.HasPrefix(line, `(property "Description"`) {
				currentDesc = propertyValue(line)
			}
			if strings.HasPrefix(line, `(property "ki_keywords"`) || strings.HasPrefix(line, `(property "Description"`) {
				val := propertyValue(line)
				if val != "" && strings.Contains(strings.ToLower(val), filter) {
					desc := currentDesc
					if desc == "" {
						desc = val
					}
					add(libName, libName+":"+current, desc, false)
				}
			}
		}
	}

	results := append(byName, byText...)
	if len(results) > maxResults {
		results = results[:maxResults]
	}
	return results
}

// isSubUnit returns true if the name matches the KiCad sub-unit pattern Name_N_M.
func isSubUnit(name string) bool {
	// Sub-units end in _<digit>_<digit>
	parts := strings.Split(name, "_")
	if len(parts) < 3 {
		return false
	}
	last := parts[len(parts)-1]
	secondLast := parts[len(parts)-2]
	return isDigits(last) && isDigits(secondLast)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// fileExists reports whether path points to an existing regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// containsPart does a fast check whether a .kicad_sym file contains a symbol
// with the given name. Uses simple string search to avoid full parse overhead
// during discovery.
func containsPart(symFilePath, partName string) bool {
	data, err := os.ReadFile(symFilePath)
	if err != nil {
		return false
	}
	// KiCad symbol files have entries like: (symbol "PartName" ...)
	needle := fmt.Sprintf(`(symbol "%s"`, partName)
	return strings.Contains(string(data), needle)
}

// quotedAfter returns the first quoted string following prefix in line.
func quotedAfter(line, prefix string) string {
	if !strings.HasPrefix(line, prefix) {
		return ""
	}
	rest := line[len(prefix):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// propertyValue returns the VALUE of a `(property "Name" "Value" …)` line.
func propertyValue(line string) string {
	first := strings.Index(line, `"`)
	if first < 0 {
		return ""
	}
	rest := line[first+1:]
	closing := strings.Index(rest, `"`)
	if closing < 0 {
		return ""
	}
	rest = rest[closing+1:]
	open := strings.Index(rest, `"`)
	if open < 0 {
		return ""
	}
	rest = rest[open+1:]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return ""
	}
	return rest[:end]
}
