package parts

import (
	"fmt"
	"os"
	"path/filepath"
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
		"SMD": "Capacitor_SMD:C_0603_1608Metric",
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
	"Device:Q_NPN_BCE": {
		"SMD": "Package_TO_SOT_SMD:SOT-23",
		"THT": "Package_TO_SOT_THT:TO-92_Inline",
	},
	"Device:Q_PNP_BCE": {
		"SMD": "Package_TO_SOT_SMD:SOT-23",
		"THT": "Package_TO_SOT_THT:TO-92_Inline",
	},
	"Device:SW_Push": {
		"SMD": "Button_SMD:SW_SPST_B3SN",
		"THT": "Button_THT:SW_Push_6mm_H5mm",
	},
	"Device:L": {
		"SMD": "Inductor_SMD:L_0603_1608Metric",
		"THT": "Inductor_THT:L_Axial_L5.3mm_D2.2mm_P10.16mm_Horizontal_Vishay_IHLPseries",
	},
	"Connector:Conn_01x02_Pin": {
		"THT": "Connector_PinHeader_2.54mm:PinHeader_1x02_P2.54mm_Vertical",
		"SMD": "Connector_PinHeader_2.54mm:PinHeader_1x02_P2.54mm_Vertical",
	},
	"Connector:Conn_01x03_Pin": {
		"THT": "Connector_PinHeader_2.54mm:PinHeader_1x03_P2.54mm_Vertical",
		"SMD": "Connector_PinHeader_2.54mm:PinHeader_1x03_P2.54mm_Vertical",
	},
	"Device:R_Potentiometer": {
		"THT": "Potentiometer_THT:Potentiometer_Bourns_3306P_Vertical",
		"SMD": "Potentiometer_SMD:Potentiometer_Bourns_3214W_Vertical",
	},
	"Device:R_Potentiometer_Dual": {
		"THT": "Potentiometer_THT:Potentiometer_Bourns_PDB18-J_Single_Vertical",
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
		return ""
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
