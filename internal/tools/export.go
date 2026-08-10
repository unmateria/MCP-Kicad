package tools

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
)

// exportCache holds the last exported file bytes keyed by resource URI,
// so the MCP resource handlers can serve them.
var exportCache struct {
	mu   sync.RWMutex
	data map[string][]byte
	mime map[string]string
}

func init() {
	exportCache.data = make(map[string][]byte)
	exportCache.mime = make(map[string]string)
}

func cacheStore(uri string, data []byte, mimeType string) {
	exportCache.mu.Lock()
	exportCache.data[uri] = data
	exportCache.mime[uri] = mimeType
	exportCache.mu.Unlock()
}

func cacheLoad(uri string) ([]byte, string, bool) {
	exportCache.mu.RLock()
	defer exportCache.mu.RUnlock()
	d, ok := exportCache.data[uri]
	return d, exportCache.mime[uri], ok
}

// registerExportResource registers (or updates) a URI on the MCP server so
// Claude can read it back and use present_files to offer a download button.
func (e *Env) registerExportResource(uri, name, mimeType string, data []byte) {
	if e.Server == nil {
		return
	}
	cacheStore(uri, data, mimeType)
	e.Server.AddResource(&mcp.Resource{
		URI:      uri,
		Name:     name,
		MIMEType: mimeType,
	}, func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		blob, mime, ok := cacheLoad(req.Params.URI)
		if !ok {
			return nil, fmt.Errorf("resource not found: %s", req.Params.URI)
		}
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{
				URI:      req.Params.URI,
				MIMEType: mime,
				Blob:     blob,
			}},
		}, nil
	})
}

type exportSchematicImageInput struct {
	SchematicPath string `json:"schematic_path" jsonschema:"Path to the .kicad_sch file"`
	Format        string `json:"format,omitempty" jsonschema:"Output format: svg (default) or pdf"`
}

func (e *Env) handleExportSchematicImage(_ context.Context, _ *mcp.CallToolRequest, input exportSchematicImageInput) (res *mcp.CallToolResult, _ any, _ error) {
	defer recoverToolPanic(&res)
	if input.SchematicPath == "" {
		return toolText("error: schematic_path is required"), nil, nil
	}

	format := strings.ToLower(strings.TrimSpace(input.Format))
	if format == "" {
		format = "svg"
	}
	if format != "svg" && format != "pdf" {
		return toolText(fmt.Sprintf("error: PNG export is not supported. Use format='svg' (default, vector + inline preview) or format='pdf'. Got %q.", input.Format)), nil, nil
	}

	schPath, err := filepath.Abs(input.SchematicPath)
	if err != nil {
		return toolText(fmt.Sprintf("error resolving path: %v", err)), nil, nil
	}
	if _, err := os.Stat(schPath); os.IsNotExist(err) {
		return toolText(fmt.Sprintf(
			"schematic not found at %s — use get_project_info to find the server's working directory",
			schPath,
		)), nil, nil
	}

	outDir := filepath.Dir(schPath)
	if e.OutputDir != "" {
		outDir = e.OutputDir
		_ = os.MkdirAll(outDir, 0o755)
	}
	baseName := strings.TrimSuffix(filepath.Base(schPath), ".kicad_sch")

	// Read the .kicad_sch file for download resource.
	schBytes, _ := os.ReadFile(schPath)

	switch format {
	case "pdf":
		return e.exportPDF(schPath, outDir, baseName, schBytes)
	default: // "svg"
		return e.exportSVG(schPath, outDir, baseName, schBytes)
	}
}

