package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"mcp-kicad/internal/fplib"
	"mcp-kicad/internal/parts"
	"mcp-kicad/internal/sexp"
	"mcp-kicad/internal/symlib"
)

// repoSource describes a Git repository that publishes KiCad libraries as
// plain files. CERN, JLCPCB, Digi-Key and the manufacturer repos differ only
// in these fields, so they share one implementation and each costs a literal.
type repoSource struct {
	name        string
	description string
	license     string
	homepage    string

	// listTree returns the file paths under dirs. A forge that can list its
	// whole tree in one request ignores dirs; one that paginates uses them to
	// avoid walking directories nobody asked about.
	listTree func(ctx context.Context, p *repoProvider, dirs []string) ([]string, error)
	// rawURL builds a download URL for a path inside the repository.
	rawURL func(repoPath string) string

	// symbolDir filters the tree for symbol libraries. Empty means "anywhere".
	symbolDir string
	// footprintDir filters the tree for footprints. EMPTY MEANS "do not index
	// this source's footprints": CERN publishes thousands of them but nothing
	// outside its SQLite says which symbol each belongs to, so walking them
	// would cost minutes and buy nothing.
	footprintDir string

	// fallbackSymbolLibs keeps the provider usable when tree discovery fails —
	// GitHub rate-limits unauthenticated API calls to 60 an hour, and running
	// out of them must not make an installed source stop working.
	fallbackSymbolLibs []string
}

// repoProvider serves one repoSource.
type repoProvider struct {
	src repoSource
	env Env
}

func (p *repoProvider) Name() string        { return p.src.name }
func (p *repoProvider) Description() string { return p.src.description }
func (p *repoProvider) License() string     { return p.src.license }

// Available is true whenever a cache directory can exist: a repository
// provider builds its index on first use, so "not indexed yet" is a state to
// pass through, not a reason to hide the source.
func (p *repoProvider) Available() bool { return p.env.LibsRoot != "" }

// repoManifest is the second half of a repository's cache: which footprint
// files exist and where. Search does not need it; Fetch does, to pair a
// symbol with the footprint it names without guessing.
type repoManifest struct {
	Built      string            `json:"built"`
	SymbolLibs []string          `json:"symbol_libs"`
	Footprints map[string]string `json:"footprints"` // bare name → repo path
	Models     map[string]string `json:"models"`     // bare name → repo path
}

func (p *repoProvider) manifestPath() string {
	return filepath.Join(parts.CachePath(p.env.LibsRoot, p.src.name), "manifest.json")
}

func (p *repoProvider) loadManifest() *repoManifest {
	data, err := os.ReadFile(p.manifestPath())
	if err != nil {
		return nil
	}
	var m repoManifest
	if json.Unmarshal(data, &m) != nil {
		return nil
	}
	return &m
}

func (p *repoProvider) saveManifest(m *repoManifest) error {
	dir := parts.CachePath(p.env.LibsRoot, p.src.name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := p.manifestPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p.manifestPath())
}

// Search ranks the cached index, building it first when it is missing.
func (p *repoProvider) Search(ctx context.Context, q Query) ([]Candidate, error) {
	if !q.wants(Symbol, Footprint) {
		return nil, nil
	}
	idx, err := p.index(ctx, false)
	if err != nil {
		return nil, err
	}
	return searchIndex(idx, q, func(e IndexEntry) Candidate {
		has := []AssetKind{Symbol}
		if e.F != "" {
			has = append(has, Footprint)
		}
		return Candidate{
			Provider:     p.src.name,
			ID:           e.L + "#" + e.N,
			MPN:          e.N,
			Manufacturer: e.M,
			Description:  e.D,
			Package:      packageFromFootprintRef(e.F),
			Has:          has,
			License:      p.src.license,
			SourceURL:    p.src.rawURL(e.L),
			Datasheet:    e.S,
		}
	}), nil
}

// Refresh rebuilds the index from the network.
func (p *repoProvider) Refresh(ctx context.Context) (*Index, error) {
	return p.index(ctx, true)
}

