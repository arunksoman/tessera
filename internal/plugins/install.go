package plugins

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/arunksoman/tessera/internal/version"
	"github.com/arunksoman/tessera/tile"
)

// DescriptorFile is the descriptor's file name in a GitHub release.
const DescriptorFile = "tessera-plugin.json"

const (
	maxDescriptor = 1 << 20
	maxSignature  = 4 << 10
	maxIndex      = 1 << 20
	// MaxBinary caps a plugin download.
	MaxBinary = 512 << 20
)

// Descriptor says what one plugin release is and where its binaries are. Its detached
// signature is the file next to it with ".sig" appended.
type Descriptor struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	APIVersion  int    `json:"api_version"`
	Description string `json:"description,omitempty"`
	Homepage    string `json:"homepage,omitempty"`
	// MinTess and MaxTess bound the tess versions this release works with, both inclusive,
	// and are checked before anything is downloaded. The binary's manifest repeats them and
	// is checked again at every start. Prefer MinTess alone.
	MinTess string `json:"min_tess,omitempty"`
	MaxTess string `json:"max_tess,omitempty"`
	// Artifacts maps "GOOS/GOARCH" to a binary. A relative URL is resolved against the
	// descriptor's own location.
	Artifacts map[string]Artifact `json:"artifacts"`
}

type Artifact struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

var hexSHA256 = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

func parseDescriptor(b []byte) (Descriptor, error) {
	var d Descriptor
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return d, fmt.Errorf("plugin descriptor: %w", err)
	}
	if !validName.MatchString(d.Name) {
		return d, fmt.Errorf("plugin descriptor: bad name %q", d.Name)
	}
	for platform, a := range d.Artifacts {
		if !hexSHA256.MatchString(a.SHA256) || a.URL == "" {
			return d, fmt.Errorf("plugin descriptor: artifact %s needs a url and a sha256", platform)
		}
	}
	return d, nil
}

// Index maps plugin names to sources.
type Index struct {
	Plugins map[string]string `json:"plugins"`
}

// Installer resolves, verifies, and installs plugins.
type Installer struct {
	Root        string            // plugin directory
	TrustedKeys map[string]string // publisher name to base64 Ed25519 public key
	Sources     map[string]string // plugin name to source
	Indexes     []string          // https URLs of indexes
	Builtins    []string          // tile IDs a plugin may not take
	TessVersion string            // this tess, checked against a plugin's bounds
	// HTTP is the client for downloads. Redirects are always limited to https.
	HTTP *http.Client
	// Platform defaults to the running GOOS/GOARCH.
	Platform string
}

// Options are the user's choices for one install.
type Options struct {
	From          string // overrides the configured source
	AllowUnsigned bool
	SHA256        string // optional pin for the binary
	Force         bool   // replace an installed plugin
}

// Pending is a verified descriptor, ready to show the user before anything runs.
type Pending struct {
	Name       string
	Source     string
	Descriptor Descriptor
	Publisher  string // empty when unsigned
	Artifact   Artifact
	From       string // where the binary comes from
	artifact   location
}

// location is an https URL or a local path.
type location struct {
	u    *url.URL
	path string
}

func (l location) String() string {
	if l.u != nil {
		return l.u.String()
	}
	return l.path
}

func (l location) remote() bool { return l.u != nil }

var githubSource = regexp.MustCompile(`^github:([A-Za-z0-9](?:[A-Za-z0-9-]{0,38}))/([A-Za-z0-9._-]{1,100})(?:@([A-Za-z0-9._+-]{1,128}))?$`)