// exportPDF exports the schematic as PDF via kicad-cli.
func (e *Env) exportPDF(schPath, outDir, baseName string, schBytes []byte) (*mcp.CallToolResult, any, error) {
	pdfPath := filepath.Join(outDir, baseName+".pdf")
	cmd := exec.Command(e.KicadCLI,
		"sch", "export", "pdf",
		"--output", pdfPath,
		"--exclude-drawing-sheet",
		"--no-background-color",
		schPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return toolText(fmt.Sprintf("kicad-cli pdf export failed: %v\n%s", err, out)), nil, nil
	}
	pdfBytes, err := os.ReadFile(pdfPath)
	if err != nil {
		return toolText("error: kicad-cli ran but produced no PDF"), nil, nil
	}

	pdfURI := "kicad://exports/" + baseName + ".pdf"
	schURI := "kicad://exports/" + baseName + ".kicad_sch"
	e.registerExportResource(pdfURI, baseName+".pdf", "application/pdf", pdfBytes)
	if len(schBytes) > 0 {
		e.registerExportResource(schURI, baseName+".kicad_sch", "application/octet-stream", schBytes)
	}

	var msg strings.Builder
	fmt.Fprintf(&msg, "Schematic exported as PDF: %s\n", pdfPath)
	fmt.Fprintf(&msg, "\nTo offer download buttons, read these MCP resources and call present_files\n")
	fmt.Fprintf(&msg, "(they carry the file CONTENT, so this works even when your filesystem access\n")
	fmt.Fprintf(&msg, "does not cover the path above; if you need the files somewhere you can reach,\n")
	fmt.Fprintf(&msg, "call set_output_dir with a directory inside your allowed folders and re-export):\n")
	fmt.Fprintf(&msg, "  PDF:      %s\n", pdfURI)
	if len(schBytes) > 0 {
		fmt.Fprintf(&msg, "  KiCad:    %s\n", schURI)
	}
	return toolText(msg.String()), nil, nil
}

// exportSVG exports the schematic as SVG with an inline PNG preview.
func (e *Env) exportSVG(schPath, outDir, baseName string, schBytes []byte) (*mcp.CallToolResult, any, error) {
	svgPath := filepath.Join(outDir, baseName+".svg")
	cmd := exec.Command(e.KicadCLI,
		"sch", "export", "svg",
		"--output", outDir,
		"--exclude-drawing-sheet",
		"--no-background-color",
		schPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return toolText(fmt.Sprintf("kicad-cli svg export failed: %v\n%s", err, out)), nil, nil
	}
	svgBytes, err := os.ReadFile(svgPath)
	if err != nil {
		return toolText("error: kicad-cli ran but produced no SVG"), nil, nil
	}

	svgURI := "kicad://exports/" + baseName + ".svg"
	schURI := "kicad://exports/" + baseName + ".kicad_sch"
	e.registerExportResource(svgURI, baseName+".svg", "image/svg+xml", svgBytes)
	if len(schBytes) > 0 {
		e.registerExportResource(schURI, baseName+".kicad_sch", "application/octet-stream", schBytes)
	}

	var contents []mcp.Content
	pngData, convErr := svgToPNG(svgPath)
	if convErr == nil {
		contents = append(contents, &mcp.ImageContent{Data: pngData, MIMEType: "image/png"})
	}

	var msg strings.Builder
	fmt.Fprintf(&msg, "Schematic exported as SVG: %s\n", svgPath)
	fmt.Fprintf(&msg, "\nTo offer download buttons, read these MCP resources and call present_files\n")
	fmt.Fprintf(&msg, "(they carry the file CONTENT, so this works even when your filesystem access\n")
	fmt.Fprintf(&msg, "does not cover the path above; if you need the files somewhere you can reach,\n")
	fmt.Fprintf(&msg, "call set_output_dir with a directory inside your allowed folders and re-export):\n")
	fmt.Fprintf(&msg, "  SVG:      %s\n", svgURI)
	if len(schBytes) > 0 {
		fmt.Fprintf(&msg, "  KiCad:    %s\n", schURI)
	}
	if convErr != nil {
		fmt.Fprintf(&msg, "\n(PNG inline preview failed: %v)", convErr)
	}
	contents = append(contents, &mcp.TextContent{Text: msg.String()})
	return &mcp.CallToolResult{Content: contents}, nil, nil
}

// svgToPNG converts an SVG file to PNG bytes for inline preview.
// Tries a headless Chromium-family browser first, then falls back to pure-Go oksvg.
// The result is cropped to content (white margins removed).
func svgToPNG(svgPath string) ([]byte, error) {
	var data []byte
	var err error
	if data, err = svgToPNGBrowser(svgPath); err != nil {
		data, err = svgToPNGGo(svgPath, 2400)
	}
	if err != nil {
		return nil, err
	}
	if cropped, cropErr := cropWhiteMargins(data, 20); cropErr == nil {
		return cropped, nil
	}
	return data, nil
}

