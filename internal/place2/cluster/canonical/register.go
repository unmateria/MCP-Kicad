package canonical

import "mcp-kicad/internal/place2/cluster"

func init() {
	for _, d := range All() {
		det := d.Detect // capture
		cluster.RegisterExtra(d.Name, det)
	}
}
