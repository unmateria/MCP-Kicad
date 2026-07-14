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

func TestHandleGetProjectInfo_NoSnapEDA(t *testing.T) {
	env := &Env{LibsRoot: "libs", KicadCLI: "/path/to/kicad-cli", SnapEDA: nil}
	res, _, err := env.handleGetProjectInfo(context.Background(), nil, getProjectInfoInput{})
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "false") {
		t.Errorf("expected SnapEDA configured=false in response, got: %q", text)
	}
}

func TestHandleGetProjectInfo_WithSnapEDA(t *testing.T) {
	env := &Env{
		LibsRoot: "libs",
		KicadCLI: "/path/to/kicad-cli",
		SnapEDA:  parts.NewSnapEDAClient("mytoken"),
	}
	res, _, err := env.handleGetProjectInfo(context.Background(), nil, getProjectInfoInput{})
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "true") {
		t.Errorf("expected SnapEDA configured=true in response, got: %q", text)
	}
}
