// Package providers finds component data in external sources and brings back
// the bytes. It does not write to disk, does not touch KiCad's libraries and
// does not know MCP exists — installing is internal/parts/importer's job, and
// keeping the two apart is what lets the same code back a KiCad GUI plugin
// later without dragging the MCP SDK along.
package providers

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// AssetKind names one file an imported part can be made of.
type AssetKind string

const (
	Symbol    AssetKind = "symbol"
	Footprint AssetKind = "footprint"
	Model3D   AssetKind = "model3d"
	Spice     AssetKind = "spice"
)

// Query is a component search.
type Query struct {
	// Text is what the user typed: an MPN, an LCSC number, or free words.
	Text string
	// Manufacturer narrows the result when the same MPN exists twice.
	Manufacturer string
	// Need lists the assets the caller cannot do without. A provider that
	// carries none of them is skipped rather than asked.
	Need []AssetKind
	// Limit caps the results per provider. Zero means DefaultLimit.
	Limit int
}

// DefaultLimit is how many hits one provider contributes when Query says
// nothing: enough to choose from, few enough to read.
const DefaultLimit = 8

func (q Query) limit() int {
	if q.Limit > 0 {
		return q.Limit
	}
	return DefaultLimit
}

// wants reports whether the caller asked for at least one of have.
func (q Query) wants(have ...AssetKind) bool {
	if len(q.Need) == 0 {
		return true
	}
	for _, n := range q.Need {
		for _, h := range have {
			if n == h {
				return true
			}
		}
	}
	return false
}

// Candidate is one part a provider can supply. It is returned to the model and
// handed back verbatim to import it, so every field has to survive a JSON round
// trip — which is why ID is an opaque provider-scoped string rather than a
// pointer into the provider's internals.
type Candidate struct {
	Provider     string      `json:"provider"`
	ID           string      `json:"id"`
	MPN          string      `json:"mpn"`
	Manufacturer string      `json:"manufacturer,omitempty"`
	Description  string      `json:"description,omitempty"`
	Package      string      `json:"package,omitempty"`
	Has          []AssetKind `json:"has"`
	License      string      `json:"license,omitempty"`
	SourceURL    string      `json:"source_url,omitempty"`
	Datasheet    string      `json:"datasheet,omitempty"`
	// Score ranks candidates across providers. Higher is better.
	Score int `json:"-"`
	// Installed marks a part already usable without importing anything.
	Installed bool `json:"installed,omitempty"`
	// LibID is set when Installed: the ready-to-use "Lib:Part".
	LibID string `json:"lib_id,omitempty"`
	// MetadataOnly marks a catalogue entry: it identifies a part but carries
	// no files. Distributors are all of this kind — none of them serves CAD.
	MetadataOnly bool `json:"metadata_only,omitempty"`
}

// Ref is the "provider:id" string the model passes to import_part.
func (c Candidate) Ref() string { return c.Provider + ":" + c.ID }

// ParseRef splits a "provider:id" reference. IDs may contain colons, so only
// the first one separates.
func ParseRef(ref string) (provider, id string, err error) {
	provider, id, ok := strings.Cut(ref, ":")
	if !ok || provider == "" || id == "" {
		return "", "", fmt.Errorf("providers: %q is not a provider:id reference", ref)
	}
	return provider, id, nil
}

// Bundle is everything one candidate is made of, in memory. Writing it out is
// the importer's decision, taken only after verification passes.
type Bundle struct {
	Candidate
	Assets map[AssetKind][]byte
	// FootprintRef is what the symbol asks for, verbatim ("Lib:Name"). The
	// importer resolves it — inside the provider's own footprints first, then
	// against the installed KiCad libraries. It is never invented.
	FootprintRef string
	// SymbolName is the name the symbol carries inside the .kicad_sym we
	// fetched, which is rarely the name it will be installed under.
	SymbolName string
	// Model3DExt is the extension the 3D asset must keep (".step", ".wrl"):
	// KiCad picks its loader from the file name, not from the content.
	Model3DExt string
	// Notes carry anything the provider wants reported but could not encode
	// as an asset: "footprint not paired in this source", for instance.
	Notes []string
}