// parseSource accepts github:OWNER/REPO[@TAG], an https URL of a descriptor, or a local path,
// optionally prefixed with file:.
func parseSource(s string) (location, error) {
	switch {
	case strings.HasPrefix(s, "github:"):
		m := githubSource.FindStringSubmatch(s)
		if m == nil || m[2] == "." || m[2] == ".." {
			return location{}, fmt.Errorf("source %q: want github:OWNER/REPO or github:OWNER/REPO@TAG", s)
		}
		release := "latest/download"
		if m[3] != "" {
			release = "download/" + url.PathEscape(m[3])
		}
		u, err := url.Parse("https://github.com/" + m[1] + "/" + m[2] + "/releases/" + release + "/" + DescriptorFile)
		return location{u: u}, err
	case strings.HasPrefix(s, "https://"):
		u, err := url.Parse(s)
		if err != nil || u.Host == "" {
			return location{}, fmt.Errorf("source %q is not a valid URL", s)
		}
		if u.User != nil {
			return location{}, fmt.Errorf("source %q must not embed credentials", s)
		}
		return location{u: u}, nil
	case strings.HasPrefix(s, "file:"):
		s = strings.TrimPrefix(s, "file:")
	case strings.Contains(s, "://"):
		return location{}, fmt.Errorf("source %q: only https, github:, and local paths are allowed", s)
	}
	if s == "" {
		return location{}, errors.New("empty source")
	}
	p, err := filepath.Abs(s)
	return location{path: p}, err
}

// resolveRef finds ref relative to base. A remote base may refer only to https.
func resolveRef(base location, ref string) (location, error) {
	if strings.HasPrefix(ref, "https://") {
		return parseSource(ref)
	}
	if base.remote() {
		r, err := url.Parse(ref)
		if err != nil {
			return location{}, err
		}
		u := base.u.ResolveReference(r)
		if u.Scheme != "https" {
			return location{}, fmt.Errorf("artifact %q is not https", ref)
		}
		return location{u: u}, nil
	}
	if strings.Contains(ref, "://") {
		return location{}, fmt.Errorf("artifact %q: only https or a path is allowed", ref)
	}
	if filepath.IsAbs(ref) {
		return location{path: ref}, nil
	}
	return location{path: filepath.Join(filepath.Dir(base.path), filepath.FromSlash(ref))}, nil
}

var errNotFound = errors.New("not found")

func (in *Installer) client() *http.Client {
	c := http.Client{Timeout: 10 * time.Minute}
	if in.HTTP != nil {
		c = *in.HTTP
	}
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		if req.URL.Scheme != "https" {
			return fmt.Errorf("refusing redirect to %s, which is not https", req.URL.Redacted())
		}
		return nil
	}
	return &c
}

func (in *Installer) open(ctx context.Context, l location) (io.ReadCloser, error) {
	if !l.remote() {
		f, err := os.Open(l.path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%s: %w", l.path, errNotFound)
		}
		return f, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "tess-plugin-installer")
	resp, err := in.client().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: %w", l.u.Redacted(), errNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: %s", l.u.Redacted(), resp.Status)
	}
	return resp.Body, nil
}

func (in *Installer) fetch(ctx context.Context, l location, max int64) ([]byte, error) {
	rc, err := in.open(ctx, l)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes", l, max)
	}
	return b, nil
}

func (in *Installer) tess() string {
	if in.TessVersion == "" {
		return version.Dev
	}
	return in.TessVersion
}

func (in *Installer) platform() string {
	if in.Platform != "" {
		return in.Platform
	}
	return runtime.GOOS + "/" + runtime.GOARCH
}

// source finds where name comes from: the --from option, the config, then each index.
func (in *Installer) source(ctx context.Context, name string, opt Options) (string, bool, error) {
	if opt.From != "" {
		return opt.From, false, nil
	}
	if s := in.Sources[name]; s != "" {
		return s, false, nil
	}
	var errs []error
	for _, ix := range in.Indexes {
		l, err := parseSource(ix)
		if err == nil && !l.remote() {
			err = errors.New("an index must be an https URL")
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("index %s: %w", ix, err))
			continue
		}
		b, err := in.fetch(ctx, l, maxIndex)
		if err != nil {
			errs = append(errs, fmt.Errorf("index %s: %w", ix, err))
			continue
		}
		var idx Index
		if err := json.Unmarshal(b, &idx); err != nil {
			errs = append(errs, fmt.Errorf("index %s: %w", ix, err))
			continue
		}
		if s := idx.Plugins[name]; s != "" {
			return s, true, nil
		}
	}
	err := fmt.Errorf("no source for plugin %q: use --from, or add it under [plugins.sources] or [plugins] indexes in config.toml", name)
	if len(errs) > 0 {
		err = fmt.Errorf("%w (%w)", err, errors.Join(errs...))
	}
	return "", false, err
}

