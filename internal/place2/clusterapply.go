package place2

import (
	"math"
	"strings"

	"mcp-kicad/internal/place2/cluster"
	"mcp-kicad/internal/place2/elk"
	"mcp-kicad/internal/sexp"
)

// ApplyClusterPull moves cluster satellites so they sit at canonical offsets
// from their anchor. Offsets are BBOX-AWARE: a satellite is placed at
// half_width + spacing away from the anchor centre, never inside the anchor's
// body.
//
// `positions` is mutated in place. Power symbols are never relocated by this
// function — ApplyPowerRails handles them separately and runs AFTER.
//
// Anchor key handling: clusters may name their anchor as "REF" (single-unit)
// OR "REF#unit" (multi-unit ICs — decoupling caps anchor on the power unit,
// not the canonical unit). Lookups try the anchor key as-is first, then fall
// back to the bare ref.
func ApplyClusterPull(syms []sexp.SchematicSymbol, clusters []Cluster, positions map[string][2]float64) int {
	if len(clusters) == 0 {
		return 0
	}
	powerRefs := make(map[string]bool)
	symByRef := make(map[string]sexp.SchematicSymbol)
	symByRefUnit := make(map[string]sexp.SchematicSymbol)
	for _, s := range syms {
		// Use the FIRST occurrence per ref so unit 1 is the canonical anchor
		// for non-multi-unit lookups.
		if _, ok := symByRef[s.Reference]; !ok {
			symByRef[s.Reference] = s
		}
		u := s.Unit
		if u <= 0 {
			u = 1
		}
		symByRefUnit[unitPosKey(s.Reference, u)] = s
		if strings.HasPrefix(s.LibID, "power:") {
			powerRefs[s.Reference] = true
		}
	}

	// Track occupied snap cells (1.27mm precision) — two satellites can't land
	// at the exact same coordinate. Larger collision avoidance happens via the
	// canonical offsets per cluster kind.
	occupied := make(map[[2]float64]bool)
	for ref, p := range positions {
		if powerRefs[ref] {
			continue
		}
		occupied[snapKey(p[0], p[1])] = true
	}

	moved := 0
	for _, c := range clusters {
		anchorSym, hasAnchor := lookupAnchor(c.Anchor, symByRefUnit, symByRef)
		anchorPos, ok := positions[c.Anchor]
		if !ok {
			// Multi-unit anchor "U1#3" might not have a synthetic position
			// yet — fall back to the bare ref so the cluster still pulls.
			ref, _ := splitAnchor(c.Anchor)
			if anchorPos, ok = positions[ref]; !ok {
				continue
			}
		}
		var satellites []string
		var satelliteSyms []sexp.SchematicSymbol
		for _, r := range c.Refs {
			if r == c.Anchor || powerRefs[r] {
				continue
			}
			satellites = append(satellites, r)
			if s, ok := symByRef[r]; ok {
				satelliteSyms = append(satelliteSyms, s)
			} else {
				satelliteSyms = append(satelliteSyms, sexp.SchematicSymbol{})
			}
		}
		if len(satellites) == 0 {
			continue
		}
		halfW, halfH := 5.08, 5.08
		anchorLibID := ""
		if hasAnchor {
			halfW, halfH = anchorHalfExtents(anchorSym)
			anchorLibID = anchorSym.LibID
		}
		offsets := bboxAwareOffsetsCtx(c.Kind, anchorLibID, satelliteSyms, halfW, halfH)
		for i, ref := range satellites {
			if i >= len(offsets) {
				break
			}
			cur, ok := positions[ref]
			if !ok {
				continue
			}
			off := offsets[i]
			newX := sexp.SnapGrid(anchorPos[0] + off[0])
			newY := sexp.SnapGrid(anchorPos[1] + off[1])
			const collisionStep = 7.62
			// Push in the direction the cluster's natural offset already
			// points so we don't push io_input connectors RIGHT of their
			// partner (or io_output LEFT). Sign of off[0]/off[1] gives us
			// "outward" from the anchor for that satellite.
			pushX := 0.0
			pushY := 0.0
			horizontalCluster := c.Kind == "decoupling" || c.Kind == "pullup" ||
				c.Kind == "header" || c.Kind == "io_connector" ||
				c.Kind == "io_input" || c.Kind == "io_output"
			if horizontalCluster {
				if off[0] < 0 {
					pushX = -collisionStep
				} else {
					pushX = collisionStep
				}
			} else {
				if off[1] < 0 {
					pushY = -collisionStep
				} else {
					pushY = collisionStep
				}
			}
			occupied[snapKey(cur[0], cur[1])] = false
			for tries := 0; tries < 8; tries++ {
				k := snapKey(newX, newY)
				if !occupied[k] {
					break
				}
				newX += pushX
				newY += pushY
			}
			if cur[0] != newX || cur[1] != newY {
				positions[ref] = [2]float64{newX, newY}
				moved++
			}
			occupied[snapKey(newX, newY)] = true
		}
	}
	return moved
}

