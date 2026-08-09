package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The Mouser response below is shaped from Mouser's published Search API v2
// documentation. Unlike every other source in this package, the distributor
// clients could NOT be measured against the live endpoint — that needs an API
// key, and this repository has none. The tests therefore pin the wire format
// we believe in, so a mismatch surfaces as a failing test rather than as an
// empty search.
const mouserBody = `{
  "Errors": [],
  "SearchResults": {
    "NumberOfResult": 1,
    "Parts": [
      {
        "ManufacturerPartNumber": "NE555PWR",
        "Manufacturer": "Texas Instruments",
        "Description": "Timers & Support Products Precision Timer",
        "DataSheetUrl": "https://www.ti.com/lit/ds/symlink/ne555.pdf",
        "Category": "Timers & Support Products",
        "Availability": "1500 In Stock",
        "MouserPartNumber": "595-NE555PWR",
        "LifecycleStatus": "Active",
        "ProductAttributes": [
          {"AttributeName": "Package / Case", "AttributeValue": "TSSOP-8"}
        ]
      }
    ]
  }
}`

func TestMouserSearch(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(mouserBody))
	}))
	defer srv.Close()

	p := &mouserProvider{env: Env{Mouser: "KEY", HTTP: srv.Client()}, baseURL: srv.URL}
	if !p.Available() {
		t.Fatal("a configured key must make the provider available")
	}
	got, err := p.Search(context.Background(), Query{Text: "NE555"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected one candidate, got %+v", got)
	}
	c := got[0]
	if c.MPN != "NE555PWR" || c.Manufacturer != "Texas Instruments" {
		t.Errorf("candidate = %+v", c)
	}
	if c.Package != "TSSOP-8" {
		t.Errorf("package = %q, want it read from the Package / Case attribute", c.Package)
	}
	if !c.MetadataOnly || len(c.Has) != 0 {
		t.Error("a distributor row must be marked metadata-only and offer no assets")
	}
	if !strings.Contains(c.Description, "1500 In Stock") {
		t.Errorf("stock should be visible in the description, got %q", c.Description)
	}

	if !strings.Contains(gotPath, "apiKey=KEY") {
		t.Errorf("the key goes in the query string, got %q", gotPath)
	}
	var sent map[string]map[string]any
	if err := json.Unmarshal([]byte(gotBody), &sent); err != nil {
		t.Fatalf("request body is not the JSON Mouser expects: %v (%s)", err, gotBody)
	}
	req, ok := sent["SearchByKeywordMfrNameRequest"]
	if !ok {
		t.Fatalf("body must be wrapped in SearchByKeywordMfrNameRequest, got %s", gotBody)
	}
	if req["keyword"] != "NE555" {
		t.Errorf("keyword = %v", req["keyword"])
	}
}