// index returns the cached catalogue, building it when absent or when force
// says to. The build is serialised per provider so two searches racing on a
// cold cache download the repository once, not twice.
func (p *repoProvider) index(ctx context.Context, force bool) (*Index, error) {
	mu := lockIndex(p.src.name)
	mu.Lock()
	defer mu.Unlock()

	if !force {
		idx, err := loadIndex(p.env.LibsRoot, p.src.name)
		if err == nil && idx != nil && len(idx.Entries) > 0 {
			return idx, nil
		}
	}
	return p.build(ctx)
}

func (p *repoProvider) build(ctx context.Context) (*Index, error) {
	client := p.env.Client()

	symLibs, footprints, models := p.discover(ctx)
	if len(symLibs) == 0 {
		return nil, fmt.Errorf("%s: no symbol libraries found in the repository", p.src.name)
	}

	urls := make([]string, 0, len(symLibs))
	byURL := make(map[string]string, len(symLibs))
	for _, lib := range symLibs {
		u := p.src.rawURL(lib)
		urls = append(urls, u)
		byURL[u] = lib
	}

	idx := &Index{Provider: p.src.name, Built: timestamp(), Source: p.src.homepage}
	// Six at a time: enough to hide the latency of thirty small files, few
	// enough that a public raw endpoint does not start refusing us.
	errs := getEach(ctx, client, urls, 6, func(u string, data []byte) {
		idx.Entries = append(idx.Entries, scanSymbolLib(data, byURL[u])...)
	})
	if len(idx.Entries) == 0 {
		if len(errs) > 0 {
			return nil, fmt.Errorf("%s: could not read any symbol library: %v", p.src.name, errs[0])
		}
		return nil, fmt.Errorf("%s: the repository's symbol libraries are empty", p.src.name)
	}

	if err := saveIndex(p.env.LibsRoot, idx); err != nil {
		return nil, err
	}
	if err := p.saveManifest(&repoManifest{
		Built: idx.Built, SymbolLibs: symLibs, Footprints: footprints, Models: models,
	}); err != nil {
		return nil, err
	}
	return idx, nil
}

// discover lists the repository's symbol libraries, footprints and 3D models.
// Tree discovery failing is survivable: the baked-in library list still lets
// the provider index its symbols, it just cannot pair footprints.
func (p *repoProvider) discover(ctx context.Context) (symLibs []string, footprints, models map[string]string) {
	footprints = map[string]string{}
	models = map[string]string{}

	var dirs []string
	for _, d := range []string{p.src.symbolDir, p.src.footprintDir} {
		if d != "" {
			dirs = append(dirs, strings.TrimSuffix(d, "/"))
		}
	}

	files, err := p.src.listTree(ctx, p, dirs)
	if err != nil {
		return append([]string(nil), p.src.fallbackSymbolLibs...), footprints, models
	}
	for _, f := range files {
		switch {
		case strings.HasSuffix(f, ".kicad_sym"):
			if p.src.symbolDir == "" || strings.HasPrefix(f, p.src.symbolDir) {
				symLibs = append(symLibs, f)
			}
		case strings.HasSuffix(f, ".kicad_mod"):
			if p.src.footprintDir == "" || !strings.HasPrefix(f, p.src.footprintDir) {
				continue
			}
			name := strings.TrimSuffix(path.Base(f), ".kicad_mod")
			// First writer wins, and the tree comes back sorted, so which
			// copy of a duplicated footprint we take is the same every run.
			if _, seen := footprints[name]; !seen {
				footprints[name] = f
			}
		case strings.HasSuffix(f, ".step"), strings.HasSuffix(f, ".stp"), strings.HasSuffix(f, ".wrl"):
			base := path.Base(f)
			name := strings.TrimSuffix(base, path.Ext(base))
			if _, seen := models[name]; !seen {
				models[name] = f
			}
		}
	}
	sort.Strings(symLibs)
	if len(symLibs) == 0 {
		symLibs = append(symLibs, p.src.fallbackSymbolLibs...)
	}
	return symLibs, footprints, models
}

