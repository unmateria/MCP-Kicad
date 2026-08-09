package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

func init() { Register(func(env Env) Provider { return &digikeyProvider{env: env} }) }

// digikeyProvider resolves a part number against Digi-Key's catalogue.
//
// Identification only, like Mouser: Digi-Key serves metadata, never CAD files.
// (The `digikey-lib` provider is a different thing entirely — a community Git
// repository of KiCad libraries that happens to be named after them.)
//
// The wire format below comes from the official Product Information v4 OpenAPI
// document, not from memory or from blog posts. That distinction cost real
// time: v3's `ManufacturerPartNumber`, `PrimaryDatasheet` and `ProductCount`
// were all renamed in v4, and several third-party clients still publish the
// old names. Unlike every other source here, this client could NOT be measured
// against the live endpoint — that needs credentials this repository does not
// have — so the shapes are pinned by tests rather than by observation.
type digikeyProvider struct {
	env     Env
	baseURL string // overridden in tests

	mu      sync.Mutex
	token   string
	expires time.Time
}

func (p *digikeyProvider) Name() string { return "digikey" }

func (p *digikeyProvider) Description() string {
	return "Digi-Key catalogue — identification only: real MPN, manufacturer, package, datasheet (no CAD files)"
}

func (p *digikeyProvider) License() string { return "catalogue metadata" }

func (p *digikeyProvider) Available() bool {
	return p.env.DigiKeyID != "" && p.env.DigiKeySecret != ""
}

func (p *digikeyProvider) base() string {
	if p.baseURL != "" {
		return p.baseURL
	}
	return "https://api.digikey.com"
}

