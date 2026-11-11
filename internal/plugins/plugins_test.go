package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/arunksoman/tessera/tile"
)

var fakeBinary []byte

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "tess-fakeplugin-")
	if err != nil {
		panic(err)
	}
	out := filepath.Join(dir, "fakeplugin")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/fakeplugin")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		panic(fmt.Sprintf("building fake plugin: %v\n%s", err, b))
	}
	fakeBinary, err = os.ReadFile(out)
	if err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

var platform = runtime.GOOS + "/" + runtime.GOARCH

// release is a plugin release served over https.
type release struct {
	files  map[string][]byte
	srv    *httptest.Server
	pubKey string
	key    []byte
}

func newRelease(t *testing.T) *release {
	t.Helper()
	pub, priv, err := Keygen()
	if err != nil {
		t.Fatal(err)
	}
	r := &release{files: map[string][]byte{}, pubKey: pub, key: priv}
	r.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasPrefix(req.URL.Path, "/redirect-http/") {
			http.Redirect(w, req, "http://"+req.Host+strings.TrimPrefix(req.URL.Path, "/redirect-http"), http.StatusFound)
			return
		}
		b, ok := r.files[req.URL.Path]
		if !ok {
			http.NotFound(w, req)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

// publish adds a descriptor for name with the fake binary, signed when sign is true.
func (r *release) publish(t *testing.T, name string, sign bool, edit func(*Descriptor)) string {
	t.Helper()
	r.files["/"+name+"/bin"] = fakeBinary
	d := Descriptor{Name: name, Version: "1.0.0", APIVersion: tile.APIVersion,
		Artifacts: map[string]Artifact{platform: {URL: "bin", SHA256: sum(fakeBinary)}}}
	if edit != nil {
		edit(&d)
	}
	b, _ := json.Marshal(d)
	r.files["/"+name+"/"+DescriptorFile] = b
	if sign {
		sig, err := Sign(r.key, b)
		if err != nil {
			t.Fatal(err)
		}
		r.files["/"+name+"/"+DescriptorFile+".sig"] = sig
	}
	return r.srv.URL + "/" + name + "/" + DescriptorFile
}

func (r *release) installer(t *testing.T) *Installer {
	return &Installer{Root: t.TempDir(), TrustedKeys: map[string]string{"acme": r.pubKey},
		Builtins: []string{"postgres", "mysql"}, HTTP: r.srv.Client()}
}

func install(t *testing.T, in *Installer, name string, opt Options) (Record, error) {
	t.Helper()
	p, err := in.Resolve(context.Background(), name, opt)
	if err != nil {
		return Record{}, err
	}
	return in.Install(context.Background(), p)
}

func TestInstallSignedAndRun(t *testing.T) {
	t.Setenv("TESSERA_PLUGIN_TEST_SECRET", "leaked")
	r := newRelease(t)
	in := r.installer(t)
	src := r.publish(t, "redis", true, nil)
	in.Sources = map[string]string{"redis": src}
	p, err := in.Resolve(context.Background(), "redis", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Publisher != "acme" || p.Descriptor.Version != "1.0.0" || p.Artifact.SHA256 != sum(fakeBinary) {
		t.Fatalf("pending = %+v", p)
	}
	rec, err := in.Install(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Manifest.ID != "redis" || !rec.Signed() || !rec.ConfigWarner {
		t.Fatalf("record = %+v", rec)
	}

	tiles, errs := Load(in.Root, in.Builtins)
	if len(errs) > 0 || len(tiles) != 1 {
		t.Fatalf("load = %v, %v", tiles, errs)
	}
	pt := tiles[0]
	defer pt.Close()
	tl := AsTile(pt)
	if tl.Manifest().Name != "Fake redis" {
		t.Fatal(tl.Manifest())
	}
	w := tl.(tile.ConfigWarner).ConfigWarnings(tile.ConnConfig{})
	if len(w) != 1 || w[0] != "env:" {
		t.Fatalf("plugin saw tess's environment: %q", w)
	}
	env := tile.Env{Secret: func(context.Context, string) ([]byte, error) { return []byte("pw"), nil }}
	c, err := tl.Open(context.Background(), tile.ConnConfig{}, env)
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.Session(context.Background(), tile.SessionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.Execute(context.Background(), tile.Request{Text: "rows 3"})
	if err != nil {
		t.Fatal(err)
	}
	var rows int
	for {
		f, err := st.Next(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if f.Batch != nil {
			rows += f.Batch.Rows
		}
	}
	if rows != 3 {
		t.Fatalf("rows = %d", rows)
	}
	c.Close()
	if err := Verify(in.Root, "redis"); err != nil {
		t.Fatal(err)
	}
}

func TestTamperedBinaryDoesNotRun(t *testing.T) {
	r := newRelease(t)
	in := r.installer(t)
	in.Sources = map[string]string{"redis": r.publish(t, "redis", true, nil)}
	rec, err := install(t, in, "redis", Options{})
	if err != nil {
		t.Fatal(err)
	}
	path := rec.binaryPath(in.Root)
	b, _ := os.ReadFile(path)
	os.WriteFile(path, append(b, 0), 0o700)
	if err := Verify(in.Root, "redis"); !errors.Is(err, errHash) {
		t.Fatalf("verify = %v", err)
	}
	tiles, _ := Load(in.Root, in.Builtins)
	_, err = tiles[0].Open(context.Background(), tile.ConnConfig{}, tile.Env{})
	if err == nil || !strings.Contains(err.Error(), "hash") {
		t.Fatalf("open of a tampered plugin = %v", err)
	}
}

func TestUnsignedNeedsConsent(t *testing.T) {
	r := newRelease(t)
	in := r.installer(t)
	src := r.publish(t, "redis", false, nil)
	if _, err := in.Resolve(context.Background(), "redis", Options{From: src}); err == nil || !strings.Contains(err.Error(), "not signed") {
		t.Fatalf("err = %v", err)
	}
	p, err := in.Resolve(context.Background(), "redis", Options{From: src, AllowUnsigned: true})
	if err != nil {
		t.Fatal(err)
	}
	if p.Publisher != "" {
		t.Fatal("unsigned plugin has a publisher")
	}
}

func TestUntrustedSignatureRefused(t *testing.T) {
	r := newRelease(t)
	in := r.installer(t)
	src := r.publish(t, "redis", true, nil)
	other, _, _ := Keygen()
	in.TrustedKeys = map[string]string{"other": other}
	_, err := in.Resolve(context.Background(), "redis", Options{From: src, AllowUnsigned: true})
	if err == nil || !strings.Contains(err.Error(), "trusted key") {
		t.Fatalf("err = %v", err)
	}
}

func TestEditedDescriptorRefused(t *testing.T) {
	r := newRelease(t)
	in := r.installer(t)
	src := r.publish(t, "redis", true, nil)
	path := "/redis/" + DescriptorFile
	r.files[path] = []byte(strings.Replace(string(r.files[path]), "1.0.0", "1.0.1", 1))
	if _, err := in.Resolve(context.Background(), "redis", Options{From: src}); err == nil {
		t.Fatal("a descriptor edited after signing was accepted")
	}
}

func TestHashMismatchInstallsNothing(t *testing.T) {
	r := newRelease(t)
	in := r.installer(t)
	src := r.publish(t, "redis", true, nil)
	r.files["/redis/bin"] = append([]byte("evil"), fakeBinary...)
	_, err := install(t, in, "redis", Options{From: src})
	if err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("err = %v", err)
	}
	if recs, _ := List(in.Root); len(recs) != 0 {
		t.Fatal("a mismatched binary was installed")
	}
	entries, _ := os.ReadDir(in.Root)
	if len(entries) != 0 {
		t.Fatalf("staging left behind: %v", entries)
	}
}

func TestSHA256Pin(t *testing.T) {
	r := newRelease(t)
	in := r.installer(t)
	src := r.publish(t, "redis", true, nil)
	if _, err := in.Resolve(context.Background(), "redis", Options{From: src, SHA256: strings.Repeat("0", 64)}); err == nil {
		t.Fatal("wrong pin accepted")
	}
	if _, err := in.Resolve(context.Background(), "redis", Options{From: src, SHA256: sum(fakeBinary)}); err != nil {
		t.Fatal(err)
	}
}

func TestDescriptorChecks(t *testing.T) {
	r := newRelease(t)
	in := r.installer(t)
	cases := map[string]func(*Descriptor){
		"describes plugin": func(d *Descriptor) { d.Name = "other" },
		"tile API":         func(d *Descriptor) { d.APIVersion = tile.APIVersion + 1 },
		"no build":         func(d *Descriptor) { d.Artifacts = map[string]Artifact{"plan9/mips": {URL: "bin", SHA256: sum(nil)}} },
		"not https": func(d *Descriptor) {
			d.Artifacts = map[string]Artifact{platform: {URL: "http://example.com/bin", SHA256: sum(fakeBinary)}}
		},
	}
	for want, edit := range cases {
		t.Run(want, func(t *testing.T) {
			src := r.publish(t, "redis", true, edit)
			if _, err := in.Resolve(context.Background(), "redis", Options{From: src}); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("err = %v, want %q", err, want)
			}
		})
	}
}

func TestNamesAndSources(t *testing.T) {
	in := &Installer{Root: t.TempDir(), Builtins: []string{"postgres"}}
	for _, name := range []string{"postgres", "../x", "Redis", ""} {
		if _, err := in.Resolve(context.Background(), name, Options{From: "x"}); err == nil {
			t.Errorf("name %q accepted", name)
		}
	}
	for _, src := range []string{"http://example.com/p.json", "ftp://example.com/p.json", "github:a/..", "github:a", "https://user:pw@example.com/p.json"} {
		if _, err := parseSource(src); err == nil {
			t.Errorf("source %q accepted", src)
		}
	}
	l, err := parseSource("github:acme/tessera-redis")
	if err != nil || l.String() != "https://github.com/acme/tessera-redis/releases/latest/download/tessera-plugin.json" {
		t.Fatalf("%v %v", l, err)
	}
	l, err = parseSource("github:acme/tessera-redis@v1.2.0")
	if err != nil || l.String() != "https://github.com/acme/tessera-redis/releases/download/v1.2.0/tessera-plugin.json" {
		t.Fatalf("%v %v", l, err)
	}
}

func TestRedirectToHTTPRefused(t *testing.T) {
	r := newRelease(t)
	in := r.installer(t)
	r.publish(t, "redis", true, nil)
	src := r.srv.URL + "/redirect-http/redis/" + DescriptorFile
	if _, err := in.Resolve(context.Background(), "redis", Options{From: src}); err == nil || !strings.Contains(err.Error(), "not https") {
		t.Fatalf("err = %v", err)
	}
}

func TestIndex(t *testing.T) {
	r := newRelease(t)
	in := r.installer(t)
	src := r.publish(t, "redis", true, nil)
	r.files["/index.json"] = []byte(`{"plugins":{"redis":"` + src + `","local":"/tmp/x"}}`)
	in.Indexes = []string{r.srv.URL + "/missing.json", r.srv.URL + "/index.json"}
	p, err := in.Resolve(context.Background(), "redis", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Source != src {
		t.Fatal(p.Source)
	}
	if _, err := in.Resolve(context.Background(), "local", Options{AllowUnsigned: true}); err == nil || !strings.Contains(err.Error(), "local source") {
		t.Fatalf("err = %v", err)
	}
	if _, err := in.Resolve(context.Background(), "nothing", Options{}); err == nil || !strings.Contains(err.Error(), "no source") {
		t.Fatalf("err = %v", err)
	}
}

func TestLocalBinaryAndForce(t *testing.T) {
	in := &Installer{Root: t.TempDir()}
	bin := filepath.Join(t.TempDir(), "build")
	os.WriteFile(bin, fakeBinary, 0o700)
	if _, err := install(t, in, "es", Options{From: bin}); err == nil {
		t.Fatal("bare binary installed without --allow-unsigned")
	}
	rec, err := install(t, in, "es", Options{From: bin, AllowUnsigned: true})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Signed() || rec.Version != "local" {
		t.Fatalf("record = %+v", rec)
	}
	if _, err := in.Resolve(context.Background(), "es", Options{From: bin, AllowUnsigned: true}); err == nil {
		t.Fatal("reinstall without --force")
	}
	if _, err := install(t, in, "es", Options{From: bin, AllowUnsigned: true, Force: true}); err != nil {
		t.Fatal(err)
	}
	if err := Remove(in.Root, "es"); err != nil {
		t.Fatal(err)
	}
	if recs, _ := List(in.Root); len(recs) != 0 {
		t.Fatal("not removed")
	}
}

func TestPluginClaimingAnotherNameRefused(t *testing.T) {
	in := &Installer{Root: t.TempDir()}
	bin := filepath.Join(t.TempDir(), "build")
	os.WriteFile(bin, fakeBinary, 0o700)
	// The fake plugin calls itself postgres when installed under a name starting with liar.
	_, err := install(t, in, "liar", Options{From: bin, AllowUnsigned: true})
	if err == nil || !strings.Contains(err.Error(), "calls itself") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadSkipsBuiltinNames(t *testing.T) {
	in := &Installer{Root: t.TempDir()}
	bin := filepath.Join(t.TempDir(), "build")
	os.WriteFile(bin, fakeBinary, 0o700)
	if _, err := install(t, in, "pgx", Options{From: bin, AllowUnsigned: true}); err != nil {
		t.Fatal(err)
	}
	// A later tess may build in a tile with the same name; the built-in wins.
	tiles, errs := Load(in.Root, []string{"pgx"})
	if len(tiles) != 0 || len(errs) != 1 {
		t.Fatalf("tiles %v, errs %v", tiles, errs)
	}
}

func TestSignFile(t *testing.T) {
	dir := t.TempDir()
	pub, priv, _ := Keygen()
	keyFile := filepath.Join(dir, "key")
	os.WriteFile(keyFile, priv, 0o600)
	desc := filepath.Join(dir, DescriptorFile)
	b, _ := json.Marshal(Descriptor{Name: "redis", Version: "1", Artifacts: map[string]Artifact{}})
	os.WriteFile(desc, b, 0o644)
	if err := SignFile(keyFile, desc); err != nil {
		t.Fatal(err)
	}
	sig, _ := os.ReadFile(desc + ".sig")
	if who, err := verify(map[string]string{"me": pub}, b, sig); err != nil || who != "me" {
		t.Fatalf("%q %v", who, err)
	}
}