// RenderSchematicPNG runs `kicad-cli sch export svg` then converts the SVG
// to PNG bytes for inline preview. Returns nil bytes + error if any step
// fails (kicadCLI empty, kicad-cli unavailable, render fails, etc.).
// Used by withInlinePNG to attach previews to mutating tools.
func RenderSchematicPNG(schPath, kicadCLI, outDir string) ([]byte, error) {
	if kicadCLI == "" {
		return nil, fmt.Errorf("kicad-cli not configured")
	}
	if schPath == "" {
		return nil, fmt.Errorf("schematic path is empty")
	}
	if _, err := os.Stat(schPath); err != nil {
		return nil, fmt.Errorf("schematic not found: %w", err)
	}
	if outDir == "" {
		outDir = filepath.Dir(schPath)
	}
	_ = os.MkdirAll(outDir, 0o755)

	baseName := strings.TrimSuffix(filepath.Base(schPath), ".kicad_sch")
	svgPath := filepath.Join(outDir, baseName+".svg")
	cmd := exec.Command(kicadCLI,
		"sch", "export", "svg",
		"--output", outDir,
		"--exclude-drawing-sheet",
		"--no-background-color",
		schPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("kicad-cli svg export: %w (%s)", err, out)
	}
	pngData, err := svgToPNG(svgPath)
	if err != nil {
		return nil, fmt.Errorf("svg→png: %w", err)
	}
	return pngData, nil
}

// cropWhiteMargins scans inward from each edge to find the first non-white pixel,
// crops the image to that bounding box, and adds padding pixels of margin on each side.
func cropWhiteMargins(data []byte, padding int) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	bounds := img.Bounds()
	minX, minY := bounds.Max.X, bounds.Max.Y
	maxX, maxY := bounds.Min.X-1, bounds.Min.Y-1
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if r < 60000 || g < 60000 || b < 60000 {
				if x < minX {
					minX = x
				}
				if y < minY {
					minY = y
				}
				if x > maxX {
					maxX = x
				}
				if y > maxY {
					maxY = y
				}
			}
		}
	}
	if maxX < minX || maxY < minY {
		return data, nil
	}
	minX -= padding
	minY -= padding
	maxX += padding
	maxY += padding
	if minX < bounds.Min.X {
		minX = bounds.Min.X
	}
	if minY < bounds.Min.Y {
		minY = bounds.Min.Y
	}
	if maxX >= bounds.Max.X {
		maxX = bounds.Max.X - 1
	}
	if maxY >= bounds.Max.Y {
		maxY = bounds.Max.Y - 1
	}
	type subImager interface {
		SubImage(r image.Rectangle) image.Image
	}
	si, ok := img.(subImager)
	if !ok {
		return data, nil
	}
	cropped := si.SubImage(image.Rect(minX, minY, maxX+1, maxY+1))
	var buf bytes.Buffer
	if err := png.Encode(&buf, cropped); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// svgToPNGBrowser uses a headless Chromium-family browser to render the SVG.
