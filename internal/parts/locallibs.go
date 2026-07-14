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
// whose name contains partFilter (case-insensitive). Returns up to maxResults
// results as "LibName:PartName" strings, sorted by library name.
func FuzzySearchGlobal(globalSymbolsDir, partFilter string, maxResults int) []string {
	entries, err := os.ReadDir(globalSymbolsDir)
	if err != nil {
		return nil
	}
	filter := strings.ToLower(partFilter)
	var results []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".kicad_sym") {
			continue
		}
		libName := strings.TrimSuffix(e.Name(), ".kicad_sym")
		symPath := filepath.Join(globalSymbolsDir, e.Name())
		data, err := os.ReadFile(symPath)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, `(symbol "`) {
				continue
			}
			rest := line[len(`(symbol "`):]
			end := strings.Index(rest, `"`)
			if end < 0 {
				continue
			}
			name := rest[:end]
			if isSubUnit(name) {
				continue
			}
			if strings.Contains(strings.ToLower(name), filter) {
				results = append(results, libName+":"+name)
				if len(results) >= maxResults {
					return results
				}
			}
		}
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
