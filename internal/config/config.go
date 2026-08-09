package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"gopkg.in/ini.v1"
)

// Config holds all validated configuration values.
type Config struct {
	KicadCLI        string
	KicadSymbols    string
	KicadFootprints string
	LibsRoot        string
	Freerouting     string
	Mouser          string // optional; distributor metadata only, no CAD files
	DigiKeyID       string // optional; OAuth2 client_credentials pair with DigiKeySecret
	DigiKeySecret   string
	PCBWay          string
	Anthropic       string // optional API key for visual ranking; empty disables capa B
	OutputDir       string // directory where generated files are written
	ConfigPath      string // absolute path to config.ini, for write-back
}

// Load reads config.ini if it exists. Missing keys fall back to auto-detection.
// Calls os.Exit(1) only if kicad-cli cannot be found by any means.
func Load(path string) *Config {
	get := func(cfg *ini.File, section, key string) string {
		if cfg == nil {
			return ""
		}
		s, err := cfg.GetSection(section)
		if err != nil {
			return ""
		}
		k, err := s.GetKey(key)
		if err != nil {
			return ""
		}
		return k.String()
	}

	var cfg *ini.File
	if data, err := ini.Load(path); err == nil {
		cfg = data
	}

	absConfigPath, _ := filepath.Abs(path)

	c := &Config{
		KicadCLI:        get(cfg, "paths", "kicad_cli"),
		KicadSymbols:    get(cfg, "paths", "kicad_symbols"),
		KicadFootprints: get(cfg, "paths", "kicad_footprints"),
		LibsRoot:        get(cfg, "paths", "libs_root"),
		Freerouting:     get(cfg, "paths", "freerouting"),
		Mouser:          apiKey(get(cfg, "api_keys", "mouser")),
		DigiKeyID:       apiKey(get(cfg, "api_keys", "digikey_client_id")),
		DigiKeySecret:   apiKey(get(cfg, "api_keys", "digikey_client_secret")),
		PCBWay:          get(cfg, "api_keys", "pcbway"),
		Anthropic:       get(cfg, "api_keys", "anthropic"),
		OutputDir:       get(cfg, "paths", "output_dir"),
		ConfigPath:      absConfigPath,
	}

	if c.KicadCLI == "" {
		c.KicadCLI = DetectKicadCLI()
	}
	if c.KicadCLI == "" {
		fmt.Fprintln(os.Stderr, "config: kicad-cli not found — install KiCad or set kicad_cli in config.ini")
		os.Exit(1)
	}

	kicadRoot := filepath.Dir(filepath.Dir(c.KicadCLI)) // .../bin/kicad-cli → .../
	if c.KicadSymbols == "" {
		c.KicadSymbols = filepath.Join(kicadRoot, "share", "kicad", "symbols")
	}
	if c.KicadFootprints == "" {
		c.KicadFootprints = filepath.Join(kicadRoot, "share", "kicad", "footprints")
	}
	if c.LibsRoot == "" {
		c.LibsRoot = "libs"
	}
	// A relative libs_root is resolved against the executable, not the working
	// directory: Claude Desktop launches the server from its own directory, so
	// "libs" would otherwise point somewhere unrelated. The working directory
	// still wins when it actually holds a libs tree, which is what `go run`
	// during development gives you.
	if !filepath.IsAbs(c.LibsRoot) {
		c.LibsRoot = resolveAgainstExecutable(c.LibsRoot)
	}
	if c.OutputDir == "" {
		c.OutputDir = defaultOutputDir()
	}
	if err := os.MkdirAll(c.OutputDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "config: cannot create output_dir %q: %v\n", c.OutputDir, err)
	}

	return c
}

// apiKey normalises an optional credential: the placeholder shipped in
// config.ini.example counts as "not configured", not as a token to send.
func apiKey(v string) string {
	if v == "YOUR_TOKEN" {
		return ""
	}
	return v
}

// resolveAgainstExecutable turns a relative path into an absolute one, keeping
// the working directory's copy when one exists there.
func resolveAgainstExecutable(rel string) string {
	if _, err := os.Stat(rel); err == nil {
		if abs, err := filepath.Abs(rel); err == nil {
			return abs
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return rel
	}
	return filepath.Join(filepath.Dir(exe), rel)
}

// defaultOutputDir is where generated files land when config.ini says nothing.
// Under the user's home directory, so it works the same on every platform and
// needs no privileges.
func defaultOutputDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "output"
	}
	return filepath.Join(home, "mcp-kicad", "output")
}

// DetectKicadCLI returns the path to kicad-cli if it can be found, or "".
// Exported so callers outside this package (utility commands, tests) can
// locate kicad-cli without hardcoding a specific KiCad version.
func DetectKicadCLI() string {
	var candidates []string
	if runtime.GOOS == "windows" {
		// Try versioned install dirs (KiCad 9.x, 8.x, …)
		for _, base := range []string{
			`C:\Program Files\KiCad`,
			`C:\Program Files (x86)\KiCad`,
		} {
			if entries, err := os.ReadDir(base); err == nil {
				for _, e := range entries {
					if e.IsDir() {
						candidates = append(candidates,
							filepath.Join(base, e.Name(), "bin", "kicad-cli.exe"))
					}
				}
			}
		}
		if p, err := exec.LookPath("kicad-cli.exe"); err == nil {
			return p
		}
	} else {
		candidates = []string{
			"/usr/bin/kicad-cli",
			"/usr/local/bin/kicad-cli",
			"/Applications/KiCad/KiCad.app/Contents/MacOS/kicad-cli",
		}
		if p, err := exec.LookPath("kicad-cli"); err == nil {
			return p
		}
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}
