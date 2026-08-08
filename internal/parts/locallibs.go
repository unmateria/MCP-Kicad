// Package parts provides component search and retrieval across all library sources.
package parts

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

// FuzzySearchGlobal finds symbols matching partFilter across every .kicad_sym
// in globalSymbolsDir, ranked by relevance. It reads the symbol NAME, its
// Description and its ki_keywords, and returns "LibName:PartName — description"
// so the caller can tell a dozen candidates apart.
//
// Ranking is the whole point, and it was learned from two sessions in a row.
// Scanning libraries alphabetically and taking the first hits meant searching
// "LED" returned Connector:8P8C_LED while Device:LED — whose name IS the query
// — never appeared, and "resistor" returned three current-sense amplifiers
// whose descriptions mention the word, but not Device:R, described simply as
// "Resistor". The information was always there; the order buried it.
//
// Multi-word queries score per term rather than requiring the literal phrase.
// "electrolytic capacitor" appears verbatim in no KiCad symbol, but scoring
// "capacitor" alone still surfaces Device:C_Polarized, which is what the
// author wanted.
func FuzzySearchGlobal(globalSymbolsDir, partFilter string, maxResults int) []string {
	entries, err := os.ReadDir(globalSymbolsDir)
	if err != nil {
		return nil
	}
	query := strings.ToLower(strings.TrimSpace(partFilter))
	if query == "" {
		return nil
	}
	rawTerms := strings.Fields(query)
	terms := expandJargon(rawTerms)
	// A one-word query names the part; a multi-word query DESCRIBES it. Only
	// the first kind may hand out the exact-name jackpot: "PTC fuse" matching
	// a symbol simply called "Fuse" on one of its two words should not beat
	// Polyfuse, which answers both.
	singleTerm := len(rawTerms) == 1

	type hit struct {
		id    string
		desc  string
		lib   string
		score int
		nlen  int
	}
	var hits []hit

	consider := func(lib, name, desc, keywords string) {
		lname := strings.ToLower(name)
		ldesc := strings.ToLower(desc)
		lkeys := strings.ToLower(keywords)

		// "The part IS what was asked for" has to consider the TRANSLATED
		// terms too, not just the literal query: searching "xtal" should land
		// on Device:Crystal, and it did not, because the exact-name bonus was
		// comparing against "xtal" while the library says "Crystal".
		exactName := lname == query
		if singleTerm {
			for _, t := range terms {
				if lname == t {
					exactName = true
				}
			}
		}

		score := 0
		switch {
		case exactName:
			score += 1000
		case ldesc == query:
			score += 800
		case strings.HasPrefix(lname, query):
			score += 400
		}
		for _, t := range terms {
			if strings.Contains(lname, t) {
				score += 100
			}
			if strings.Contains(ldesc, t) {
				score += 60
			}
			if strings.Contains(lkeys, t) {
				score += 40
			}
		}
		if score == 0 {
			return
		}
		hits = append(hits, hit{id: lib + ":" + name, desc: desc, lib: lib, score: score, nlen: len(name)})
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

		name, desc, keywords := "", "", ""
		flush := func() {
			if name != "" {
				consider(libName, name, desc, keywords)
			}
			name, desc, keywords = "", "", ""
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, `(symbol "`) {
				n := quotedAfter(line, `(symbol "`)
				if n == "" || isSubUnit(n) {
					continue // sub-unit: keep accumulating for the parent
				}
				flush()
				name = n
				continue
			}
			if name == "" {
				continue
			}
			if strings.HasPrefix(line, `(property "Description"`) {
				desc = propertyValue(line)
			}
			if strings.HasPrefix(line, `(property "ki_keywords"`) {
				keywords = propertyValue(line)
			}
		}
		flush()
	}

	// Best score first; then the shorter name, because the generic part of a
	// family (Device:R) is nearly always what a plain-word search wanted;
	// then alphabetically so the list is stable run to run.
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		if hits[i].nlen != hits[j].nlen {
			return hits[i].nlen < hits[j].nlen
		}
		return hits[i].id < hits[j].id
	})

	// Cap per library so one family cannot fill the answer: "Schottky" matches
	// every 74LS part, and eight variants of one buffer had crowded out the
	// actual Schottky diodes. Three rather than two, because a generic library
	// legitimately holds several variants worth seeing — at two, "electrolytic
	// capacitor" returned C and C_US and hid C_Polarized, the one wanted.
	const perLib = 3
	countByLib := map[string]int{}
	var out []string
	for _, h := range hits {
		if countByLib[h.lib] >= perLib {
			continue
		}
		countByLib[h.lib]++
		entry := h.id
		if h.desc != "" {
			entry += "  — " + h.desc
		}
		out = append(out, entry)
		if len(out) >= maxResults {
			break
		}
	}
	return out
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

// jargon maps words engineers use to the words KiCad's libraries actually
// contain. Every entry comes from a search that returned nothing useful in a
// real session, not from imagination — KiCad has no symbol whose text says
// "electrolytic", it says "Polarized capacitor", and someone asking for one is
// not going to guess that.
//
// Deliberately tiny. A big synonym table would be a second vocabulary to
// maintain and would start guessing wrong; this only covers words where the
// trade name and the library name genuinely differ.
var jargon = map[string]string{
	"electrolytic": "polarized",
	"xtal":         "crystal",
	"opto":         "optocoupler",
	"optoisolator": "optocoupler",
	"pot":          "potentiometer",
	"ldo":          "dropout",
}

// expandJargon adds the library's own word alongside each trade term, keeping
// both so a query that already used KiCad's vocabulary is unaffected.
func expandJargon(terms []string) []string {
	out := terms
	for _, t := range terms {
		if alt, ok := jargon[t]; ok {
			out = append(out, alt)
		}
	}
	return out
}
