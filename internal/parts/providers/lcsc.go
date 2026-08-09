package providers

import (
	"context"
	"fmt"
	"strings"

	"mcp-kicad/internal/parts/easyeda"
)

func init() { Register(func(env Env) Provider { return &lcscProvider{env: env} }) }

// lcscProvider serves LCSC parts through EasyEDA's API, converting them into
// KiCad files on the way past.
//
// This is the long tail. The Git repositories carry curated selections —
// thousands of parts each — while LCSC lists millions, and for anything
// obscure it is the only source that has it at all. The price is that nothing
// there is a KiCad file: every symbol and footprint is converted, which is why
// import_part's verification matters more here than anywhere else.
type lcscProvider struct{ env Env }

func (p *lcscProvider) Name() string { return "lcsc" }

func (p *lcscProvider) Description() string {
	return "LCSC / EasyEDA, converted to KiCad — millions of parts, searchable by C-number"
}

func (p *lcscProvider) License() string { return "third-party data; check the manufacturer's terms" }

func (p *lcscProvider) Available() bool { return true }

// Search resolves an LCSC number to a single candidate.
//
// EasyEDA has no public keyword search endpoint — only lookup by part number —
// so a query that is not a C-number returns nothing rather than a guess. The
// repository sources answer keyword searches; this one answers "I have the
// LCSC code from the BOM".
func (p *lcscProvider) Search(ctx context.Context, q Query) ([]Candidate, error) {
	if !q.wants(Symbol, Footprint) {
		return nil, nil
	}
	id := lcscNumber(q.Text)
	if id == "" {
		return nil, nil
	}
	comp, err := easyeda.NewClient(p.env.Client()).Fetch(ctx, id)
	if err != nil {
		// A part number that does not exist is not an error worth surfacing:
		// the user typed something C-shaped and it was not an LCSC code.
		return nil, nil
	}
	has := []AssetKind{Symbol}
	if len(comp.PackageDetail.DataStr.Shape) > 0 {
		has = append(has, Footprint)
	}
	return []Candidate{{
		Provider:     p.Name(),
		ID:           id,
		MPN:          comp.MPN(),
		Manufacturer: comp.Manufacturer(),
		Description:  comp.Description,
		Package:      comp.Package(),
		Has:          has,
		License:      p.License(),
		SourceURL:    comp.Datasheet(),
		Datasheet:    comp.Datasheet(),
		// An exact part-number lookup outranks a fuzzy hit from a repository,
		// because the user gave the code that identifies exactly one part.
		Score: 1000,
	}}, nil
}

func (p *lcscProvider) Fetch(ctx context.Context, id string) (*Bundle, error) {
	num := lcscNumber(id)
	if num == "" {
		return nil, fmt.Errorf("lcsc: %q is not an LCSC part number", id)
	}
	comp, err := easyeda.NewClient(p.env.Client()).Fetch(ctx, num)
	if err != nil {
		return nil, err
	}
	conv, err := easyeda.Convert(comp)
	if err != nil {
		return nil, err
	}

	b := &Bundle{
		Candidate: Candidate{
			Provider:     p.Name(),
			ID:           num,
			MPN:          conv.MPN,
			Manufacturer: conv.Manufacturer,
			Description:  conv.Description,
			Package:      conv.Package,
			Has:          []AssetKind{Symbol},
			License:      p.License(),
			SourceURL:    conv.Datasheet,
			Datasheet:    conv.Datasheet,
		},
		Assets:     map[AssetKind][]byte{Symbol: conv.SymbolLib},
		SymbolName: conv.SymbolName,
		Notes:      append([]string(nil), conv.Notes...),
	}
	if len(conv.Footprint) > 0 {
		b.Assets[Footprint] = conv.Footprint
		b.Has = append(b.Has, Footprint)
		b.FootprintRef = conv.FootprintName
	}
	b.Notes = append(b.Notes,
		"converted from EasyEDA, not authored for KiCad: EasyEDA records no electrical type "+
			"for its pins, so they arrive as `unspecified` and ERC will ask about every one. "+
			"Look at the symbol picture before trusting the pin order.")
	return b, nil
}

// lcscNumber extracts a C-number from a query. LCSC codes are written with and
// without the C, in either case, and pasted with surrounding text.
func lcscNumber(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for _, field := range strings.Fields(s) {
		f := strings.ToUpper(strings.Trim(field, ".,;:()[]"))
		if len(f) < 2 || f[0] != 'C' {
			continue
		}
		digits := f[1:]
		allDigits := true
		for _, r := range digits {
			if r < '0' || r > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			return f
		}
	}
	return ""
}