// lookupAnchor finds the symbol for a cluster anchor, trying "REF#unit" then
// bare ref. Returns the symbol and a flag indicating whether anything was
// resolved.
func lookupAnchor(anchor string, byRefUnit, byRef map[string]sexp.SchematicSymbol) (sexp.SchematicSymbol, bool) {
	if s, ok := byRefUnit[anchor]; ok {
		return s, true
	}
	ref, _ := splitAnchor(anchor)
	if s, ok := byRef[ref]; ok {
		return s, true
	}
	if s, ok := byRefUnit[ref+"#1"]; ok {
		return s, true
	}
	return sexp.SchematicSymbol{}, false
}

// splitAnchor cracks "REF#unit" into (ref, unit). Single-unit anchors return
// (anchor, 1).
func splitAnchor(s string) (string, int) {
	idx := strings.Index(s, "#")
	if idx < 0 {
		return s, 1
	}
	ref := s[:idx]
	unit := 0
	for _, c := range s[idx+1:] {
		if c < '0' || c > '9' {
			return s, 1
		}
		unit = unit*10 + int(c-'0')
	}
	if unit < 1 {
		unit = 1
	}
	return ref, unit
}

// unitPosKey encodes "REF" (single-unit) or "REF#unit" (multi-unit) — must
// match the convention in tools/cluster_apply.go and place2/pipeline.go.
func unitPosKey(ref string, unit int) string {
	if unit <= 1 {
		return ref
	}
	return ref + "#" + clusterApplyItoa(unit)
}

func clusterApplyItoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// snapKey rounds a position to a canonical cell so floating-point jitter
// doesn't mask collisions between satellites.
func snapKey(x, y float64) [2]float64 {
	return [2]float64{math.Round(x*100) / 100, math.Round(y*100) / 100}
}

// ConvertClusters maps cluster.Cluster to the place2.Cluster type.
func ConvertClusters(in []cluster.Cluster) []Cluster {
	out := make([]Cluster, len(in))
	for i, c := range in {
		out[i] = Cluster{Kind: c.Kind, Refs: c.Refs, Anchor: c.Anchor}
	}
	return out
}

// anchorHalfExtents returns conservative half-width / half-height of the
// anchor body in mm. Uses the MAXIMUM of the pin bounding box plus pin
// length (the body extends pinLen inward from each pin tip) and the
// hand-tuned table in elk.SymbolSize. The op-amp triangle, for example,
// is much taller than its pin bbox, so the table dominates there.
func anchorHalfExtents(s sexp.SchematicSymbol) (halfW, halfH float64) {
	const pinLen = 2.54
	tableW, tableH := elk.SymbolSize(s.LibID)
	halfW = tableW / 2
	halfH = tableH / 2
	if len(s.Pins) >= 2 {
		minX, maxX := math.Inf(1), math.Inf(-1)
		minY, maxY := math.Inf(1), math.Inf(-1)
		for _, p := range s.Pins {
			if p.X < minX {
				minX = p.X
			}
			if p.X > maxX {
				maxX = p.X
			}
			if p.Y < minY {
				minY = p.Y
			}
			if p.Y > maxY {
				maxY = p.Y
			}
		}
		pinHalfW := (maxX-minX)/2 + pinLen
		pinHalfH := (maxY-minY)/2 + pinLen
		if pinHalfW > halfW {
			halfW = pinHalfW
		}
		if pinHalfH > halfH {
			halfH = pinHalfH
		}
	}
	return
}

// bboxAwareOffsetsCtx is the anchor-aware variant. It dispatches to special
// layouts when the anchor's lib_id calls for it (e.g. op-amp power units
// want bypass caps to FLANK the V+/V- pins, not stack below the body).
func bboxAwareOffsetsCtx(kind, anchorLibID string, sats []sexp.SchematicSymbol, halfW, halfH float64) [][2]float64 {
	if kind == "decoupling" && strings.HasPrefix(anchorLibID, "Amplifier_Operational:") {
		return opampDecouplingOffsets(sats, halfW, halfH)
	}
	return bboxAwareOffsets(kind, sats, halfW, halfH)
}