// Resolve finds and verifies a plugin's descriptor. Nothing is downloaded beyond the
// descriptor and its signature, and nothing runs.
func (in *Installer) Resolve(ctx context.Context, name string, opt Options) (*Pending, error) {
	if err := checkName(name, in.Builtins); err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(in.Root, name)); err == nil && !opt.Force {
		return nil, fmt.Errorf("plugin %q is already installed; use --force to replace it", name)
	}
	src, fromIndex, err := in.source(ctx, name, opt)
	if err != nil {
		return nil, err
	}
	l, err := parseSource(src)
	if err != nil {
		return nil, err
	}
	if fromIndex && !l.remote() {
		return nil, fmt.Errorf("index gave a local source %q for %s, which is refused", src, name)
	}
	if opt.SHA256 != "" && !hexSHA256.MatchString(opt.SHA256) {
		return nil, errors.New("--sha256 must be 64 hex digits")
	}
	p := &Pending{Name: name, Source: src}
	if !l.remote() {
		head, err := peek(l.path, 64)
		if err != nil {
			return nil, err
		}
		if !looksLikeJSON(head) {
			return in.localBinary(p, l, opt)
		}
	}
	raw, err := in.fetch(ctx, l, maxDescriptor)
	if err != nil {
		return nil, fmt.Errorf("reading plugin descriptor: %w", err)
	}
	d, err := parseDescriptor(raw)
	if err != nil {
		return nil, err
	}
	sigLoc := l
	if l.remote() {
		u := *l.u
		u.Path += ".sig"
		u.RawPath = ""
		sigLoc = location{u: &u}
	} else {
		sigLoc.path += ".sig"
	}
	sig, err := in.fetch(ctx, sigLoc, maxSignature)
	switch {
	case err == nil:
		// A signature that fails is refused even with --allow-unsigned: it means tampering
		// or the wrong key, not a plugin that was never signed.
		if p.Publisher, err = verify(in.TrustedKeys, raw, sig); err != nil {
			return nil, fmt.Errorf("plugin %s: %w", name, err)
		}
	case errors.Is(err, errNotFound):
		if !opt.AllowUnsigned {
			return nil, fmt.Errorf("plugin %s is not signed; install it only if you trust its source, with --allow-unsigned", name)
		}
	default:
		return nil, fmt.Errorf("reading signature: %w", err)
	}
	if d.Name != name {
		return nil, fmt.Errorf("source for %q describes plugin %q", name, d.Name)
	}
	if d.APIVersion != tile.APIVersion {
		return nil, fmt.Errorf("plugin %s %s needs tile API %d, this tess has %d", name, d.Version, d.APIVersion, tile.APIVersion)
	}
	if err := version.Supports(in.tess(), d.MinTess, d.MaxTess); err != nil {
		return nil, fmt.Errorf("plugin %s %s %w", name, d.Version, err)
	}
	a, ok := d.Artifacts[in.platform()]
	if !ok {
		return nil, fmt.Errorf("plugin %s %s has no build for %s", name, d.Version, in.platform())
	}
	if opt.SHA256 != "" && !strings.EqualFold(opt.SHA256, a.SHA256) {
		return nil, fmt.Errorf("plugin %s: sha256 %s does not match --sha256", name, a.SHA256)
	}
	al, err := resolveRef(l, a.URL)
	if err != nil {
		return nil, err
	}
	p.Descriptor, p.Artifact, p.artifact, p.From = d, a, al, al.String()
	return p, nil
}

