package parts

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// footprintTable maps libID → mountType → footprint reference.
// MountType is "THT" or "SMD" (uppercase).
var footprintTable = map[string]map[string]string{
	"Device:R": {
		"SMD": "Resistor_SMD:R_0603_1608Metric",
		"THT": "Resistor_THT:R_Axial_DIN0207_L6.3mm_D2.5mm_P7.62mm_Horizontal",
	},
	"Device:C": {
		"SMD": "Capacitor_SMD:C_0603_1608Metric",
		"THT": "Capacitor_THT:C_Disc_D5.0mm_W2.5mm_P5.00mm",
	},
	"Device:C_Polarized": {
		"SMD": "Capacitor_SMD:CP_Elec_5x5.4",
		"THT": "Capacitor_THT:CP_Radial_D5.0mm_P2.50mm",
	},
	"Device:LED": {
		"SMD": "LED_SMD:LED_0603_1608Metric",
		"THT": "LED_THT:LED_D5.0mm",
	},
	"Device:Battery_Cell": {
		"THT": "Battery:BatteryHolder_Keystone_1042_1x18650",
	},
	"Device:Battery": {
		"THT": "Battery:BatteryHolder_Keystone_1042_1x18650",
	},
	"Device:D": {
		"SMD": "Diode_SMD:D_SOD-123",
		"THT": "Diode_THT:D_DO-41_SOD81_P10.16mm_Horizontal",
	},
	"Device:D_Zener": {
		"SMD": "Diode_SMD:D_SOD-123",
		"THT": "Diode_THT:D_DO-41_SOD81_P10.16mm_Horizontal",
	},
	// The pad numbers of SOT-23 follow the B-E-C order, so a B-C-E symbol on
	// it swaps collector and emitter: no SMD default for these.
	"Transistor_BJT:Q_NPN_BCE": {"THT": "Package_TO_SOT_THT:TO-92_Inline"},
	"Transistor_BJT:Q_PNP_BCE": {"THT": "Package_TO_SOT_THT:TO-92_Inline"},
	"Device:SW_Push": {
		"SMD": "Button_Switch_SMD:SW_SPST_B3S-1000",
		"THT": "Button_Switch_THT:SW_PUSH_6mm",
	},
	"Device:L": {
		"SMD": "Inductor_SMD:L_0603_1608Metric",
		"THT": "Inductor_THT:L_Axial_L5.3mm_D2.2mm_P10.16mm_Horizontal_Vishay_IM-1",
	},
	"Device:Crystal": {
		"THT": "Crystal:Crystal_HC49-4H_Vertical",
		"SMD": "Crystal:Crystal_SMD_HC49-SD",
	},
	// The L78xx/L79xx symbols are generic; TO-220 is the package their part
	// numbers without a suffix letter are sold in.
	"Regulator_Linear:L7805": {"THT": "Package_TO_SOT_THT:TO-220-3_Vertical"},
	"Regulator_Linear:L7812": {"THT": "Package_TO_SOT_THT:TO-220-3_Vertical"},
	"Regulator_Linear:L7905": {"THT": "Package_TO_SOT_THT:TO-220-3_Vertical"},
	"Regulator_Linear:L7912": {"THT": "Package_TO_SOT_THT:TO-220-3_Vertical"},
	"4xxx:4029": {
		"THT": "Package_DIP:DIP-16_W7.62mm",
		"SMD": "Package_SO:SOIC-16_3.9x9.9mm_P1.27mm",
	},
	"4xxx_IEEE:4511": {
		"THT": "Package_DIP:DIP-16_W7.62mm",
		"SMD": "Package_SO:SOIC-16_3.9x9.9mm_P1.27mm",
	},
	"Device:R_Potentiometer": {
		"THT": "Potentiometer_THT:Potentiometer_Bourns_3296W_Vertical",
		"SMD": "Potentiometer_SMD:Potentiometer_Bourns_3214W_Vertical",
	},
	"Connector_Audio:AudioJack3": {
		"THT": "Connector_Audio:Jack_3.5mm_QingPu_WQP-PJ398SM_Vertical_CircularHoles",
		"SMD": "Connector_Audio:Jack_3.5mm_CUI_SJ-3523-SMT_Horizontal",
	},
	// Op-amps and dual op-amps in DIP-8 / SOIC-8 packages (NE5532, LM358, TL072…).
	"Amplifier_Operational:NE5532": {
		"THT": "Package_DIP:DIP-8_W7.62mm",
		"SMD": "Package_SO:SOIC-8_3.9x4.9mm_P1.27mm",
	},
	"Amplifier_Operational:LM358": {
		"THT": "Package_DIP:DIP-8_W7.62mm",
		"SMD": "Package_SO:SOIC-8_3.9x4.9mm_P1.27mm",
	},
	"Amplifier_Operational:TL072": {
		"THT": "Package_DIP:DIP-8_W7.62mm",
		"SMD": "Package_SO:SOIC-8_3.9x4.9mm_P1.27mm",
	},
	"Amplifier_Operational:TL082": {
		"THT": "Package_DIP:DIP-8_W7.62mm",
		"SMD": "Package_SO:SOIC-8_3.9x4.9mm_P1.27mm",
	},
	// Single op-amps in DIP-8/SOIC-8.
	"Amplifier_Operational:LM741": {
		"THT": "Package_DIP:DIP-8_W7.62mm",
		"SMD": "Package_SO:SOIC-8_3.9x4.9mm_P1.27mm",
	},
	// Common 555 timer.
	"Timer:NE555P": {
		"THT": "Package_DIP:DIP-8_W7.62mm",
	},
	"Timer:LM555": {
		"THT": "Package_DIP:DIP-8_W7.62mm",
		"SMD": "Package_SO:SOIC-8_3.9x4.9mm_P1.27mm",
	},
}