// opampDecouplingOffsets places bypass caps to the LEFT of the op-amp power
// unit. Cap 0 (V+ bypass) sits at the same Y level as V+ pin so its pin 1
// (top of vertical cap) lands at vPlusY exactly — the cap's center is at
// vPlusY + capPinHalf.
//
// KiCad's TL07x / NE5532 / LM358 power units have V+ at the unit Y - 7.62mm
// and V- at unit Y + 7.62mm. Vertical-cap pin 1 is at center - 3.81 (cap
// height = 7.62 between pins).
//
// Gap from cap right pin to op-amp left pin: 1.27mm (= 1 grid step) —
// tight enough that the wire connecting them is a single short segment,
// loose enough that KiCad's pin labels don't collide.
func opampDecouplingOffsets(sats []sexp.SchematicSymbol, halfW, halfH float64) [][2]float64 {
	count := len(sats)
	out := make([][2]float64, 0, count)
	const sep = 1.27 // tighter than the generic 5.08 cluster sep
	const capHalf = 3.81
	const vPlusY = -7.62
	const vMinusY = +7.62

	satHalfW := 0.0
	for _, s := range sats {
		sw, _ := elk.SymbolSize(s.LibID)
		if sw/2 > satHalfW {
			satHalfW = sw / 2
		}
	}
	if satHalfW < 1.27 {
		satHalfW = 1.27
	}
	leftX := -halfW - satHalfW - sep
	// Place cap CENTERS so their top pin (pin 1) lands exactly on V+/V- Y.
	// pin 1 = center - capHalf, so center = railY + capHalf.
	rails := []float64{vPlusY + capHalf, vMinusY + capHalf}
	for i := 0; i < count; i++ {
		if i < len(rails) {
			out = append(out, [2]float64{leftX, rails[i]})
		} else {
			out = append(out, [2]float64{leftX, rails[1] + float64(i-1)*7.62})
		}
	}
	return out
}