// Fetch downloads one candidate: the symbol, flattened, plus the footprint it
// names and that footprint's 3D model when the repository carries them.
func (p *repoProvider) Fetch(ctx context.Context, id string) (*Bundle, error) {
	libPath, symName, ok := strings.Cut(id, "#")
	if !ok {
		return nil, fmt.Errorf("%s: %q is not a <library>#<symbol> id", p.src.name, id)
	}
	client := p.env.Client()

	symData, err := get(ctx, client, p.src.rawURL(libPath), nil)
	if err != nil {
		return nil, err
	}
	lib, err := symlib.Parse(symData)
	if err != nil {
		return nil, fmt.Errorf("%s: %s: %w", p.src.name, libPath, err)
	}
	flat, err := lib.Flatten(symName)
	if err != nil {
		return nil, fmt.Errorf("%s: %s: %w", p.src.name, libPath, err)
	}

	single := symlib.New()
	single.Put(flat)

	b := &Bundle{
		Candidate: Candidate{
			Provider:     p.src.name,
			ID:           id,
			MPN:          symName,
			Manufacturer: firstProperty(flat, "Manufacturer", "Manufacturer_Name", "MANUFACTURER"),
			Description:  symlib.Property(flat, "Description"),
			Has:          []AssetKind{Symbol},
			License:      p.src.license,
			SourceURL:    p.src.rawURL(libPath),
			Datasheet:    symlib.Property(flat, "Datasheet"),
		},
		Assets:       map[AssetKind][]byte{Symbol: single.Bytes()},
		FootprintRef: symlib.Property(flat, "Footprint"),
		SymbolName:   symName,
	}
	b.Package = packageFromFootprintRef(b.FootprintRef)

	p.attachFootprint(ctx, b)
	return b, nil
}

// attachFootprint resolves the symbol's footprint reference inside this
// repository. It never substitutes a similar one: an unresolved reference is
// reported so the importer can fall back to the installed KiCad libraries,
// where the footprint is a measured file rather than a guess.
func (p *repoProvider) attachFootprint(ctx context.Context, b *Bundle) {
	if b.FootprintRef == "" {
		b.Notes = append(b.Notes, "the symbol names no footprint")
		return
	}
	m := p.loadManifest()
	if m == nil || len(m.Footprints) == 0 {
		b.Notes = append(b.Notes, "this source's footprint list is not cached; the footprint will be looked up in the installed KiCad libraries")
		return
	}
	bare := b.FootprintRef
	if _, after, ok := strings.Cut(bare, ":"); ok {
		bare = after
	}
	fpPath, ok := m.Footprints[bare]
	if !ok {
		b.Notes = append(b.Notes, fmt.Sprintf("%q is not in this source; it will be looked up in the installed KiCad libraries", b.FootprintRef))
		return
	}
	data, err := get(ctx, p.env.Client(), p.src.rawURL(fpPath), nil)
	if err != nil {
		b.Notes = append(b.Notes, fmt.Sprintf("footprint %s could not be downloaded: %v", bare, err))
		return
	}
	fp, err := fplib.Parse(data)
	if err != nil {
		b.Notes = append(b.Notes, fmt.Sprintf("footprint %s does not parse: %v", bare, err))
		return
	}
	b.Assets[Footprint] = data
	b.Has = append(b.Has, Footprint)

	// A 3D model only comes along when the footprint asks for one by a name
	// this repository actually publishes.
	if model := fp.Model(); model != "" && len(m.Models) > 0 {
		base := path.Base(strings.ReplaceAll(model, "\\", "/"))
		key := strings.TrimSuffix(base, path.Ext(base))
		if mp, ok := m.Models[key]; ok {
			if data, err := get(ctx, p.env.Client(), p.src.rawURL(mp), nil); err == nil {
				b.Assets[Model3D] = data
				b.Has = append(b.Has, Model3D)
				b.Model3DExt = path.Ext(mp)
			}
		}
	}
}

// scanSymbolLib turns one downloaded .kicad_sym into index entries. Parsing
// rather than scanning lines: this repository does not read KiCad files with
// string matching, and a symbol whose description spans two lines would be
// half-indexed if it did.
func scanSymbolLib(data []byte, libPath string) []IndexEntry {
	lib, err := symlib.Parse(data)
	if err != nil {
		return nil
	}
	var out []IndexEntry
	for _, sym := range lib.Symbols() {
		name := sexp.StringValue(sym, 1)
		if name == "" {
			continue
		}
		e := IndexEntry{
			N: name,
			D: symlib.Property(sym, "Description"),
			L: libPath,
			F: symlib.Property(sym, "Footprint"),
			M: firstProperty(sym, "Manufacturer", "Manufacturer_Name", "MANUFACTURER"),
			S: symlib.Property(sym, "Datasheet"),
			K: strings.TrimSpace(strings.Join(nonEmpty(
				symlib.Property(sym, "ki_keywords"),
				firstProperty(sym, "LCSC", "LCSC Part #", "LCSC_Part"),
				firstProperty(sym, "MPN", "Part", "Manufacturer_Part_Number"),
			), " ")),
		}
		out = append(out, e)
	}
	return out
}

