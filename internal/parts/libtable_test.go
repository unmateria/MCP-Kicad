package parts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegisterLibrary_CreatesFileWithMatchingHeader(t *testing.T) {
	tmp := t.TempDir()
	for _, tc := range []struct {
		file string
		head string
	}{
		{"sym-lib-table", "(sym_lib_table"},
		{"fp-lib-table", "(fp_lib_table"},
	} {
		path := filepath.Join(tmp, tc.file)
		changed, err := RegisterLibrary(path, LibTableEntry{Name: "L", URI: "/x"})
		if err != nil {
			t.Fatal(err)
		}
		if !changed {
			t.Fatalf("%s: expected the first registration to change the table", tc.file)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(strings.TrimSpace(string(data)), tc.head) {
			t.Errorf("%s: expected header %s, got:\n%s", tc.file, tc.head, data)
		}
	}
}

func TestRegisterLibrary_IdempotentAndUpdating(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sym-lib-table")
	if _, err := RegisterLibrary(path, LibTableEntry{Name: "L", URI: "/old"}); err != nil {
		t.Fatal(err)
	}

	changed, err := RegisterLibrary(path, LibTableEntry{Name: "L", URI: "/old", Type: "KiCad"})
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("re-registering an identical entry must not rewrite the table")
	}

	changed, err = RegisterLibrary(path, LibTableEntry{Name: "L", URI: "/new"})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("a changed URI must update the entry")
	}

	entries, err := ReadLibTable(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one entry, got %d: %+v", len(entries), entries)
	}
	if entries[0].URI != "/new" {
		t.Errorf("expected the URI to be updated in place, got %q", entries[0].URI)
	}
}

// The table KiCad itself writes has to survive a round trip: parse, append,
// serialize, re-parse — with every pre-existing row intact.
func TestRegisterLibrary_PreservesExistingRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sym-lib-table")
	kicadStyle := "(sym_lib_table\n" +
		"\t(version 7)\n" +
		"\t(lib (name \"KiCad\") (type \"Table\") (uri \"C:/Program Files/KiCad/10.0/share/kicad/template/sym-lib-table\") (options \"\") (descr \"Default\"))\n" +
		"\t(lib (name \"mine\") (type \"KiCad\") (uri \"C:/x/mine.kicad_sym\") (options \"\") (descr \"\"))\n" +
		")\n"
	if err := os.WriteFile(path, []byte(kicadStyle), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := RegisterLibrary(path, LibTableEntry{Name: ImportedLib, URI: "C:/libs/symbols/MCP_Imported.kicad_sym"}); err != nil {
		t.Fatal(err)
	}

	entries, err := ReadLibTable(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"KiCad", "mine", ImportedLib}
	if len(entries) != len(want) {
		t.Fatalf("expected %d entries, got %d: %+v", len(want), len(entries), entries)
	}
	for i, name := range want {
		if entries[i].Name != name {
			t.Errorf("entry %d: expected %q, got %q", i, name, entries[i].Name)
		}
	}
	if entries[0].Type != "Table" {
		t.Errorf("the KiCad default row must keep its Table type, got %q", entries[0].Type)
	}
}

func TestVersionFromPath(t *testing.T) {
	cases := map[string]string{
		`C:\Program Files\KiCad\10.0\bin\kicad-cli.exe`: "10.0",
		"/usr/bin/kicad-cli":                            "",
		"/opt/kicad/9.0/bin/kicad-cli":                  "9.0",
	}
	for in, want := range cases {
		if got := versionFromPath(in); got != want {
			t.Errorf("versionFromPath(%q) = %q, want %q", in, got, want)
		}
	}
}
