package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPluginTrust(t *testing.T) {
	env(t)
	if r := tess("", "plugin", "trust", "acme", "not-a-key"); r.code != exitError {
		t.Fatalf("bad key accepted: %+v", r)
	}
	key := filepath.Join(t.TempDir(), "key")
	r := tess("", "plugin", "keygen", "--out", key)
	if r.code != exitOK {
		t.Fatal(r)
	}
	pub := strings.TrimSpace(r.stdout)
	if r := tess("", "plugin", "keygen", "--out", key); r.code == exitOK {
		t.Fatal("keygen overwrote an existing key")
	}
	if r := tess("", "plugin", "trust", "acme", pub); r.code != exitOK {
		t.Fatal(r)
	}
	if r := tess("", "plugin", "keys"); !strings.Contains(r.stdout, "acme\t"+pub) {
		t.Fatal(r)
	}
	if r := tess("", "plugin", "untrust", "acme"); r.code != exitOK {
		t.Fatal(r)
	}
	if r := tess("", "plugin", "keys"); r.stdout != "" {
		t.Fatal(r)
	}
}

func TestPluginInstallNeedsConsent(t *testing.T) {
	env(t)
	// tess() has no terminal, so without --yes nobody can confirm.
	r := tess("", "plugin", "install", "redis", "--from", "github:acme/tessera-redis")
	if r.code != exitError || !strings.Contains(r.err, "confirmation") {
		t.Fatalf("%+v", r)
	}
	r = tess("", "plugin", "install", "postgres", "--yes", "--from", "./x")
	if r.code != exitError || !strings.Contains(r.err, "built into tess") {
		t.Fatalf("%+v", r)
	}
	r = tess("", "plugin", "install", "redis", "--yes")
	if r.code != exitError || !strings.Contains(r.err, "no source") {
		t.Fatalf("%+v", r)
	}
}

// TestPluginEndToEnd installs a locally built plugin, uses it in a profile, and removes it.
func TestPluginEndToEnd(t *testing.T) {
	env(t)
	bin := filepath.Join(t.TempDir(), "build")
	cmd := exec.Command("go", "build", "-o", bin, "../plugins/testdata/fakeplugin")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if r := tess("", "plugin", "install", "kv", "--yes", "--from", bin); r.code != exitError || !strings.Contains(r.err, "--allow-unsigned") {
		t.Fatalf("unsigned binary: %+v", r)
	}
	r := tess("", "plugin", "install", "kv", "--yes", "--allow-unsigned", "--from", bin)
	if r.code != exitOK || !strings.Contains(r.stdout, "installed kv") || !strings.Contains(r.err, "not signed") {
		t.Fatalf("%+v", r)
	}
	if r := tess("", "plugin", "list"); !strings.Contains(r.stdout, "kv\tlocal\tunsigned") {
		t.Fatal(r)
	}
	if r := tess("", "plugin", "verify"); r.code != exitOK || !strings.Contains(r.stdout, "kv\tok") {
		t.Fatal(r)
	}
	r = tess("pw\n", "profile", "add", "cache", "--tile", "kv", "--host", "h", "--set", "db=3", "--password-stdin")
	if r.code != exitOK {
		t.Fatalf("%+v", r)
	}
	r = tess("", "query", "-c", "cache", "-f", "csv", "rows 2")
	if r.code != exitOK || r.stdout != "n\n1\n2\n" {
		t.Fatalf("query through plugin: %+v", r)
	}
	if r := tess("", "plugin", "remove", "kv"); r.code != exitOK {
		t.Fatal(r)
	}
	if r := tess("", "connect", "cache"); r.code != exitError || !strings.Contains(r.err, "unknown tile") {
		t.Fatalf("%+v", r)
	}
}
