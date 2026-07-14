package route2

import (
	"mcp-kicad/internal/sexp"
)

// LabelAt computes the orientation and offset position for a net label at
// the given pin. The label angle is the OPPOSITE of the pin's outgoing
// direction so the label text reads outward from the symbol.
//
// Returns (x, y, angleDeg) ready to feed sexp.NewNetLabel.
func LabelAt(pin sexp.PinInfo) (x, y, angle float64) {
	const off = 1.27
	dx, dy := pin.DirDelta()
	x = pin.X + dx*off
	y = pin.Y + dy*off
	// Label angle: 0=right, 90=up, 180=left, 270=down.
	// The pin's outgoing direction is in screen frame; the label rotates
	// 180° from it so its anchor sits at the pin and the text trails outward.
	angle = pin.Direction
	switch int(angle) % 360 {
	case 0:
		angle = 0
	case 90:
		angle = 90
	case 180:
		angle = 180
	case 270:
		angle = 270
	}
	return
}
