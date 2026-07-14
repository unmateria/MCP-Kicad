package parts

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapEDASearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[
			{"snapeda_part_id":1,"part_name":"ATmega328P","manufacturer":"Microchip","has_symbol":true,"has_footprint":true,"slug_url":"http://example.com/1"},
			{"snapeda_part_id":2,"part_name":"ATmega328P-AU","manufacturer":"Microchip","has_symbol":true,"has_footprint":false,"slug_url":"http://example.com/2"}
		]}`)
	}))
	defer srv.Close()

	c := newSnapEDAClientWithBase("token", srv.URL)
	results, err := c.Search("ATmega328P")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].PartName != "ATmega328P" {
		t.Errorf("unexpected part name: %q", results[0].PartName)
	}
	if !results[0].HasSymbol {
		t.Error("expected HasSymbol=true for first result")
	}
	if results[1].HasFootprint {
		t.Error("expected HasFootprint=false for second result")
	}
}

func TestSnapEDASearch_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := newSnapEDAClientWithBase("token", srv.URL)
	_, err := c.Search("something")
	if err == nil {
		t.Fatal("expected error for HTTP 500")
	}
}

func TestSnapEDASearch_EmptyResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[]}`)
	}))
	defer srv.Close()

	c := newSnapEDAClientWithBase("token", srv.URL)
	results, err := c.Search("nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}
}

func TestSnapEDADownload(t *testing.T) {
	const partID = 42
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.RawQuery, "type=symbol") {
			fmt.Fprint(w, "(kicad_symbol_lib)")
		} else if strings.Contains(r.URL.RawQuery, "type=footprint") {
			fmt.Fprint(w, "(module R)")
		} else {
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newSnapEDAClientWithBase("token", srv.URL)
	destDir := filepath.Join(t.TempDir(), "downloaded")

	symPath, fpPath, err := c.Download(partID, destDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(symPath); err != nil {
		t.Errorf("symbol file not created: %v", err)
	}
	if _, err := os.Stat(fpPath); err != nil {
		t.Errorf("footprint file not created: %v", err)
	}
}

func TestSnapEDADownload_NoSymbol(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.RawQuery, "type=symbol") {
			http.NotFound(w, r)
		} else {
			fmt.Fprint(w, "(module R)")
		}
	}))
	defer srv.Close()

	c := newSnapEDAClientWithBase("token", srv.URL)
	destDir := filepath.Join(t.TempDir(), "downloaded")

	_, _, err := c.Download(1, destDir)
	if err == nil {
		t.Fatal("expected error when symbol download fails")
	}
}