// SuggestFootprint returns the best default footprint reference for a given
// component libID and mount type ("THT" or "SMD").
// Returns "" if no suggestion exists for that combination.
func SuggestFootprint(libID, mountType string) string {
	mt := strings.ToUpper(mountType)
	entry, ok := footprintTable[libID]
	if !ok {
		return connectorFootprint(libID)
	}
	if fp, ok := entry[mt]; ok {
		return fp
	}
	// Unknown or empty mount type: fall back in a FIXED order. Ranging over the
	// map here returned THT or SMD depending on the run, so the same design
	// compiled to two different boards.
	for _, alt := range []string{"THT", "SMD"} {
		if fp, ok := entry[alt]; ok {
			return fp
		}
	}
	keys := make([]string, 0, len(entry))
	for k := range entry {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		return entry[keys[0]]
	}
	return ""
}

// GlobalFootprintSearch searches the KiCad global footprints directory for a
// .kicad_mod file matching the given query ("LibName:FootprintName").
// globalFpDir is the path to the footprints folder, e.g.
// "C:/Program Files/KiCad/10.0/share/kicad/footprints".
func GlobalFootprintSearch(globalFpDir, query string) (*ComponentResult, error) {
	parts := strings.SplitN(query, ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("footprints: query must be LibName:FootprintName, got %q", query)
	}
	libName, fpName := parts[0], parts[1]
	fpPath := filepath.Join(globalFpDir, libName+".pretty", fpName+".kicad_mod")
	if info, err := os.Stat(fpPath); err == nil && info.Mode().IsRegular() {
		return &ComponentResult{
			Source:   "kicad-global-fp",
			Path:     fpPath,
			LibName:  libName,
			PartName: fpName,
		}, nil
	}
	return nil, fmt.Errorf("footprints: %q not found in global KiCad footprints", query)
}

// connectorRe matches KiCad's single-row connector symbols:
// Connector_Generic:Conn_01x04, Connector:Conn_01x04_Pin, …_Socket.
var connectorRe = regexp.MustCompile(`^Connector(?:_Generic)?:Conn_01x(\d\d)(_Pin|_Socket)?$`)

// connectorFootprint maps a single-row connector symbol to the 2.54 mm
// header with the same pin count: a socket for _Socket, a pin header
// otherwise. The pitch is a choice, not a fact about the symbol, which is why
// the compile report lists it as a default.
func connectorFootprint(libID string) string {
	m := connectorRe.FindStringSubmatch(libID)
	if m == nil {
		return ""
	}
	if m[2] == "_Socket" {
		return "Connector_PinSocket_2.54mm:PinSocket_1x" + m[1] + "_P2.54mm_Vertical"
	}
	return "Connector_PinHeader_2.54mm:PinHeader_1x" + m[1] + "_P2.54mm_Vertical"
}

// DefaultFootprintRefs lists every footprint the defaults can name, sorted,
// so a test can check them against an installed KiCad.
func DefaultFootprintRefs() []string {
	seen := map[string]bool{}
	for _, e := range footprintTable {
		for _, fp := range e {
			seen[fp] = true
		}
	}
	for n := 1; n <= 40; n++ {
		for _, suffix := range []string{"", "_Pin", "_Socket"} {
			seen[connectorFootprint(fmt.Sprintf("Connector:Conn_01x%02d%s", n, suffix))] = true
		}
	}
	out := make([]string, 0, len(seen))
	for fp := range seen {
		out = append(out, fp)
	}
	sort.Strings(out)
	return out
}
