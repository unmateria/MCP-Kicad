package route2

import "math"

// Pin is a routing endpoint with optional outgoing direction (degrees, screen
// coords; -1 for unknown).
type Pin struct {
	Ref string
	X   float64
	Y   float64
	Dir float64 // 0/90/180/270 or -1
}

// CollinearGroup is a set of pins that share a row (Horizontal) or column
// (Vertical). The Axis value is the shared coordinate.
type CollinearGroup struct {
	Orientation byte // 'H' (shared Y) or 'V' (shared X)
	Axis        float64
	Pins        []Pin
}

// DetectCollinearGroups partitions `pins` by approximate axis sharing within
// `tol` mm. A pin can appear in at most one group of each orientation; the
// caller picks which to keep. Pins with no peers are not returned.
//
// Two passes:
//  1. group by snapped Y (horizontal trunks)
//  2. group by snapped X (vertical trunks)
func DetectCollinearGroups(pins []Pin, tol float64) []CollinearGroup {
	if tol <= 0 {
		tol = 1.27
	}
	var out []CollinearGroup
	for _, orient := range []byte{'H', 'V'} {
		buckets := make(map[int64][]Pin)
		for _, p := range pins {
			var key int64
			if orient == 'H' {
				key = int64(math.Round(p.Y / tol))
			} else {
				key = int64(math.Round(p.X / tol))
			}
			buckets[key] = append(buckets[key], p)
		}
		for k, group := range buckets {
			if len(group) < 2 {
				continue
			}
			axis := float64(k) * tol
			out = append(out, CollinearGroup{Orientation: orient, Axis: axis, Pins: group})
		}
	}
	return out
}