// Mouser answers 200 with a populated Errors array when the key is rejected.
// Trusting the status code alone turns a bad key into a silent empty result.
func TestMouserReportsErrorsInsideA200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"Errors":[{"Message":"Invalid unique identifier."}],"SearchResults":null}`))
	}))
	defer srv.Close()
	p := &mouserProvider{env: Env{Mouser: "BAD", HTTP: srv.Client()}, baseURL: srv.URL}
	_, err := p.Search(context.Background(), Query{Text: "NE555"})
	if err == nil || !strings.Contains(err.Error(), "Invalid unique identifier") {
		t.Errorf("expected the API's own message to surface, got %v", err)
	}
}

func TestMouserUnconfiguredIsUnavailable(t *testing.T) {
	p := &mouserProvider{env: Env{}}
	if p.Available() {
		t.Error("no key means not available")
	}
	got, err := p.Search(context.Background(), Query{Text: "NE555"})
	if err != nil || len(got) != 0 {
		t.Errorf("an unconfigured provider must return nothing quietly, got %v %v", got, err)
	}
}

// The v4 response below is shaped from Digi-Key's official ProductSearch v4
// OpenAPI document. The names matter more than usual: v3 called these
// ManufacturerPartNumber, PrimaryDatasheet and ProductCount, several
// third-party clients still publish the old ones, and a wrong name here
// produces an empty field rather than an error.
const digikeyBody = `{
  "ProductsCount": 1,
  "ExactMatches": [
    {
      "ManufacturerProductNumber": "NE5532P",
      "Manufacturer": { "Id": 296, "Name": "Texas Instruments" },
      "Description": {
        "ProductDescription": "IC OPAMP AUDIO 2 CIRCUIT 8DIP",
        "DetailedDescription": "Dual low-noise audio operational amplifier"
      },
      "DatasheetUrl": "https://www.ti.com/lit/ds/symlink/ne5532.pdf",
      "ProductUrl": "https://www.digikey.com/en/products/detail/NE5532P/296-1373-5-ND",
      "QuantityAvailable": 4210,
      "Parameters": [
        { "ParameterText": "Package / Case", "ValueText": "8-DIP (0.300\", 7.62mm)" }
      ],
      "ProductVariations": [ { "DigiKeyProductNumber": "296-1373-5-ND" } ]
    }
  ],
  "Products": []
}`

func TestDigiKeySearch(t *testing.T) {
	var tokenCalls int
	var gotHeaders http.Header
	var gotBody string
	var gotTokenForm string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/oauth2/token":
			tokenCalls++
			b, _ := io.ReadAll(r.Body)
			gotTokenForm = string(b)
			if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
				t.Errorf("token request Content-Type = %q, the endpoint rejects JSON", ct)
			}
			_, _ = w.Write([]byte(`{"access_token":"TOK","expires_in":599,"token_type":"Bearer"}`))
		case "/products/v4/search/keyword":
			gotHeaders = r.Header.Clone()
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
			_, _ = w.Write([]byte(digikeyBody))
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := &digikeyProvider{
		env:     Env{DigiKeyID: "ID", DigiKeySecret: "SECRET", HTTP: srv.Client()},
		baseURL: srv.URL,
	}
	got, err := p.Search(context.Background(), Query{Text: "NE5532"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected one candidate, got %+v", got)
	}
	c := got[0]
	if c.MPN != "NE5532P" {
		t.Errorf("MPN = %q — v4 calls this ManufacturerProductNumber, not ManufacturerPartNumber", c.MPN)
	}
	if c.Manufacturer != "Texas Instruments" {
		t.Errorf("manufacturer = %q — v4 nests it as {Id, Name}", c.Manufacturer)
	}
	if !strings.Contains(c.Description, "Dual low-noise") {
		t.Errorf("description = %q — v4 nests it as {ProductDescription, DetailedDescription}", c.Description)
	}
	if !strings.Contains(c.Description, "4210 in stock") {
		t.Errorf("stock should be visible, got %q", c.Description)
	}
	if c.Datasheet == "" {
		t.Error("datasheet = \"\" — v4 renamed PrimaryDatasheet to DatasheetUrl")
	}
	if c.ID != "296-1373-5-ND" {
		t.Errorf("ID = %q — the Digi-Key number lives in ProductVariations, not at the top level", c.ID)
	}
	if !strings.HasPrefix(c.Package, "8-DIP") {
		t.Errorf("package = %q, want it read from the Package / Case parameter", c.Package)
	}
	if !c.MetadataOnly {
		t.Error("a distributor row must be marked metadata-only")
	}

	if gotHeaders.Get("Authorization") != "Bearer TOK" {
		t.Errorf("Authorization = %q", gotHeaders.Get("Authorization"))
	}
	if gotHeaders.Get("X-DIGIKEY-Client-Id") != "ID" {
		t.Errorf("X-DIGIKEY-Client-Id is required and must carry the client id, got %q",
			gotHeaders.Get("X-DIGIKEY-Client-Id"))
	}
	if !strings.Contains(gotTokenForm, "grant_type=client_credentials") {
		t.Errorf("token form = %q", gotTokenForm)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(gotBody), &sent); err != nil {
		t.Fatalf("search body is not JSON: %v (%s)", err, gotBody)
	}
	if sent["Keywords"] != "NE5532" {
		t.Errorf("body = %s — v4 wants Keywords/Limit/Offset", gotBody)
	}

	// The token is cached: v4 allows 120 requests a minute, and spending one
	// of them on authentication per search halves the budget.
	if _, err := p.Search(context.Background(), Query{Text: "NE5532"}); err != nil {
		t.Fatal(err)
	}
	if tokenCalls != 1 {
		t.Errorf("token fetched %d times, want 1 — it should be cached until expiry", tokenCalls)
	}
}

func TestDigiKeyUnconfiguredIsUnavailable(t *testing.T) {
	p := &digikeyProvider{env: Env{DigiKeyID: "id"}} // secret missing
	if p.Available() {
		t.Error("both halves of the credential are needed")
	}
	got, err := p.Search(context.Background(), Query{Text: "NE555"})
	if err != nil || len(got) != 0 {
		t.Errorf("an unconfigured provider must return nothing quietly, got %v %v", got, err)
	}
}

// Neither distributor can hand over files, and saying so with the next step
// attached is more useful than an error.
func TestDistributorFetchRefusesWithGuidance(t *testing.T) {
	for _, p := range []Provider{
		&mouserProvider{env: Env{Mouser: "K"}},
		&digikeyProvider{env: Env{DigiKeyID: "i", DigiKeySecret: "s"}},
	} {
		_, err := p.Fetch(context.Background(), "X")
		if err == nil {
			t.Fatalf("%s: expected a refusal", p.Name())
		}
		if !strings.Contains(err.Error(), "find_part") {
			t.Errorf("%s: the refusal should name the next step, got %v", p.Name(), err)
		}
	}
}
