package tools

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// kicadProTemplate is share/kicad/template/kicad.kicad_pro from KiCad 10:
// what KiCad itself writes for File > New Project. KiCad fills in every
// setting it omits.
const kicadProTemplate = `{
  "board": {
    "design_settings": {
      "defaults": {},
      "diff_pair_dimensions": [],
      "drc_exclusions": [],
      "rules": {},
      "track_widths": [],
      "via_dimensions": []
    }
  },
  "boards": [],
  "libraries": {
    "pinned_footprint_libs": [],
    "pinned_symbol_libs": []
  },
  "meta": {
    "filename": "FILENAME",
    "version": 1
  },
  "net_settings": {
    "classes": [],
    "meta": {
      "version": 0
    }
  },
  "pcbnew": {
    "page_layout_descr_file": ""
  },
  "sheets": [],
  "text_variables": {}
}
`

// ensureProjectFile writes <name>.kicad_pro next to <name>.kicad_sch unless
// one is already there. A lone schematic opens in KiCad, but only a project
// offers "Update PCB from Schematic" — the step that turns the assigned
// footprints into a board. An existing project is the user's and is never
// touched. Reports whether a file was written.
func ensureProjectFile(schPath string) (bool, error) {
	pro := strings.TrimSuffix(schPath, ".kicad_sch") + ".kicad_pro"
	if _, err := os.Stat(pro); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	content := strings.Replace(kicadProTemplate, "FILENAME", filepath.Base(pro), 1)
	return true, os.WriteFile(pro, []byte(content), 0o644)
}
