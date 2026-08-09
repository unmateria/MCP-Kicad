package tools

import (
	"context"
	"strings"
	"testing"

	"mcp-kicad/internal/parts"
)

func TestHandleValidateDesign_NoPaths(t *testing.T) {
	env := &Env{KicadCLI: "kicad-cli"}
	res, _, err := env.handleValidateDesign(context.Background(), nil, validateDesignInput{})
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "error") {
		t.Errorf("expected error when no paths provided, got: %q", text)
	}
}

func TestHandleGetProjectInfo_NoDistributorKeys(t *testing.T) {
	env := &Env{LibsRoot: "libs", KicadCLI: "/path/to/kicad-cli"}
	res, _, err := env.handleGetProjectInfo(context.Background(), nil, getProjectInfoInput{})
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "mouser=false") || !strings.Contains(text, "digikey=false") {
		t.Errorf("expected both distributor keys reported as absent, got: %q", text)
	}
	if !strings.Contains(text, parts.ImportedLib) {
		t.Errorf("expected the imported library path in the response, got: %q", text)
	}
}

func TestHandleGetProjectInfo_WithDistributorKeys(t *testing.T) {
	env := &Env{
		LibsRoot:      "libs",
		KicadCLI:      "/path/to/kicad-cli",
		Mouser:        "mykey",
		DigiKeyID:     "id",
		DigiKeySecret: "secret",
	}
	res, _, err := env.handleGetProjectInfo(context.Background(), nil, getProjectInfoInput{})
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "mouser=true") || !strings.Contains(text, "digikey=true") {
		t.Errorf("expected both distributor keys reported as present, got: %q", text)
	}
}
