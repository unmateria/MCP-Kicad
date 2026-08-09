package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-kicad/internal/parts"
)

const minimalSymLib = `(kicad_symbol_lib (version 20220914)
  (symbol "R" (pin passive line (at 0 0 270))))`

const minimalSymLibTable = `(sym_lib_table)`

func resultText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if len(result.Content) == 0 {
		t.Fatal("empty content in result")
	}
	tc, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected *mcp.TextContent, got %T", result.Content[0])
	}
	return tc.Text
}

func TestHandleGetProjectInfo(t *testing.T) {
	env := &Env{LibsRoot: "libs", KicadCLI: "/usr/bin/kicad-cli"}
	res, _, err := env.handleGetProjectInfo(context.Background(), nil, getProjectInfoInput{})
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "libs") {
		t.Errorf("expected response to mention libs root, got: %q", text)
	}
}

func TestHandleCheckComponentExistence_LocalFound(t *testing.T) {
	tmp := t.TempDir()
	symDir := filepath.Join(tmp, "kicad-official", "kicad-symbols")
	if err := os.MkdirAll(symDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(symDir, "Device.kicad_sym"), []byte(minimalSymLib), 0o644); err != nil {
		t.Fatal(err)
	}

	env := &Env{LibsRoot: tmp}
	res, _, err := env.handleCheckComponentExistence(context.Background(), nil, checkComponentInput{Query: "Device:R"})
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "Device") {
		t.Errorf("expected response to mention component, got: %q", text)
	}
}

func TestHandleCheckComponentExistence_Missing(t *testing.T) {
	tmp := t.TempDir()
	env := &Env{LibsRoot: tmp}
	res, _, err := env.handleCheckComponentExistence(context.Background(), nil, checkComponentInput{Query: "Device:Nonexistent"})
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "not found") {
		t.Errorf("expected 'not found' in response, got: %q", text)
	}
}

func TestHandleRegisterLibrary_Success(t *testing.T) {
	tmp := t.TempDir()
	tableFile := filepath.Join(tmp, "sym-lib-table")
	if err := os.WriteFile(tableFile, []byte(minimalSymLibTable), 0o644); err != nil {
		t.Fatal(err)
	}

	env := &Env{}
	res, _, err := env.handleRegisterLibrary(context.Background(), nil, registerLibraryInput{
		TableFile: tableFile,
		LibName:   "MyLib",
		LibPath:   "/some/path/MyLib.kicad_sym",
		LibType:   "KiCad",
	})
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "registered") {
		t.Errorf("expected 'registered' in response, got: %q", text)
	}

	data, _ := os.ReadFile(tableFile)
	if !strings.Contains(string(data), `"MyLib"`) {
		t.Errorf("expected MyLib entry in table file, got:\n%s", data)
	}
}

func TestHandleRegisterLibrary_Duplicate(t *testing.T) {
	tmp := t.TempDir()
	tableFile := filepath.Join(tmp, "sym-lib-table")
	if err := os.WriteFile(tableFile, []byte(minimalSymLibTable), 0o644); err != nil {
		t.Fatal(err)
	}

	env := &Env{}
	input := registerLibraryInput{
		TableFile: tableFile,
		LibName:   "MyLib",
		LibPath:   "/some/path/MyLib.kicad_sym",
	}

	// Register once.
	if _, _, err := env.handleRegisterLibrary(context.Background(), nil, input); err != nil {
		t.Fatal(err)
	}
	// Register again.
	res, _, err := env.handleRegisterLibrary(context.Background(), nil, input)
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "already registered") {
		t.Errorf("expected 'already registered', got: %q", text)
	}

	// Verify the entry appears only once.
	data, _ := os.ReadFile(tableFile)
	count := strings.Count(string(data), `"MyLib"`)
	if count != 1 {
		t.Errorf("expected exactly 1 occurrence of MyLib, got %d:\n%s", count, data)
	}
}

// An imported library shadows the installed one: a part dropped into
// libs/symbols/ has to be found by name without any further configuration.
func TestHandleCheckComponentExistence_FindsImported(t *testing.T) {
	tmp := t.TempDir()
	symDir := parts.SymbolsPath(tmp)
	if err := os.MkdirAll(symDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(symDir, parts.ImportedLib+".kicad_sym"), []byte(minimalSymLib), 0o644); err != nil {
		t.Fatal(err)
	}

	env := &Env{LibsRoot: tmp}
	res, _, err := env.handleCheckComponentExistence(context.Background(), nil,
		checkComponentInput{Query: parts.ImportedLib + ":R"})
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "imported") {
		t.Errorf("expected the imported library to be reported as such, got: %q", text)
	}
}

// The header a table file gets must follow its NAME: writing (sym_lib_table
// into an fp-lib-table is what the textual implementation used to do, and
// KiCad refuses to load the result.
func TestHandleRegisterLibrary_FootprintTableHeader(t *testing.T) {
	tmp := t.TempDir()
	tableFile := filepath.Join(tmp, "fp-lib-table")

	env := &Env{}
	if _, _, err := env.handleRegisterLibrary(context.Background(), nil, registerLibraryInput{
		TableFile: tableFile,
		LibName:   "MyFootprints",
		LibPath:   "/some/path/MyFootprints.pretty",
	}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(tableFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(data)), "(fp_lib_table") {
		t.Errorf("fp-lib-table must start with (fp_lib_table, got:\n%s", data)
	}
	if !strings.Contains(string(data), `"MyFootprints"`) {
		t.Errorf("expected the entry in the table, got:\n%s", data)
	}
}
