package parts

import (
	"fmt"
	"os"
	"path/filepath"
)

// The layout of libsRoot. Everything the importer writes lands here, and
// nothing else does: the tree is disposable and reproducible from the
// MCP_Source property each imported symbol carries.
const (
	SymbolsDir     = "symbols"    // .kicad_sym files
	FootprintsDir  = "footprints" // .pretty directories
	Models3DDir    = "3dmodels"   // .step / .wrl files
	CacheDir       = "cache"      // provider indexes, per provider subdir
	LegacyDownload = "downloaded" // pre-importer dumping ground, still searched

	// ImportedLib is the single library every imported part goes into.
	// One library means one entry in KiCad's tables, registered once.
	ImportedLib = "MCP_Imported"
)

// SymbolsPath returns the directory holding imported .kicad_sym libraries.
func SymbolsPath(libsRoot string) string { return filepath.Join(libsRoot, SymbolsDir) }

// FootprintsPath returns the directory holding imported .pretty libraries.
func FootprintsPath(libsRoot string) string { return filepath.Join(libsRoot, FootprintsDir) }

// Models3DPath returns the directory holding imported 3D models.
func Models3DPath(libsRoot string) string { return filepath.Join(libsRoot, Models3DDir) }

// CachePath returns the cache directory for one provider's index.
func CachePath(libsRoot, provider string) string {
	return filepath.Join(libsRoot, CacheDir, provider)
}

// ImportedSymbolLib is the path of the single library imported symbols go into.
func ImportedSymbolLib(libsRoot string) string {
	return filepath.Join(SymbolsPath(libsRoot), ImportedLib+".kicad_sym")
}

// ImportedFootprintLib is the path of the .pretty directory imported
// footprints go into.
func ImportedFootprintLib(libsRoot string) string {
	return filepath.Join(FootprintsPath(libsRoot), ImportedLib+".pretty")
}

// EnsureTree creates the libsRoot layout. It is called before any write
// because libs/ is not versioned: a fresh clone has no such directory.
func EnsureTree(libsRoot string) error {
	if libsRoot == "" {
		return fmt.Errorf("parts: libs root is not configured")
	}
	for _, d := range []string{SymbolsDir, FootprintsDir, Models3DDir, CacheDir} {
		if err := os.MkdirAll(filepath.Join(libsRoot, d), 0o755); err != nil {
			return fmt.Errorf("parts: cannot create %s: %w", d, err)
		}
	}
	return nil
}

// SymbolSearchPath returns the directories to look in for "<LibName>.kicad_sym",
// in priority order: imported libraries first, then anything cloned under
// libsRoot, then the KiCad installation.
//
// This is the ordering that makes an imported part usable everywhere at once —
// every consumer of a library symbol resolves through it.
func SymbolSearchPath(libsRoot, globalSymbolsDir string) []string {
	var dirs []string
	if libsRoot != "" {
		dirs = append(dirs,
			SymbolsPath(libsRoot),
			filepath.Join(libsRoot, "kicad-official", "kicad-symbols"),
			filepath.Join(libsRoot, "alternate"),
			filepath.Join(libsRoot, LegacyDownload),
		)
	}
	if globalSymbolsDir != "" {
		dirs = append(dirs, globalSymbolsDir)
	}
	return dirs
}

// FootprintSearchPath returns the directories holding "<LibName>.pretty"
// directories, in the same priority order as SymbolSearchPath.
func FootprintSearchPath(libsRoot, globalFootprintsDir string) []string {
	var dirs []string
	if libsRoot != "" {
		dirs = append(dirs,
			FootprintsPath(libsRoot),
			filepath.Join(libsRoot, "kicad-official", "kicad-footprints"),
			filepath.Join(libsRoot, "alternate"),
			filepath.Join(libsRoot, LegacyDownload),
		)
	}
	if globalFootprintsDir != "" {
		dirs = append(dirs, globalFootprintsDir)
	}
	return dirs
}

// FindSymbolLib returns the first existing "<libName>.kicad_sym" along the
// search path, and the directory it was found in.
func FindSymbolLib(searchPath []string, libName string) (path, dir string, ok bool) {
	for _, d := range searchPath {
		if d == "" {
			continue
		}
		p := filepath.Join(d, libName+".kicad_sym")
		if fileExists(p) {
			return p, d, true
		}
	}
	return "", "", false
}

// SourceLabel names where a library file came from, for the messages the tools
// print back to the model.
func SourceLabel(libsRoot, dir string) string {
	switch {
	case libsRoot != "" && dir == SymbolsPath(libsRoot):
		return "imported"
	case libsRoot != "" && dir == FootprintsPath(libsRoot):
		return "imported"
	case libsRoot != "" && dir == filepath.Join(libsRoot, "kicad-official", "kicad-symbols"),
		libsRoot != "" && dir == filepath.Join(libsRoot, "kicad-official", "kicad-footprints"):
		return "local-official"
	case libsRoot != "" && dir == filepath.Join(libsRoot, "alternate"):
		return "local-alternate"
	case libsRoot != "" && dir == filepath.Join(libsRoot, LegacyDownload):
		return "local-downloaded"
	}
	return "kicad-global"
}
