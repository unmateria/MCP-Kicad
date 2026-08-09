package importer

import "strings"

// CanonicalMPN is the name an imported part is installed under.
//
// One rule, applied by the importer and never by a provider: sources spell the
// same order code half a dozen ways, and letting each keep its own spelling
// gives you a library where the ESP32 you imported last month cannot be found
// by the name you remember.
//
//	"ne555p"            → "NE555P"
//	"ESP32-C3-MINI-1"   → "ESP32-C3-MINI-1"
//	"LM2596S-5.0"       → "LM2596S-5.0"
//	"Foo / Bar (rev A)" → "FOO_BAR_REV_A"
func CanonicalMPN(s string) string {
	var b strings.Builder
	lastUnderscore := false
	for _, r := range strings.ToUpper(strings.TrimSpace(s)) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '+', r == '-':
			if r == '_' {
				if lastUnderscore {
					continue
				}
				lastUnderscore = true
			} else {
				lastUnderscore = false
			}
			b.WriteRune(r)
		default:
			if lastUnderscore {
				continue
			}
			lastUnderscore = true
			b.WriteRune('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

// FootprintName pairs a footprint with the part it belongs to:
// "NE555P__DIP-8_W7.62mm". Two parts that share a package would otherwise
// fight over one file name, and whichever was imported last would silently
// replace the other's footprint.
//
// Package case is preserved — KiCad's own footprints are named
// SOIC-8_3.9x4.9mm_P1.27mm and mangling that makes them unrecognisable.
func FootprintName(mpn, pkg string) string {
	mpn = CanonicalMPN(mpn)
	pkg = sanitizeFileComponent(pkg)
	if pkg == "" {
		return mpn
	}
	if strings.HasPrefix(pkg, mpn+"__") || pkg == mpn {
		return pkg // the source already named it after the part
	}
	return mpn + "__" + pkg
}

// sanitizeFileComponent strips what a file name cannot hold, on any of the
// platforms this server runs on. Repeated underscores are left alone: "__" is
// the separator FootprintName puts between the part and its package, and
// collapsing it turns NE555P__DIP-8 into a name that no longer round-trips.
func sanitizeFileComponent(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', 0:
			r = '_'
		}
		b.WriteRune(r)
	}
	return strings.Trim(b.String(), "_ .")
}
