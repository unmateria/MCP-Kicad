package easyeda

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// rec is one EasyEDA graphic: a record whose fields are separated by tildes,
// with sub-records separated by "^^".
type rec struct {
	kind  string
	parts []string   // the first "^^" group, split on "~"
	subs  [][]string // every "^^" group, split on "~"
}

func parseRec(s string) rec {
	groups := strings.Split(s, "^^")
	r := rec{subs: make([][]string, 0, len(groups))}
	for _, g := range groups {
		r.subs = append(r.subs, strings.Split(g, "~"))
	}
	r.parts = r.subs[0]
	if len(r.parts) > 0 {
		r.kind = r.parts[0]
	}
	return r
}

// at returns field i of the first group, or "".
func (r rec) at(i int) string {
	if i < len(r.parts) {
		return r.parts[i]
	}
	return ""
}

// num returns field i as a float, or 0.
func (r rec) num(i int) float64 { return atof(r.at(i)) }

// sub returns field j of "^^" group i, or "".
func (r rec) sub(i, j int) string {
	if i < len(r.subs) && j < len(r.subs[i]) {
		return r.subs[i][j]
	}
	return ""
}

func atof(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return v
}

// fmtNum prints a coordinate the way KiCad does: enough precision to be exact,
// no trailing zeros to make the file noisy.
func fmtNum(v float64) string {
	if v == 0 {
		return "0" // avoid "-0", which KiCad writes as 0 and diffs badly against
	}
	s := strconv.FormatFloat(v, 'f', 6, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimSuffix(s, ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

// frame converts EasyEDA coordinates into KiCad ones.
//
// The two editors differ in exactly two ways, and getting either wrong
// produces a part that looks plausible and is mirrored:
//
//   - everything is relative to the document's own origin, not to (0,0);
//   - the schematic's Y axis points DOWN in EasyEDA and UP in KiCad, so a
//     symbol needs its Y negated. A footprint does not: KiCad's board Y also
//     points down.
type frame struct {
	ox, oy float64
	flipY  bool
}

func (f frame) x(v float64) float64 { return (v - f.ox) * Scale }

func (f frame) y(v float64) float64 {
	d := (v - f.oy) * Scale
	if f.flipY {
		return -d
	}
	return d
}

// len converts a length, which carries no origin.
func (f frame) length(v float64) float64 { return v * Scale }

// pt is a converted point.
type pt struct{ X, Y float64 }

func (f frame) pt(x, y float64) pt { return pt{f.x(x), f.y(y)} }

// points parses a "x1 y1 x2 y2 …" list.
func (f frame) points(s string) []pt {
	fields := strings.Fields(strings.ReplaceAll(s, ",", " "))
	out := make([]pt, 0, len(fields)/2)
	for i := 0; i+1 < len(fields); i += 2 {
		out = append(out, f.pt(atof(fields[i]), atof(fields[i+1])))
	}
	return out
}

// --- SVG path handling -------------------------------------------------
//
// EasyEDA stores pin stems, arcs and filled regions as SVG path data. Only the
// subset those actually use is handled: absolute and relative moves and lines,
// horizontal and vertical shorthands, elliptical arcs and close.

type pathSeg struct {
	cmd    byte
	points []pt   // in EasyEDA units, untransformed
	arc    arcDef // valid when cmd == 'A'
}

type arcDef struct {
	rx, ry     float64
	rotation   float64
	largeArc   bool
	sweep      bool
	end        pt
	start      pt
	hasEllipse bool
}

// parsePath splits SVG path data into absolute segments, still in EasyEDA
// units. Returning raw units keeps the transform in one place.
func parsePath(d string) []pathSeg {
	tokens := tokenizePath(d)
	var (
		segs         []pathSeg
		cur, start   pt
		i            int
		lastCmd      byte
		haveCurrent  bool
		readTwo      = func() (float64, float64) { a := atof(tokens[i]); b := atof(tokens[i+1]); i += 2; return a, b }
		readOne      = func() float64 { a := atof(tokens[i]); i++; return a }
		appendLineTo = func(p pt) {
			segs = append(segs, pathSeg{cmd: 'L', points: []pt{cur, p}})
			cur = p
		}
	)
	for i < len(tokens) {
		tok := tokens[i]
		var cmd byte
		if len(tok) == 1 && isPathCmd(tok[0]) {
			cmd = tok[0]
			i++
		} else if lastCmd != 0 {
			cmd = implicitRepeat(lastCmd) // a repeated coordinate list continues the last command
		} else {
			i++
			continue
		}
		lastCmd = cmd
		rel := cmd >= 'a' && cmd <= 'z'
		base := pt{}
		if rel && haveCurrent {
			base = cur
		}
		switch upper(cmd) {
		case 'M':
			if i+1 >= len(tokens) {
				return segs
			}
			x, y := readTwo()
			cur = pt{base.X + x, base.Y + y}
			start = cur
			haveCurrent = true
			segs = append(segs, pathSeg{cmd: 'M', points: []pt{cur}})
		case 'L':
			if i+1 >= len(tokens) {
				return segs
			}
			x, y := readTwo()
			appendLineTo(pt{base.X + x, base.Y + y})
		case 'H':
			if i >= len(tokens) {
				return segs
			}
			x := readOne()
			appendLineTo(pt{base.X + x, cur.Y})
		case 'V':
			if i >= len(tokens) {
				return segs
			}
			y := readOne()
			appendLineTo(pt{cur.X, base.Y + y})
		case 'A':
			if i+6 >= len(tokens) {
				return segs
			}
			rx, ry := readTwo()
			rot := readOne()
			large := readOne() != 0
			sweep := readOne() != 0
			x, y := readTwo()
			end := pt{base.X + x, base.Y + y}
			segs = append(segs, pathSeg{cmd: 'A', arc: arcDef{
				rx: rx, ry: ry, rotation: rot, largeArc: large, sweep: sweep,
				start: cur, end: end, hasEllipse: math.Abs(rx-ry) > 1e-9,
			}})
			cur = end
		case 'Z':
			segs = append(segs, pathSeg{cmd: 'Z', points: []pt{cur, start}})
			cur = start
		default:
			// A curve or something else this converter does not translate.
			// Skipping the numbers keeps the rest of the path readable.
			i++
		}
	}
	return segs
}

func tokenizePath(d string) []string {
	var out []string
	var num strings.Builder
	flush := func() {
		if num.Len() > 0 {
			out = append(out, num.String())
			num.Reset()
		}
	}
	for i := 0; i < len(d); i++ {
		ch := d[i]
		switch {
		case isPathCmd(ch):
			flush()
			out = append(out, string(ch))
		case ch == ' ' || ch == ',' || ch == '\t' || ch == '\n' || ch == '\r':
			flush()
		case ch == '-' && num.Len() > 0 && d[i-1] != 'e' && d[i-1] != 'E':
			flush()
			num.WriteByte(ch)
		default:
			num.WriteByte(ch)
		}
	}
	flush()
	return out
}

func isPathCmd(c byte) bool {
	switch upper(c) {
	case 'M', 'L', 'H', 'V', 'A', 'Z', 'C', 'S', 'Q', 'T':
		return true
	}
	return false
}

func upper(c byte) byte {
	if c >= 'a' && c <= 'z' {
		return c - 32
	}
	return c
}

// implicitRepeat is what a bare coordinate list continues. SVG says a repeated
// moveto becomes a lineto; every other command repeats itself.
func implicitRepeat(c byte) byte {
	switch c {
	case 'M':
		return 'L'
	case 'm':
		return 'l'
	}
	return c
}

// arcMidpoint returns the point halfway along an SVG elliptical arc, which is
// what KiCad's (fp_arc …) wants instead of SVG's flags.
//
// This is the endpoint-to-centre parameterisation from the SVG specification,
// implemented for the circular case. EasyEDA draws silkscreen outlines and
// pin-1 markers with these, so dropping them loses the marking that says which
// end of the package is which.
func arcMidpoint(a arcDef) (pt, bool) {
	if a.rx == 0 || a.ry == 0 {
		return pt{}, false
	}
	rx, ry := math.Abs(a.rx), math.Abs(a.ry)
	phi := a.rotation * math.Pi / 180

	dx2 := (a.start.X - a.end.X) / 2
	dy2 := (a.start.Y - a.end.Y) / 2
	cosP, sinP := math.Cos(phi), math.Sin(phi)
	x1 := cosP*dx2 + sinP*dy2
	y1 := -sinP*dx2 + cosP*dy2

	// Grow the radii when they are too small to span the endpoints, exactly as
	// the specification requires; EasyEDA rounds its numbers and produces this.
	lambda := (x1*x1)/(rx*rx) + (y1*y1)/(ry*ry)
	if lambda > 1 {
		s := math.Sqrt(lambda)
		rx *= s
		ry *= s
	}

	num := rx*rx*ry*ry - rx*rx*y1*y1 - ry*ry*x1*x1
	den := rx*rx*y1*y1 + ry*ry*x1*x1
	if den == 0 {
		return pt{}, false
	}
	coef := math.Sqrt(math.Max(0, num/den))
	if a.largeArc == a.sweep {
		coef = -coef
	}
	cx1 := coef * rx * y1 / ry
	cy1 := -coef * ry * x1 / rx

	cx := cosP*cx1 - sinP*cy1 + (a.start.X+a.end.X)/2
	cy := sinP*cx1 + cosP*cy1 + (a.start.Y+a.end.Y)/2

	theta1 := angleBetween(1, 0, (x1-cx1)/rx, (y1-cy1)/ry)
	delta := angleBetween((x1-cx1)/rx, (y1-cy1)/ry, (-x1-cx1)/rx, (-y1-cy1)/ry)
	delta = math.Mod(delta, 2*math.Pi)
	if !a.sweep && delta > 0 {
		delta -= 2 * math.Pi
	} else if a.sweep && delta < 0 {
		delta += 2 * math.Pi
	}

	mid := theta1 + delta/2
	mx := cosP*rx*math.Cos(mid) - sinP*ry*math.Sin(mid) + cx
	my := sinP*rx*math.Cos(mid) + cosP*ry*math.Sin(mid) + cy
	return pt{mx, my}, true
}

func angleBetween(ux, uy, vx, vy float64) float64 {
	dot := ux*vx + uy*vy
	lens := math.Hypot(ux, uy) * math.Hypot(vx, vy)
	if lens == 0 {
		return 0
	}
	ang := math.Acos(math.Max(-1, math.Min(1, dot/lens)))
	if ux*vy-uy*vx < 0 {
		return -ang
	}
	return ang
}

// snapAngle rounds a direction to the four orientations KiCad allows a pin to
// take. EasyEDA's own numbers are already axis-aligned; this guards against a
// rounding error turning a horizontal pin into a 1-degree diagonal, which
// KiCad accepts and then refuses to connect anything to.
func snapAngle(dx, dy float64) (int, error) {
	switch {
	case math.Abs(dx) < 1e-6 && math.Abs(dy) < 1e-6:
		return 0, fmt.Errorf("zero-length direction")
	case math.Abs(dx) >= math.Abs(dy) && dx > 0:
		return 0, nil
	case math.Abs(dx) >= math.Abs(dy):
		return 180, nil
	case dy > 0:
		return 90, nil
	default:
		return 270, nil
	}
}