func firstProperty(sym *sexp.Node, keys ...string) string {
	for _, k := range keys {
		if v := symlib.Property(sym, k); v != "" {
			return v
		}
	}
	return ""
}

func nonEmpty(vals ...string) []string {
	var out []string
	for _, v := range vals {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// packageFromFootprintRef reads the package name out of "Lib:SOIC-8_3.9x4.9mm".
// It is a label for the human reading the candidate list, never something the
// importer acts on.
func packageFromFootprintRef(ref string) string {
	if ref == "" {
		return ""
	}
	if _, after, ok := strings.Cut(ref, ":"); ok {
		return after
	}
	return ref
}

// --- tree listing, one function per forge ---

// githubTree lists a GitHub repository through the trees API, which returns
// the whole thing in one request — dirs is not needed.
func githubTree(owner, repo, branch string) func(context.Context, *repoProvider, []string) ([]string, error) {
	return func(ctx context.Context, p *repoProvider, _ []string) ([]string, error) {
		u := fmt.Sprintf("https://api.github.com/repos/%s/%s/git/trees/%s?recursive=1", owner, repo, branch)
		data, err := get(ctx, p.env.Client(), u, map[string]string{"Accept": "application/vnd.github+json"})
		if err != nil {
			return nil, err
		}
		var payload struct {
			Tree []struct {
				Path string `json:"path"`
				Type string `json:"type"`
			} `json:"tree"`
			Truncated bool `json:"truncated"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			return nil, err
		}
		var out []string
		for _, t := range payload.Tree {
			if t.Type == "blob" {
				out = append(out, t.Path)
			}
		}
		sort.Strings(out)
		if payload.Truncated && len(out) == 0 {
			return nil, fmt.Errorf("github tree for %s/%s came back truncated and empty", owner, repo)
		}
		return out, nil
	}
}

// gitlabTree lists a GitLab repository directory by directory.
//
// GitLab pages its tree endpoint 100 entries at a time and returns them in
// path order. Walking a whole repository that way is how the CERN provider
// first failed to find a single symbol: PcbLib holds thousands of footprints
// and sorts before SchLib, so the paging budget ran out long before the
// symbols. Asking for the directories we actually want costs one request each.
func gitlabTree(project, branch string) func(context.Context, *repoProvider, []string) ([]string, error) {
	return func(ctx context.Context, p *repoProvider, dirs []string) ([]string, error) {
		if len(dirs) == 0 {
			dirs = []string{""}
		}
		var out []string
		for _, dir := range dirs {
			for page := 1; page <= 100; page++ {
				u := fmt.Sprintf(
					"https://gitlab.com/api/v4/projects/%s/repository/tree?recursive=true&per_page=100&page=%d&ref=%s",
					url.PathEscape(project), page, url.QueryEscape(branch))
				if dir != "" {
					u += "&path=" + url.QueryEscape(dir)
				}
				data, err := get(ctx, p.env.Client(), u, nil)
				if err != nil {
					return nil, err
				}
				var payload []struct {
					Path string `json:"path"`
					Type string `json:"type"`
				}
				if err := json.Unmarshal(data, &payload); err != nil {
					return nil, err
				}
				for _, t := range payload {
					if t.Type == "blob" {
						out = append(out, t.Path)
					}
				}
				if len(payload) < 100 {
					break
				}
			}
		}
		sort.Strings(out)
		return out, nil
	}
}

// escapePath percent-encodes each segment of a repository path. CERN's library
// files are called things like "SchLib/Analog & Interface.kicad_sym"; handing
// that to a raw endpoint unescaped fetches nothing.
func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

func githubRaw(owner, repo, branch string) func(string) string {
	return func(p string) string {
		return fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s", owner, repo, branch, escapePath(p))
	}
}

func gitlabRaw(project, branch string) func(string) string {
	return func(p string) string {
		return fmt.Sprintf("https://gitlab.com/%s/-/raw/%s/%s", project, branch, escapePath(p))
	}
}
