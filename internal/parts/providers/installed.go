package providers

import (
	"context"
	"fmt"

	"mcp-kicad/internal/parts"
)

func init() { Register(func(env Env) Provider { return &installedProvider{env: env} }) }

// installedProvider searches what is already on this machine: the KiCad
// installation and anything previously imported.
//
// It exists so one search answers the whole question. Finding out that the
// part is already there AFTER downloading it from a repository is the failure
// this provider prevents, and it is also the fastest source by a wide margin —
// no network, no index, 22k symbols read straight off disk.
type installedProvider struct{ env Env }

func (p *installedProvider) Name() string { return "installed" }

func (p *installedProvider) Description() string {
	return "the KiCad libraries on this machine, plus everything already imported"
}

func (p *installedProvider) License() string { return "already installed" }

func (p *installedProvider) Available() bool {
	return p.env.KicadSymbols != "" || p.env.LibsRoot != ""
}

func (p *installedProvider) Search(_ context.Context, q Query) ([]Candidate, error) {
	if !q.wants(Symbol) {
		return nil, nil
	}
	dirs := parts.SymbolSearchPath(p.env.LibsRoot, p.env.KicadSymbols)
	hits := parts.FuzzySearchDirs(dirs, q.Text, q.limit())
	out := make([]Candidate, 0, len(hits))
	for _, h := range hits {
		out = append(out, Candidate{
			Provider:    p.Name(),
			ID:          h.LibID,
			MPN:         h.PartName,
			Description: h.Description,
			Has:         []AssetKind{Symbol},
			License:     "already installed",
			Score:       h.Score,
			Installed:   true,
			LibID:       h.LibID,
			SourceURL:   h.Dir,
		})
	}
	return out, nil
}

// Fetch always refuses, on purpose: there is nothing to import. Copying an
// installed symbol into the imported library would give the same part two
// names and two places to drift apart.
func (p *installedProvider) Fetch(_ context.Context, id string) (*Bundle, error) {
	return nil, fmt.Errorf(
		"%q is already installed and usable as lib_id %q — put that straight into your .design.json, there is nothing to import",
		id, id)
}
