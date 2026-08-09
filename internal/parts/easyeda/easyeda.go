// Package easyeda converts EasyEDA / LCSC component data into KiCad symbols
// and footprints.
//
// EasyEDA does not publish KiCad files. It publishes its own JSON, in which
// every graphic is a tilde-separated string, and the only way to get an LCSC
// part into KiCad is to understand that format and re-emit it. That is what
// this package does; nothing else here depends on it.
//
// The conversion logic is ported from JLC2KiCadLib (MIT). It is deliberately
// NOT ported from easyeda2kicad.py, which is AGPL-3.0 and therefore
// incompatible with this repository's licence.
//
// Every constant below was measured against live API responses on 2026-08-09,
// and the footprint conversion is checked against the same parts as published
// by the JLCPCB KiCad library — two independent renderings of one package have
// to agree on where the pads are.
package easyeda

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Scale is millimetres per EasyEDA unit. One unit is 10 mil in both the
// schematic and the PCB editor: measured against SOP-4_L4.4-W2.8-P1.27-LS7.0,
// whose 27.559-unit lead span comes out at exactly the 7.00 mm its name
// promises.
const Scale = 0.254

// browserUA is required. The API answers 403 to a request that does not look
// like a browser, which is the first thing to check when this stops working.
const browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

const apiBase = "https://easyeda.com/api"

// Client talks to the EasyEDA product API.
type Client struct {
	HTTP    *http.Client
	BaseURL string
}

// NewClient returns a client with a sane timeout.
func NewClient(h *http.Client) *Client {
	if h == nil {
		h = &http.Client{Timeout: 60 * time.Second}
	}
	return &Client{HTTP: h, BaseURL: apiBase}
}

// Component is the part of EasyEDA's response this package reads. Everything
// omitted is either presentation or fields the converter has no use for.
type Component struct {
	UUID  string `json:"uuid"`
	Title string `json:"title"`
	LCSC  struct {
		Number string  `json:"number"`
		URL    string  `json:"url"`
		Stock  int     `json:"stock"`
		Price  float64 `json:"price"`
	} `json:"lcsc"`
	Description   string   `json:"description"`
	Tags          []string `json:"tags"`
	DataStr       DataStr  `json:"dataStr"`
	PackageDetail struct {
		Title   string  `json:"title"`
		UUID    string  `json:"uuid"`
		DataStr DataStr `json:"dataStr"`
	} `json:"packageDetail"`
}

// DataStr is one EasyEDA document: a header with the origin, and a list of
// graphics encoded as tilde-separated strings.
type DataStr struct {
	Head  Head     `json:"head"`
	Shape []string `json:"shape"`
}

// Head carries the document origin every coordinate is relative to.
type Head struct {
	X      float64           `json:"x"`
	Y      float64           `json:"y"`
	UUID   string            `json:"uuid"`
	UUID3D string            `json:"uuid_3d"`
	CPara  map[string]string `json:"c_para"`
}

// Param reads a c_para field, which is where EasyEDA keeps the manufacturer,
// the part number and the package name.
func (h Head) Param(key string) string { return h.CPara[key] }

// MPN is the manufacturer part number, falling back to the component title —
// which for an LCSC-contributed part is usually the same string.
func (c *Component) MPN() string {
	if v := c.DataStr.Head.Param("Manufacturer Part"); v != "" {
		return v
	}
	if v := c.DataStr.Head.Param("name"); v != "" {
		return v
	}
	return c.Title
}

// Manufacturer is the maker EasyEDA records, if any.
func (c *Component) Manufacturer() string { return c.DataStr.Head.Param("Manufacturer") }

// Package is the package name, which becomes the footprint's name suffix.
func (c *Component) Package() string {
	if v := c.PackageDetail.DataStr.Head.Param("package"); v != "" {
		return v
	}
	if v := c.DataStr.Head.Param("package"); v != "" {
		return v
	}
	return c.PackageDetail.Title
}

// Datasheet is the LCSC product page, which is the only stable link EasyEDA
// gives. It is not a PDF, but it is where the PDF is.
func (c *Component) Datasheet() string { return c.LCSC.URL }

