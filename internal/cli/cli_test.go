package cli

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arunksoman/tessera/internal/engine"
	"github.com/arunksoman/tessera/internal/tiles/mysql"
	"github.com/arunksoman/tessera/internal/tiles/postgres"
	"github.com/arunksoman/tessera/tile"
)

type result struct {
	code        int
	stdout, err string
}

func tess(stdin string, args ...string) result {
	var out, errb bytes.Buffer
	a := &App{Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errb, Version: "test",
		Tiles: []tile.Tile{postgres.Tile{}, mysql.Tile{}}}
	code := a.Run(args)
	return result{code, out.String(), errb.String()}
}

// env isolates a test from the real config directory and OS keychain.
func env(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	key := filepath.Join(dir, "master.key")
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, b, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TESSERA_CONFIG_DIR", dir)
	t.Setenv("TESSERA_MASTER_KEY_FILE", key)
	return dir
}

func TestVersionAndHelp(t *testing.T) {
	if r := tess("", "version"); r.code != 0 || r.stdout != "tess test\n" {
		t.Fatalf("%+v", r)
	}
	if r := tess(""); r.code != exitUsage || !strings.Contains(r.err, "Usage") {
		t.Fatalf("%+v", r)
	}
	if r := tess("", "nope"); r.code != exitUsage || !strings.Contains(r.err, `unknown command "nope"`) {
		t.Fatalf("%+v", r)
	}
}

func TestProfileLifecycle(t *testing.T) {
	dir := env(t)
	r := tess("hunter2\n", "profile", "add", "dev", "--tile", "postgres", "--host", "h", "--user", "u",
		"--database", "d", "--tls", "disable", "--env", "production", "--read-only", "--password-stdin")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	r = tess("", "profile", "list")
	if r.code != 0 || !strings.Contains(r.stdout, "dev\tpostgres\th [production] [read-only]") {
		t.Fatalf("%+v", r)
	}
	for _, f := range []string{"config.toml", "secrets.json"} {
		raw, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "hunter2") {
			t.Fatalf("plaintext password in %s", f)
		}
	}
	if r = tess("", "profile", "rm", "dev"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if r = tess("", "profile", "list"); r.stdout != "" {
		t.Fatalf("%+v", r)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "secrets.json"))
	if strings.Contains(string(raw), "dev/password") {
		t.Fatal("removing a profile must remove its secret")
	}
}

func TestProfileAddValidates(t *testing.T) {
	env(t)
	if r := tess("", "profile", "add", "x", "--tile", "oracle"); r.code != exitUsage {
		t.Fatalf("%+v", r)
	}
	if r := tess("", "profile", "add", "--tile", "postgres"); r.code != exitUsage {
		t.Fatalf("%+v", r)
	}
	if r := tess("", "profile", "add", "a/b", "--tile", "postgres"); r.code != exitError {
		t.Fatalf("%+v", r)
	}
}

func TestQueryValidates(t *testing.T) {
	env(t)
	if r := tess("", "query", "SELECT 1"); r.code != exitUsage || !strings.Contains(r.err, "-c PROFILE") {
		t.Fatalf("%+v", r)
	}
	if r := tess("", "query", "-c", "x", "-f", "xml", "SELECT 1"); r.code != exitUsage {
		t.Fatalf("%+v", r)
	}
	if r := tess("", "query", "-c", "missing", "SELECT 1"); r.code != exitError || !strings.Contains(r.err, `no profile named "missing"`) {
		t.Fatalf("%+v", r)
	}
}

func TestConfirmerRules(t *testing.T) {
	drop := engine.Prompt{Statement: "DROP TABLE t", Class: tile.Classification{Risk: tile.RiskDestructive, Reason: "DROP removes data"}}
	prod := drop
	prod.Env = engine.EnvProduction
	app := func(stdin string, tty bool) (*App, *bytes.Buffer) {
		var errb bytes.Buffer
		return &App{Stdin: strings.NewReader(stdin), Stdout: &bytes.Buffer{}, Stderr: &errb, TTY: tty}, &errb
	}

	if a, _ := app("yes\n", false); a.confirmer(false, false, "dev") != nil {
		t.Error("without a terminal nobody can answer, so it must refuse")
	}
	if a, _ := app("yes\n", true); a.confirmer(false, true, "dev") != nil {
		t.Error("when SQL came from stdin, stdin cannot also answer")
	}
	if a, _ := app("yes\n", true); !a.confirmer(false, false, "dev")(drop) {
		t.Error("typing yes must confirm")
	}
	if a, _ := app("y\n", true); a.confirmer(false, false, "dev")(drop) {
		t.Error("only the exact word confirms")
	}
	if a, _ := app("yes\n", true); a.confirmer(false, false, "prod-db")(prod) {
		t.Error("production needs the profile name, not yes")
	}
	a, errb := app("prod-db\n", true)
	if !a.confirmer(false, false, "prod-db")(prod) {
		t.Error("the profile name must confirm on production")
	}
	if !strings.Contains(errb.String(), "PRODUCTION") || !strings.Contains(errb.String(), "DROP TABLE t") {
		t.Errorf("prompt must show the statement and the environment: %q", errb.String())
	}
	if a, _ := app("", false); !a.confirmer(true, true, "dev")(prod) {
		t.Error("--yes must confirm for scripts")
	}
}
