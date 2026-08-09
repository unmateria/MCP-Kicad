package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

func init() { Register(func(env Env) Provider { return &mouserProvider{env: env} }) }

// mouserProvider resolves a part number against Mouser's catalogue.
//
// It carries no CAD data and never will: no distributor serves .kicad_sym or
// .kicad_mod files. What it is for is the step before the search — turning
// "the number printed on this reel" or a half-remembered order code into a
// real manufacturer part number, package and datasheet, which the library
// sources can then be searched for.
//
// Candidates from here are marked metadata-only and import_part refuses them,
// with the MPN to search for instead.
type mouserProvider struct {
	env     Env
	baseURL string // overridden in tests
}

func (p *mouserProvider) Name() string { return "mouser" }

func (p *mouserProvider) Description() string {
	return "Mouser catalogue — identification only: real MPN, manufacturer, package, datasheet (no CAD files)"
}

func (p *mouserProvider) License() string { return "catalogue metadata" }

func (p *mouserProvider) Available() bool { return p.env.Mouser != "" }

func (p *mouserProvider) base() string {
	if p.baseURL != "" {
		return p.baseURL
	}
	return "https://api.mouser.com/api/v2"
}

// mouserRequest is the body the keyword endpoint wants. The field names are
// Mouser's and are case-sensitive.
type mouserRequest struct {
	Search struct {
		Keyword          string `json:"keyword"`
		ManufacturerName string `json:"manufacturerName,omitempty"`
		Records          int    `json:"records,omitempty"`
		PageNumber       int    `json:"pageNumber,omitempty"`
		SearchOptions    string `json:"searchOptions,omitempty"`
	} `json:"SearchByKeywordMfrNameRequest"`
}

type mouserResponse struct {
	Errors []struct {
		Message string `json:"Message"`
	} `json:"Errors"`
	SearchResults struct {
		NumberOfResult int `json:"NumberOfResult"`
		Parts          []struct {
			ManufacturerPartNumber string `json:"ManufacturerPartNumber"`
			Manufacturer           string `json:"Manufacturer"`
			Description            string `json:"Description"`
			DataSheetUrl           string `json:"DataSheetUrl"`
			Category               string `json:"Category"`
			Availability           string `json:"Availability"`
			MouserPartNumber       string `json:"MouserPartNumber"`
			LifecycleStatus        string `json:"LifecycleStatus"`
			ProductAttributes      []struct {
				AttributeName  string `json:"AttributeName"`
				AttributeValue string `json:"AttributeValue"`
			} `json:"ProductAttributes"`
		} `json:"Parts"`
	} `json:"SearchResults"`
}

func (p *mouserProvider) Search(ctx context.Context, q Query) ([]Candidate, error) {
	// A caller that asked for a symbol or a footprint is not served here, and
	// filling its list with rows it cannot import would be noise.
	if len(q.Need) > 0 && !q.wants(Spice) {
		return nil, nil
	}
	if !p.Available() || strings.TrimSpace(q.Text) == "" {
		return nil, nil
	}

	var body mouserRequest
	body.Search.Keyword = q.Text
	body.Search.ManufacturerName = q.Manufacturer
	body.Search.Records = q.limit()

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	url := fmt.Sprintf("%s/search/keywordandmanufacturer?apiKey=%s", p.base(), p.env.Mouser)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := p.env.Client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("mouser: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mouser: HTTP %d", resp.StatusCode)
	}

	var parsed mouserResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("mouser: %w", err)
	}
	// Mouser answers 200 with an Errors array when the key is wrong, so the
	// status code alone does not mean the search worked.
	for _, e := range parsed.Errors {
		if e.Message != "" {
			return nil, fmt.Errorf("mouser: %s", e.Message)
		}
	}

	out := make([]Candidate, 0, len(parsed.SearchResults.Parts))
	for _, part := range parsed.SearchResults.Parts {
		if part.ManufacturerPartNumber == "" {
			continue
		}
		pkg := ""
		for _, a := range part.ProductAttributes {
			if strings.EqualFold(a.AttributeName, "Package / Case") ||
				strings.EqualFold(a.AttributeName, "Packaging") {
				pkg = a.AttributeValue
				break
			}
		}
		desc := part.Description
		if part.Availability != "" {
			desc = strings.TrimSpace(desc + "  [" + part.Availability + "]")
		}
		out = append(out, Candidate{
			Provider:     p.Name(),
			ID:           part.MouserPartNumber,
			MPN:          part.ManufacturerPartNumber,
			Manufacturer: part.Manufacturer,
			Description:  desc,
			Package:      pkg,
			Has:          nil, // metadata only
			License:      p.License(),
			SourceURL:    part.DataSheetUrl,
			Datasheet:    part.DataSheetUrl,
			MetadataOnly: true,
			Score:        Score(q.Text, part.ManufacturerPartNumber, part.Description, part.Category),
		})
	}
	return out, nil
}

func (p *mouserProvider) Fetch(_ context.Context, id string) (*Bundle, error) {
	return nil, metadataOnlyError(p.Name(), id)
}

// metadataOnlyError is what a distributor says when asked for files it does
// not have. It names the next step rather than just refusing.
func metadataOnlyError(provider, id string) error {
	return fmt.Errorf(
		"%s is a catalogue, not a CAD library: %q has no symbol or footprint to install. "+
			"Take the manufacturer part number it reported and run find_part again with that — "+
			"the library sources (jlcpcb, cern, digikey-lib, the manufacturer repos, lcsc) are "+
			"where the files are",
		provider, id)
}