//
// The new headless mode (the only one available since 2024) ignores the
// working directory for --screenshot and writes either to user-data-dir or
// to a path specified via --screenshot=<path>. We pass the exact target
// file so we know where to read it back.
//
// Any Chromium derivative accepts these flags, so Edge, Chrome, Chromium and
// Brave are interchangeable here. Only the Windows path is verified in
// practice; the others let a Linux or macOS build render text-accurate
// previews instead of dropping to the text-less oksvg fallback.
func svgToPNGBrowser(svgPath string) ([]byte, error) {
	browserExe := findChromium()
	if browserExe == "" {
		return nil, fmt.Errorf("no Chromium-family browser found (tried Edge, Chrome, Chromium, Brave)")
	}

	tmpDir, err := os.MkdirTemp("", "mcp-kicad-export-*")
	if err != nil {
		return nil, fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	absSVG, _ := filepath.Abs(svgPath)
	fileURL := "file:///" + strings.ReplaceAll(filepath.ToSlash(absSVG), " ", "%20")
	shotPath := filepath.Join(tmpDir, "screenshot.png")

	// Modern Edge requires the explicit "new" headless mode and refuses to
	// run screenshots against a locked default profile — both were silently
	// killing this path and dropping every render to the text-less oksvg
	// fallback. The 2x device scale keeps schematic text sharp after the
	// white-margin crop.
	cmd := exec.Command(browserExe,
		"--headless=new",
		"--disable-gpu",
		"--no-sandbox",
		"--hide-scrollbars",
		"--user-data-dir="+filepath.Join(tmpDir, "profile"),
		"--force-device-scale-factor=2",
		"--window-size=3400,2500",
		"--virtual-time-budget=3000",
		"--screenshot="+shotPath,
		fileURL,
	)
	cmd.Dir = tmpDir
	out, cmdErr := cmd.CombinedOutput()
	// Edge's new headless mode hands the screenshot to a child that can
	// outlive msedge itself: the PNG appears up to a few SECONDS after the
	// command returns (and Edge exits non-zero on benign warnings anyway).
	// Reading immediately was silently dropping every render to the
	// text-less oksvg fallback — poll until the file exists and its size is
	// stable across two reads.
	deadline := time.Now().Add(12 * time.Second)
	lastLen := -1
	for time.Now().Before(deadline) {
		data, rerr := os.ReadFile(shotPath)
		switch {
		case rerr == nil && len(data) > 0 && len(data) == lastLen:
			return data, nil
		case rerr == nil:
			lastLen = len(data)
		}
		time.Sleep(300 * time.Millisecond)
	}
	return nil, fmt.Errorf("browser produced no screenshot (err=%v)\n%s", cmdErr, out)
}

// findChromium returns the first Chromium-family browser present on this
// machine, or "" when none is installed.
func findChromium() string {
	var candidates []string
	switch runtime.GOOS {
	case "windows":
		candidates = []string{
			`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
			`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		}
	case "darwin":
		candidates = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
		}
	default:
		candidates = []string{
			"/usr/bin/chromium",
			"/usr/bin/chromium-browser",
			"/usr/bin/google-chrome",
			"/usr/bin/microsoft-edge",
			"/snap/bin/chromium",
		}
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	// PATH is the last resort: a distro or a manual install can put the
	// binary anywhere, and on Linux that is the normal case.
	for _, n := range []string{"chromium", "chromium-browser", "google-chrome", "microsoft-edge", "msedge", "chrome"} {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	return ""
}

// svgToPNGGo uses the pure-Go oksvg+rasterx renderer as a fallback.
func svgToPNGGo(svgPath string, maxDim int) ([]byte, error) {
	icon, err := oksvg.ReadIcon(svgPath, oksvg.StrictErrorMode)
	if err != nil {
		icon, err = oksvg.ReadIcon(svgPath, oksvg.WarnErrorMode)
		if err != nil {
			return nil, fmt.Errorf("parse SVG: %w", err)
		}
	}
	w, h := icon.ViewBox.W, icon.ViewBox.H
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("SVG has zero dimensions")
	}
	scale := 1.0
	if w > float64(maxDim) {
		scale = float64(maxDim) / w
	}
	if h*scale > float64(maxDim) {
		scale = float64(maxDim) / h
	}
	pw, ph := int(w*scale), int(h*scale)
	if pw < 1 {
		pw = 1
	}
	if ph < 1 {
		ph = 1
	}
	icon.SetTarget(0, 0, float64(pw), float64(ph))
	rgba := image.NewRGBA(image.Rect(0, 0, pw, ph))
	for i := range rgba.Pix {
		rgba.Pix[i] = 0xff
	}
	scanner := rasterx.NewScannerGV(pw, ph, rgba, rgba.Bounds())
	raster := rasterx.NewDasher(pw, ph, scanner)
	icon.Draw(raster, 1.0)
	var buf bytes.Buffer
	if err := png.Encode(&buf, rgba); err != nil {
		return nil, fmt.Errorf("encode PNG: %w", err)
	}
	return buf.Bytes(), nil
}

// RegisterExportTools registers image export tools on the server.
func RegisterExportTools(s *mcp.Server, env *Env) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "export_schematic_image",
		Description: "Export a .kicad_sch schematic for visual review and download.\n\n" +
			"Format options:\n" +
			"  svg (default) — vector SVG + inline PNG preview.\n" +
			"  pdf           — PDF via kicad-cli. MCP resource URI only (no inline preview).\n\n" +
			"After calling this tool, read each kicad:// resource URI and " +
			"call present_files to offer the user download buttons.",
	}, WrapTool(env.Log, "export_schematic_image", env.handleExportSchematicImage))
}