// bboxAwareOffsets returns canonical (dx, dy) satellite offsets in mm,
// scaled so the satellite never lands inside the anchor's body. Each cluster
// kind has its own canonical topology.
func bboxAwareOffsets(kind string, sats []sexp.SchematicSymbol, halfW, halfH float64) [][2]float64 {
	count := len(sats)
	out := make([][2]float64, 0, count)
	const sep = 5.08      // minimum air gap between bodies
	const stack = 7.62    // vertical spacing for stacked satellites

	// Half extent of the satellite (assume identical across the cluster, take
	// the largest to be safe).
	satHalfW, satHalfH := 0.0, 0.0
	for _, s := range sats {
		sw, sh := elk.SymbolSize(s.LibID)
		if sw/2 > satHalfW {
			satHalfW = sw / 2
		}
		if sh/2 > satHalfH {
			satHalfH = sh / 2
		}
	}
	if satHalfW < 2.54 {
		satHalfW = 2.54
	}
	if satHalfH < 3.81 {
		satHalfH = 3.81
	}

	switch kind {
	case "decoupling":
		// Caps below the IC, side-by-side, spaced cap-width apart.
		baseY := halfH + satHalfH + sep
		spread := stack
		startX := -float64(count-1) * spread / 2
		for i := 0; i < count; i++ {
			out = append(out, [2]float64{startX + float64(i)*spread, baseY})
		}
	case "pullup":
		// Pull-up resistors above the IC.
		baseY := -halfH - satHalfH - sep
		spread := stack
		startX := -float64(count-1) * spread / 2
		for i := 0; i < count; i++ {
			out = append(out, [2]float64{startX + float64(i)*spread, baseY})
		}
	case "lc_filter":
		// Inductor right of the regulator, output cap below it.
		out = append(out, [2]float64{halfW + satHalfW + sep, 0})
		for i := 1; i < count; i++ {
			out = append(out, [2]float64{halfW + satHalfW + sep, float64(i) * stack})
		}
	case "crystal":
		// Crystal anchor; load caps flank it on each side.
		offs := [][2]float64{
			{-halfW - satHalfW - sep, 0},
			{halfW + satHalfW + sep, 0},
			{0, halfH + satHalfH + sep},
		}
		for i := 0; i < count && i < len(offs); i++ {
			out = append(out, offs[i])
		}
	case "voltage_divider":
		// Two Rs in series; the second one stacks below the first.
		for i := 0; i < count; i++ {
			out = append(out, [2]float64{0, float64(i+1) * stack})
		}
	case "opamp_feedback":
		// Rf — feedback resistor (member[0] after rule ordering) — sits ABOVE
		// the op-amp body, aligned with the inverting input pin's vertical
		// axis so the top-loop wire is short and straight.
		//
		// Rin — input resistor (member[1]) — sits directly LEFT of the
		// inverting input pin so its right-side pin meets the input head-on.
		//
		// We use the inverting input's typical Y-offset (-2.54mm above the
		// op-amp centre for KiCad's TL072-style symbol) when placing Rin so
		// the wire from Rin's right pin into the inverting input is straight.
		const invertingPinYOffset = -2.54 // KiCad opamp inverting input is above centre
		offs := [][2]float64{
			{0, -halfH - satHalfH - sep},                          // Rf above the body (top-loop)
			{-halfW - satHalfW - sep, invertingPinYOffset},        // Rin left of inverting input
			{halfW + satHalfW + sep, halfH + satHalfH + sep},      // extra Rs: bottom-right
		}
		for i := 0; i < count && i < len(offs); i++ {
			out = append(out, offs[i])
		}
		for i := len(offs); i < count; i++ {
			out = append(out, [2]float64{halfW + satHalfW + sep, float64(i-len(offs)+1) * stack})
		}
	case "header":
		// Members go to the LEFT of the connector (which sits on the right
		// edge of the page by convention).
		baseX := -halfW - satHalfW - 2*sep
		startY := -float64(count-1) * stack / 2
		for i := 0; i < count; i++ {
			out = append(out, [2]float64{baseX, startY + float64(i)*stack})
		}
	case "bias_compensation":
		// Bias resistor sits LEFT of the op-amp at the same Y level as the
		// non-inverting input pin so its right pin meets U.+ head-on. The
		// + pin on KiCad TL07x/NE5532 sits at center + (−halfW, −2.54).
		const plusPinYOffset = -2.54
		baseX := -halfW - satHalfW - 2*sep
		baseY := plusPinYOffset
		for i := 0; i < count; i++ {
			out = append(out, [2]float64{baseX, baseY + float64(i)*stack})
		}
	case "io_connector", "io_input":
		// Input connector: LEFT of the partner so signal flows L→R.
		baseX := -halfW - satHalfW - 2*sep
		baseY := 0.0
		for i := 0; i < count; i++ {
			out = append(out, [2]float64{baseX, baseY + float64(i)*stack})
		}
	case "bypass_nonpower":
		// Cap below the IC, near the named pin (CTL/REF/FB). One cap, one slot.
		baseY := halfH + satHalfH + sep
		for i := 0; i < count; i++ {
			out = append(out, [2]float64{0, baseY + float64(i)*stack})
		}
	case "series_led":
		// Anchor = LED. Resistor sits to its LEFT so signal flows L→R into the LED.
		for i := 0; i < count; i++ {
			out = append(out, [2]float64{-halfW - satHalfW - sep, 0})
		}
	case "oscillator_rc":
		// Timing R/C cluster sits BELOW the IC (R+C side by side).
		baseY := halfH + satHalfH + sep
		spread := stack
		startX := -float64(count-1) * spread / 2
		for i := 0; i < count; i++ {
			out = append(out, [2]float64{startX + float64(i)*spread, baseY})
		}
	case "feedback_divider":
		// Two Rs stacked vertically to the RIGHT of the IC (FB pin convention).
		baseX := halfW + satHalfW + sep
		for i := 0; i < count; i++ {
			out = append(out, [2]float64{baseX, float64(i) * stack})
		}
	case "io_output":
		// Output connector: RIGHT of the partner so the output rail extends
		// off the right edge of the page, KiCad convention.
		baseX := halfW + satHalfW + 2*sep
		baseY := 0.0
		for i := 0; i < count; i++ {
			out = append(out, [2]float64{baseX, baseY + float64(i)*stack})
		}
	default:
		baseX := halfW + satHalfW + sep
		for i := 0; i < count; i++ {
			out = append(out, [2]float64{baseX, float64(i) * stack})
		}
	}
	return out
}