// Provider is one source of component data.
type Provider interface {
	// Name is the stable identifier used in references and tool arguments.
	Name() string
	// Description is one line explaining what this source carries.
	Description() string
	// License is the licence its content is published under, or "" when it
	// varies per part and has to be read from the part itself.
	License() string
	// Available reports whether the provider can run at all: a configured API
	// key, a reachable directory. A provider that merely needs to build its
	// index on first use is still available.
	Available() bool
	// Search returns candidates ranked by the provider's own idea of
	// relevance. It may hit the network to build a cached index.
	Search(ctx context.Context, q Query) ([]Candidate, error)
	// Fetch brings back the bytes for one candidate ID.
	Fetch(ctx context.Context, id string) (*Bundle, error)
}

// Env is everything a provider needs from the server to do its job.
type Env struct {
	LibsRoot        string
	KicadSymbols    string
	KicadFootprints string
	HTTP            *http.Client
	Mouser          string
	DigiKeyID       string
	DigiKeySecret   string
}

// Client returns the HTTP client to use, defaulting to one with a timeout —
// a provider that hangs would hang the whole search.
func (e Env) Client() *http.Client {
	if e.HTTP != nil {
		return e.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

// Factory builds a provider for one server environment.
type Factory func(Env) Provider

var (
	registryMu sync.Mutex
	registry   []Factory
)

// Register adds a provider factory. Providers register from init(), the same
// pattern as place2/cluster/canonical, so adding a source is one new file.
func Register(f Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = append(registry, f)
}

// All builds every registered provider, sorted by name. Sorted, because the
// order results come back in is part of the answer and a map-order search
// would rank differently on every run.
func All(env Env) []Provider {
	registryMu.Lock()
	factories := append([]Factory(nil), registry...)
	registryMu.Unlock()

	out := make([]Provider, 0, len(factories))
	for _, f := range factories {
		if p := f(env); p != nil {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// Get returns the named provider.
func Get(env Env, name string) (Provider, error) {
	for _, p := range All(env) {
		if p.Name() == name {
			return p, nil
		}
	}
	return nil, fmt.Errorf("providers: no source named %q", name)
}

// SearchAll queries every available provider concurrently and merges the
// results, best first. A provider that fails contributes an error rather than
// sinking the whole search: one unreachable repository must not make a part
// that IS installed locally unfindable.
func SearchAll(ctx context.Context, env Env, q Query, only []string) ([]Candidate, []error) {
	provs := All(env)
	if len(only) > 0 {
		keep := map[string]bool{}
		for _, n := range only {
			keep[n] = true
		}
		var filtered []Provider
		for _, p := range provs {
			if keep[p.Name()] {
				filtered = append(filtered, p)
			}
		}
		provs = filtered
	}

	type result struct {
		cands []Candidate
		err   error
	}
	results := make([]result, len(provs))
	var wg sync.WaitGroup
	for i, p := range provs {
		if !p.Available() {
			continue
		}
		wg.Add(1)
		go func(i int, p Provider) {
			defer wg.Done()
			cands, err := p.Search(ctx, q)
			if err != nil {
				results[i] = result{err: fmt.Errorf("%s: %w", p.Name(), err)}
				return
			}
			results[i] = result{cands: cands}
		}(i, p)
	}
	wg.Wait()

	var all []Candidate
	var errs []error
	for _, r := range results {
		if r.err != nil {
			errs = append(errs, r.err)
			continue
		}
		all = append(all, r.cands...)
	}
	sort.SliceStable(all, func(i, j int) bool {
		// A catalogue entry never outranks something that carries files, no
		// matter how well its text matched: the question find_part answers is
		// "what can I install", and an exact-name distributor hit at the top
		// of the list is an answer nobody can act on.
		if all[i].MetadataOnly != all[j].MetadataOnly {
			return all[j].MetadataOnly
		}
		if all[i].Score != all[j].Score {
			return all[i].Score > all[j].Score
		}
		if all[i].Installed != all[j].Installed {
			return all[i].Installed // already on disk beats a download
		}
		if all[i].Provider != all[j].Provider {
			return all[i].Provider < all[j].Provider
		}
		return all[i].ID < all[j].ID
	})
	return all, errs
}