// apiResponse is EasyEDA's envelope.
type apiResponse struct {
	Success bool            `json:"success"`
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Result  json.RawMessage `json:"result"`
}

// Fetch returns one component by LCSC number ("C115450").
func (c *Client) Fetch(ctx context.Context, lcscID string) (*Component, error) {
	lcscID = strings.ToUpper(strings.TrimSpace(lcscID))
	if lcscID == "" {
		return nil, fmt.Errorf("easyeda: empty LCSC number")
	}
	// The version parameter is not optional: without it the endpoint answers
	// with an older document shape whose packageDetail is missing.
	url := fmt.Sprintf("%s/products/%s/components?version=6.4.19.5", c.BaseURL, lcscID)
	raw, err := c.getResult(ctx, url)
	if err != nil {
		return nil, err
	}
	var comp Component
	if err := json.Unmarshal(raw, &comp); err != nil {
		return nil, fmt.Errorf("easyeda: %s: %w", lcscID, err)
	}
	if len(comp.DataStr.Shape) == 0 {
		return nil, fmt.Errorf("easyeda: %s has no symbol data", lcscID)
	}
	return &comp, nil
}

func (c *Client) getResult(ctx context.Context, url string) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("easyeda: %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("easyeda: %s: HTTP %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	var env apiResponse
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("easyeda: %s: %w", url, err)
	}
	if !env.Success {
		msg := env.Message
		if msg == "" {
			msg = fmt.Sprintf("code %d", env.Code)
		}
		return nil, fmt.Errorf("easyeda: %s: %s", url, msg)
	}
	return env.Result, nil
}

// Converted is a component turned into KiCad files.
type Converted struct {
	MPN           string
	Manufacturer  string
	Package       string
	Description   string
	Datasheet     string
	LCSC          string
	SymbolLib     []byte // a one-symbol .kicad_sym
	SymbolName    string
	Footprint     []byte // a .kicad_mod, empty when the part has no package
	FootprintName string
	// Notes records what the source contained that this converter did not
	// translate. Silence about a dropped graphic is how a footprint ends up
	// missing its keep-out and nobody notices.
	Notes []string
}

// Convert turns a fetched component into KiCad files.
func Convert(c *Component) (*Converted, error) {
	mpn := c.MPN()
	if mpn == "" {
		return nil, fmt.Errorf("easyeda: component has no part number")
	}
	out := &Converted{
		MPN:          mpn,
		Manufacturer: c.Manufacturer(),
		Package:      c.Package(),
		Description:  strings.TrimSpace(c.Description),
		Datasheet:    c.Datasheet(),
		LCSC:         c.LCSC.Number,
		SymbolName:   mpn,
	}
	if out.Description == "" && len(c.Tags) > 0 {
		out.Description = strings.Join(c.Tags, ", ")
	}

	symLib, symNotes, err := convertSymbol(c, mpn)
	if err != nil {
		return nil, err
	}
	out.SymbolLib = symLib
	out.Notes = append(out.Notes, symNotes...)

	if len(c.PackageDetail.DataStr.Shape) > 0 {
		fpName := out.Package
		if fpName == "" {
			fpName = mpn
		}
		fp, fpNotes, err := convertFootprint(c, fpName)
		if err != nil {
			out.Notes = append(out.Notes, "footprint could not be converted: "+err.Error())
		} else {
			out.Footprint = fp
			out.FootprintName = fpName
			out.Notes = append(out.Notes, fpNotes...)
		}
	} else {
		out.Notes = append(out.Notes, "this LCSC entry carries no package, so there is no footprint")
	}
	// EasyEDA publishes its 3D models as .obj behind a separate endpoint, in a
	// format KiCad does not read. Converting it is a project of its own and is
	// not attempted here rather than shipped half-working.
	if c.PackageDetail.DataStr.Head.UUID3D != "" {
		out.Notes = append(out.Notes, "the source has a 3D model, which this converter does not translate")
	}
	return out, nil
}