// peek reads up to n bytes from the start of a file.
func peek(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b := make([]byte, n)
	m, err := io.ReadFull(f, b)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return b[:m], nil
}

// localBinary installs a binary built on this machine, which no one signed.
func (in *Installer) localBinary(p *Pending, l location, opt Options) (*Pending, error) {
	if !opt.AllowUnsigned {
		return nil, fmt.Errorf("%s is a bare binary, not a signed descriptor; install it with --allow-unsigned", l)
	}
	sum, err := hashFile(l.path)
	if err != nil {
		return nil, err
	}
	if opt.SHA256 != "" && !strings.EqualFold(opt.SHA256, sum) {
		return nil, fmt.Errorf("%s: sha256 %s does not match --sha256", l, sum)
	}
	p.Descriptor = Descriptor{Name: p.Name, Version: "local", APIVersion: tile.APIVersion}
	p.Artifact = Artifact{URL: l.path, SHA256: sum}
	p.artifact, p.From = l, l.path
	return p, nil
}

// Install downloads the binary, checks its hash, starts it once to read its manifest, and
// only then puts it in place.
func (in *Installer) Install(ctx context.Context, p *Pending) (Record, error) {
	rec := Record{Name: p.Name, Version: p.Descriptor.Version, Source: p.Source, Publisher: p.Publisher,
		SHA256: strings.ToLower(p.Artifact.SHA256), Binary: binaryName(p.Name), InstalledAt: time.Now().UTC()}
	if err := os.MkdirAll(in.Root, 0o700); err != nil {
		return rec, err
	}
	stage, err := os.MkdirTemp(in.Root, ".stage-")
	if err != nil {
		return rec, err
	}
	defer os.RemoveAll(stage)
	bin := filepath.Join(stage, rec.Binary)
	if err := in.download(ctx, p.artifact, bin, rec.SHA256); err != nil {
		return rec, err
	}
	proc, h, err := launch(ctx, bin, stage, in.tess())
	if err != nil {
		return rec, fmt.Errorf("checking plugin %s: %w", p.Name, err)
	}
	proc.stop()
	if h.Manifest.ID != p.Name {
		return rec, fmt.Errorf("plugin binary for %q calls itself %q", p.Name, h.Manifest.ID)
	}
	if err := version.Supports(in.tess(), h.Manifest.MinTess, h.Manifest.MaxTess); err != nil {
		return rec, fmt.Errorf("plugin %s %w", p.Name, err)
	}
	rec.Manifest, rec.ConfigWarner = h.Manifest, h.Facets.ConfigWarner
	if err := writeRecord(stage, rec); err != nil {
		return rec, err
	}
	final := filepath.Join(in.Root, p.Name)
	var old string
	if _, err := os.Stat(final); err == nil {
		old = filepath.Join(in.Root, ".old-"+p.Name+"-"+rand.Text())
		if err := os.Rename(final, old); err != nil {
			return rec, fmt.Errorf("replacing plugin %s: %w", p.Name, err)
		}
	}
	if err := os.Rename(stage, final); err != nil {
		if old != "" {
			os.Rename(old, final)
		}
		return rec, err
	}
	if old != "" {
		os.RemoveAll(old)
	}
	return rec, nil
}

func (in *Installer) download(ctx context.Context, from location, to, want string) error {
	rc, err := in.open(ctx, from)
	if err != nil {
		return fmt.Errorf("downloading plugin: %w", err)
	}
	defer rc.Close()
	f, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(rc, MaxBinary+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("downloading plugin: %w", err)
	}
	if n > MaxBinary {
		return fmt.Errorf("plugin binary is larger than %d bytes", MaxBinary)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("plugin binary sha256 is %s, but the descriptor says %s", got, want)
	}
	return nil
}
