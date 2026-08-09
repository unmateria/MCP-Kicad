package main

import (
	"context"
	"log"
	"os"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-kicad/internal/config"
	"mcp-kicad/internal/parts"
	"mcp-kicad/internal/tools"
)

func main() {
	// Locate config.ini next to the executable or in the working directory.
	configPath := "config.ini"
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "config.ini")
		if _, err := os.Stat(candidate); err == nil {
			configPath = candidate
		}
	}

	cfg := config.Load(configPath)

	anthropicKey := cfg.Anthropic
	if anthropicKey == "YOUR_TOKEN" {
		anthropicKey = ""
	}

	// Imported parts live under libs_root and that tree is not versioned, so a
	// fresh checkout has none of it. Create it before any tool can look there.
	if err := parts.EnsureTree(cfg.LibsRoot); err != nil {
		log.Printf("warning: %v", err)
	}

	env := &tools.Env{
		LibsRoot:        cfg.LibsRoot,
		KicadCLI:        cfg.KicadCLI,
		KicadSymbols:    cfg.KicadSymbols,
		KicadFootprints: cfg.KicadFootprints,
		Mouser:          cfg.Mouser,
		DigiKeyID:       cfg.DigiKeyID,
		DigiKeySecret:   cfg.DigiKeySecret,
		AnthropicKey:    anthropicKey,
		OutputDir:       cfg.OutputDir,
		ConfigPath:      cfg.ConfigPath,
		Log:             tools.NewSessionLogger(cfg.OutputDir),
	}

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "mcp-kicad",
		Version: "0.1.0",
	}, nil)

	env.Server = server

	tools.RegisterComponentTools(server, env)
	tools.RegisterImportTools(server, env)
	tools.RegisterSchematicTools(server, env)
	tools.RegisterPCBTools(server, env)
	tools.RegisterValidationTools(server, env)
	tools.RegisterExportTools(server, env)
	tools.RegisterNetlistTools(server, env)
	tools.RegisterCompileTools(server, env)

	log.SetOutput(os.Stderr)
	log.Println("mcp-kicad server starting (stdio transport)")

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