// accessToken returns a cached OAuth2 token, fetching one when it is missing
// or about to expire. The documented lifetime is ten minutes, so a client that
// asked for a new token per search would spend half its rate limit on auth.
func (p *digikeyProvider) accessToken(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.token != "" && time.Now().Before(p.expires) {
		return p.token, nil
	}

	form := url.Values{
		"client_id":     {p.env.DigiKeyID},
		"client_secret": {p.env.DigiKeySecret},
		"grant_type":    {"client_credentials"},
	}
	// Form-encoded, not JSON. The endpoint rejects a JSON body.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.base()+"/v1/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := p.env.Client().Do(req)
	if err != nil {
		return "", fmt.Errorf("digikey: token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("digikey: token: HTTP %d (check digikey_client_id and digikey_client_secret)", resp.StatusCode)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		TokenType   string `json:"token_type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", fmt.Errorf("digikey: token: %w", err)
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("digikey: token response carried no access_token")
	}
	// Read the lifetime rather than assuming it: the documentation's own
	// example is 599 seconds, other sources claim half an hour. A minute of
	// margin covers the round trip.
	ttl := time.Duration(tok.ExpiresIn) * time.Second
	if ttl <= time.Minute {
		ttl = 5 * time.Minute
	}
	p.token = tok.AccessToken
	p.expires = time.Now().Add(ttl - time.Minute)
	return p.token, nil
}

// digikeyRequest is the v4 KeywordSearch body. The schema sets
// additionalProperties:false, so an unknown field is a 400 rather than being
// ignored — every name here is from the spec.
type digikeyRequest struct {
	Keywords string `json:"Keywords"`
	Limit    int    `json:"Limit,omitempty"`
	Offset   int    `json:"Offset,omitempty"`
}

// digikeyProduct is the subset of v4's Product this provider reads. Note what
// is NOT here: v4 has no DigiKeyProductNumber at the top level — it lives
// inside ProductVariations — and the manufacturer and description are objects,
// not strings.
type digikeyProduct struct {
	ManufacturerProductNumber string `json:"ManufacturerProductNumber"`
	Manufacturer              struct {
		Name string `json:"Name"`
	} `json:"Manufacturer"`
	Description struct {
		ProductDescription  string `json:"ProductDescription"`
		DetailedDescription string `json:"DetailedDescription"`
	} `json:"Description"`
	DatasheetUrl      string `json:"DatasheetUrl"`
	ProductUrl        string `json:"ProductUrl"`
	QuantityAvailable int64  `json:"QuantityAvailable"`
	Parameters        []struct {
		ParameterText string `json:"ParameterText"`
		ValueText     string `json:"ValueText"`
	} `json:"Parameters"`
	ProductVariations []struct {
		DigiKeyProductNumber string `json:"DigiKeyProductNumber"`
	} `json:"ProductVariations"`
}

type digikeyResponse struct {
	Products      []digikeyProduct `json:"Products"`
	ExactMatches  []digikeyProduct `json:"ExactMatches"`
	ProductsCount int              `json:"ProductsCount"`
}

// digikeyError is v4's problem document, whose fields are lower-case while
// everything else in the API is upper-case.
type digikeyError struct {
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Status int    `json:"status"`
}

func (p *digikeyProvider) Search(ctx context.Context, q Query) ([]Candidate, error) {
	if len(q.Need) > 0 && !q.wants(Spice) {
		return nil, nil
	}
	if !p.Available() || strings.TrimSpace(q.Text) == "" {
		return nil, nil
	}
	token, err := p.accessToken(ctx)
	if err != nil {
		return nil, err
	}

	limit := q.limit()
	if limit > 50 {
		limit = 50 // the schema caps Limit at 50
	}
	payload, err := json.Marshal(digikeyRequest{Keywords: q.Text, Limit: limit})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.base()+"/products/v4/search/keyword", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-DIGIKEY-Client-Id", p.env.DigiKeyID)
	req.Header.Set("X-DIGIKEY-Locale-Site", "US")
	req.Header.Set("X-DIGIKEY-Locale-Language", "en")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := p.env.Client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("digikey: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e digikeyError
		if json.NewDecoder(resp.Body).Decode(&e) == nil && e.Title != "" {
			return nil, fmt.Errorf("digikey: HTTP %d: %s %s", resp.StatusCode, e.Title, e.Detail)
		}
		return nil, fmt.Errorf("digikey: HTTP %d", resp.StatusCode)
	}
	var parsed digikeyResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("digikey: %w", err)
	}

	// Exact matches come back in their own array and are the better answer to
	// "what part is this", which is the only question this provider is for.
	products := append(append([]digikeyProduct{}, parsed.ExactMatches...), parsed.Products...)
	seen := map[string]bool{}
	out := make([]Candidate, 0, len(products))
	for _, prod := range products {
		mpn := prod.ManufacturerProductNumber
		if mpn == "" || seen[mpn] {
			continue
		}
		seen[mpn] = true

		pkg := ""
		for _, param := range prod.Parameters {
			if strings.EqualFold(param.ParameterText, "Package / Case") {
				pkg = param.ValueText
				break
			}
		}
		desc := prod.Description.ProductDescription
		if prod.Description.DetailedDescription != "" {
			desc = prod.Description.DetailedDescription
		}
		if prod.QuantityAvailable > 0 {
			desc = fmt.Sprintf("%s  [%d in stock]", desc, prod.QuantityAvailable)
		}
		id := mpn
		if len(prod.ProductVariations) > 0 && prod.ProductVariations[0].DigiKeyProductNumber != "" {
			id = prod.ProductVariations[0].DigiKeyProductNumber
		}

		out = append(out, Candidate{
			Provider:     p.Name(),
			ID:           id,
			MPN:          mpn,
			Manufacturer: prod.Manufacturer.Name,
			Description:  desc,
			Package:      pkg,
			Has:          nil, // metadata only
			License:      p.License(),
			SourceURL:    prod.ProductUrl,
			Datasheet:    prod.DatasheetUrl,
			MetadataOnly: true,
			Score:        Score(q.Text, mpn, desc, prod.Manufacturer.Name),
		})
		if len(out) >= q.limit() {
			break
		}
	}
	return out, nil
}

func (p *digikeyProvider) Fetch(_ context.Context, id string) (*Bundle, error) {
	return nil, metadataOnlyError(p.Name(), id)
}
