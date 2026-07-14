package parts

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

const snapedaDefaultBase = "https://www.snapeda.com/api/v1/parts"

// SnapEDAClient wraps the SnapEDA REST API.
type SnapEDAClient struct {
	Token      string
	HTTPClient *http.Client
	baseURL    string
}

// NewSnapEDAClient creates a client with a 30-second timeout.
func NewSnapEDAClient(token string) *SnapEDAClient {
	return newSnapEDAClientWithBase(token, snapedaDefaultBase)
}

// newSnapEDAClientWithBase creates a client targeting a custom base URL (for tests).
func newSnapEDAClientWithBase(token, baseURL string) *SnapEDAClient {
	return &SnapEDAClient{
		Token:   token,
		baseURL: baseURL,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// snapedaSearchResponse is a partial model of the SnapEDA search JSON response.
type snapedaSearchResponse struct {
	Data []struct {
		SnapedaPartID int    `json:"snapeda_part_id"`
		PartName      string `json:"part_name"`
		Manufacturer  string `json:"manufacturer"`
		HasSymbol     bool   `json:"has_symbol"`
		HasFootprint  bool   `json:"has_footprint"`
		SlugURL       string `json:"slug_url"`
	} `json:"data"`
}

// SearchResult holds one search hit from SnapEDA.
type SearchResult struct {
	ID           int
	PartName     string
	Manufacturer string
	HasSymbol    bool
	HasFootprint bool
	SlugURL      string
}

// Search queries SnapEDA for components matching the given keyword.
// Returns up to 10 results.
func (c *SnapEDAClient) Search(keyword string) ([]SearchResult, error) {
	endpoint := fmt.Sprintf("%s/search/?q=%s&token=%s", c.baseURL, url.QueryEscape(keyword), url.QueryEscape(c.Token))
	resp, err := c.HTTPClient.Get(endpoint)
	if err != nil {
		return nil, fmt.Errorf("snapeda: HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("snapeda: unexpected status %d", resp.StatusCode)
	}

	var parsed snapedaSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("snapeda: JSON decode failed: %w", err)
	}

	results := make([]SearchResult, 0, len(parsed.Data))
	for _, d := range parsed.Data {
		results = append(results, SearchResult{
			ID:           d.SnapedaPartID,
			PartName:     d.PartName,
			Manufacturer: d.Manufacturer,
			HasSymbol:    d.HasSymbol,
			HasFootprint: d.HasFootprint,
			SlugURL:      d.SlugURL,
		})
	}
	return results, nil
}

// Download fetches the KiCad symbol and footprint files for a given SnapEDA part ID
// and saves them under destDir. Returns the paths to the downloaded files.
func (c *SnapEDAClient) Download(partID int, destDir string) (symbolPath, footprintPath string, err error) {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", "", fmt.Errorf("snapeda: cannot create dest dir: %w", err)
	}

	symURL := fmt.Sprintf("%s/%d/view/?token=%s&type=symbol&format=kicad", c.baseURL, partID, url.QueryEscape(c.Token))
	fpURL := fmt.Sprintf("%s/%d/view/?token=%s&type=footprint&format=kicad", c.baseURL, partID, url.QueryEscape(c.Token))

	symbolPath = filepath.Join(destDir, fmt.Sprintf("%d.kicad_sym", partID))
	footprintPath = filepath.Join(destDir, fmt.Sprintf("%d.kicad_mod", partID))

	if err := downloadFile(c.HTTPClient, symURL, symbolPath); err != nil {
		return "", "", fmt.Errorf("snapeda: symbol download failed: %w", err)
	}
	if err := downloadFile(c.HTTPClient, fpURL, footprintPath); err != nil {
		return "", "", fmt.Errorf("snapeda: footprint download failed: %w", err)
	}
	return symbolPath, footprintPath, nil
}

func downloadFile(client *http.Client, rawURL, destPath string) error {
	resp, err := client.Get(rawURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d for %s", resp.StatusCode, rawURL)
	}
	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}
