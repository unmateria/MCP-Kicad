package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"mcp-kicad/internal/parts"
)

// IndexEntry is one searchable part in a provider's cached index. The field
// names are short because there are tens of thousands of these on disk.
type IndexEntry struct {
	N string `json:"n"`           // part name as it appears in the source
	D string `json:"d,omitempty"` // description
	L string `json:"l"`           // library path inside the source
	F string `json:"f,omitempty"` // footprint reference the symbol asks for
	M string `json:"m,omitempty"` // manufacturer, when the source states one
	S string `json:"s,omitempty"` // datasheet URL
	K string `json:"k,omitempty"` // extra keywords (LCSC code, package…)
}

// Index is a provider's offline catalogue. Search reads it; only a refresh
// touches the network. A search that went to the network every time would be
// unusable over a catalogue of seventeen thousand parts.
type Index struct {
	Provider string       `json:"provider"`
	Built    string       `json:"built"`
	Source   string       `json:"source"`
	Entries  []IndexEntry `json:"entries"`
}

// indexPath is where a provider's index lives.
func indexPath(libsRoot, provider string) string {
	return filepath.Join(parts.CachePath(libsRoot, provider), "index.json")
}

// loadIndex reads a cached index. A missing file is not an error; it means the
// index has not been built yet.
func loadIndex(libsRoot, provider string) (*Index, error) {
	data, err := os.ReadFile(indexPath(libsRoot, provider))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var idx Index
	if err := json.Unmarshal(data, &idx); err != nil {
		// A truncated cache is not worth a hard failure: rebuilding is cheap
		// and the alternative is a permanently broken provider.
		return nil, nil
	}
	return &idx, nil
}

// saveIndex writes an index atomically, so an interrupted refresh leaves the
// previous catalogue intact instead of a half-written one.
func saveIndex(libsRoot string, idx *Index) error {
	dir := parts.CachePath(libsRoot, idx.Provider)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	sort.Slice(idx.Entries, func(i, j int) bool {
		if idx.Entries[i].N != idx.Entries[j].N {
			return idx.Entries[i].N < idx.Entries[j].N
		}
		return idx.Entries[i].L < idx.Entries[j].L
	})
	data, err := json.Marshal(idx)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "index.json.tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, indexPath(libsRoot, idx.Provider))
}

// timestamp is the build time recorded in an index.
func timestamp() string { return time.Now().UTC().Format(time.RFC3339) }

// indexAge returns how old a built index is, and whether it was built at all.
func indexAge(idx *Index) (time.Duration, bool) {
	if idx == nil || idx.Built == "" {
		return 0, false
	}
	t, err := time.Parse(time.RFC3339, idx.Built)
	if err != nil {
		return 0, false
	}
	return time.Since(t), true
}

// indexLocks serialises index builds per provider within one process, so two
// concurrent searches do not download the same thirty libraries twice.
var indexLocks sync.Map

func lockIndex(provider string) *sync.Mutex {
	v, _ := indexLocks.LoadOrStore(provider, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// searchIndex ranks a query against an index and turns the hits into
// candidates. The provider fills in what only it knows via decorate.
func searchIndex(idx *Index, q Query, decorate func(IndexEntry) Candidate) []Candidate {
	if idx == nil {
		return nil
	}
	type scored struct {
		c Candidate
		e IndexEntry
	}
	var hits []scored
	for _, e := range idx.Entries {
		s := Score(q.Text, e.N, e.D, e.K+" "+e.M)
		if s == 0 {
			continue
		}
		if q.Manufacturer != "" && e.M != "" &&
			!strings.Contains(strings.ToLower(e.M), strings.ToLower(q.Manufacturer)) {
			continue
		}
		c := decorate(e)
		c.Score = s
		hits = append(hits, scored{c: c, e: e})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].c.Score != hits[j].c.Score {
			return hits[i].c.Score > hits[j].c.Score
		}
		return hits[i].c.ID < hits[j].c.ID
	})
	limit := q.limit()
	out := make([]Candidate, 0, limit)
	for _, h := range hits {
		if len(out) >= limit {
			break
		}
		out = append(out, h.c)
	}
	return out
}

// IndexStatus reports what a provider has cached, for the tool that tells the
// user why a search came back empty.
type IndexStatus struct {
	Provider string
	Built    bool
	Entries  int
	Age      time.Duration
}

// Status reads the cached index metadata for one provider without building it.
func Status(libsRoot, provider string) IndexStatus {
	idx, err := loadIndex(libsRoot, provider)
	if err != nil || idx == nil {
		return IndexStatus{Provider: provider}
	}
	age, ok := indexAge(idx)
	return IndexStatus{Provider: provider, Built: ok, Entries: len(idx.Entries), Age: age}
}

// Refresher is implemented by providers that keep a cached catalogue. The
// interface is optional: a provider that queries a live API every time has
// nothing to refresh, and asking it to would be a no-op with a progress bar.
type Refresher interface {
	Refresh(ctx context.Context) (*Index, error)
}

// Refresh rebuilds the cached catalogue of the named providers (all of them
// when names is empty), returning what each ended up with.
func Refresh(ctx context.Context, env Env, names []string) ([]IndexStatus, []error) {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	var out []IndexStatus
	var errs []error
	for _, p := range All(env) {
		if len(want) > 0 && !want[p.Name()] {
			continue
		}
		r, isRefresher := p.(Refresher)
		if !isRefresher || !p.Available() {
			continue
		}
		if _, err := r.Refresh(ctx); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p.Name(), err))
			continue
		}
		out = append(out, Status(env.LibsRoot, p.Name()))
	}
	return out, errs
}
