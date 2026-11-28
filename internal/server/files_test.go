package server

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func listFiles(t *testing.T, f *fixture, path string) (int, filesJSON) {
	t.Helper()
	var out filesJSON
	code := f.call("GET", "/api/files?path="+url.QueryEscape(path), nil, &out)
	return code, out
}

func TestFilesListsFoldersFirst(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "b"), 0o755)
	os.WriteFile(filepath.Join(dir, "a.db"), []byte("12345"), 0o644)
	os.WriteFile(filepath.Join(dir, "Z.txt"), nil, 0o644)
	f := newFixture(t)
	f.login()

	code, out := listFiles(t, f, dir)
	if code != http.StatusOK || out.Kind != "dir" || out.Path != dir || out.Parent != filepath.Dir(dir) {
		t.Fatalf("%d %+v", code, out)
	}
	var names []string
	for _, e := range out.Entries {
		names = append(names, e.Name)
	}
	if !slices.Equal(names, []string{"b", "a.db", "Z.txt"}) || !out.Entries[0].Dir || out.Entries[1].Size != 5 {
		t.Fatalf("entries = %+v", out.Entries)
	}
	if len(out.Roots) == 0 || out.Sep == "" {
		t.Fatalf("roots %v sep %q", out.Roots, out.Sep)
	}

	if _, out = listFiles(t, f, filepath.Join(dir, "a.db")); out.Kind != "file" || out.Size != 5 {
		t.Fatalf("file: %+v", out)
	}
	if _, out = listFiles(t, f, filepath.Join(dir, "new.db")); out.Kind != "missing" || !out.ParentExists {
		t.Fatalf("missing: %+v", out)
	}
	if _, out = listFiles(t, f, filepath.Join(dir, "nope", "new.db")); out.Kind != "missing" || out.ParentExists {
		t.Fatalf("missing folder: %+v", out)
	}
	home, _ := os.UserHomeDir()
	if _, out = listFiles(t, f, ""); out.Path != home || out.Home != home {
		t.Fatalf("empty path: %+v", out)
	}
}

func TestFilesOnlyOnLoopback(t *testing.T) {
	remote := newFixtureAt(t, Config{Addr: "0.0.0.0:0", BehindProxy: true, AllowedHosts: []string{"127.0.0.1"}})
	remote.login()
	if code, _ := listFiles(t, remote, t.TempDir()); code != http.StatusForbidden {
		t.Fatalf("remote: %d", code)
	}
}
