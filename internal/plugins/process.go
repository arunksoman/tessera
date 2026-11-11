// Package plugins installs third-party tiles and runs each in its own process.
//
// A plugin is not sandboxed: it runs with the user's permissions. What this package
// guarantees is that only a binary the user chose to install runs, byte for byte, and that
// a plugin sees only the secrets of connections opened through it.
package plugins

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/arunksoman/tessera/internal/version"
	"github.com/arunksoman/tessera/tile"
	"github.com/arunksoman/tessera/tile/plugin"
)

// handshakeTimeout bounds how long a starting plugin may take to say what it is.
const handshakeTimeout = 15 * time.Second

// passEnv lists the environment variables a plugin process receives. Everything else, such
// as TESSERA_MASTER_KEY_FILE or cloud credentials, stays with tess.
var passEnv = []string{
	"PATH", "HOME", "USER", "USERPROFILE", "SYSTEMROOT", "SYSTEMDRIVE", "WINDIR", "COMSPEC",
	"TEMP", "TMP", "TMPDIR", "APPDATA", "LOCALAPPDATA", "PROGRAMDATA",
	"LANG", "LC_ALL", "LC_CTYPE", "TZ",
	"SSL_CERT_FILE", "SSL_CERT_DIR",
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
	"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR",
}

func pluginEnv() []string {
	var out []string
	for _, k := range passEnv {
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// tail keeps the last bytes a plugin wrote to standard error, for error messages.
type tail struct {
	mu  sync.Mutex
	buf []byte
}

const tailMax = 4096

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > tailMax {
		t.buf = t.buf[len(t.buf)-tailMax:]
	}
	return len(p), nil
}

// String returns the tail without control characters, since it may reach a terminal.
func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r >= ' ' && r != 0x7f && !(r >= 0x80 && r < 0xa0) {
			return r
		}
		return -1
	}, string(t.buf)))
}

// proc is one running plugin process.
type proc struct {
	client *plugin.Client
	cmd    *exec.Cmd
	stdin  io.Closer
	stderr *tail
	exited chan struct{}
}

// launch starts the binary at path and completes the handshake, telling it this tess's
// version.
func launch(ctx context.Context, path, dir, tess string) (*proc, plugin.HandshakeReply, error) {
	// os.Pipe rather than StdinPipe and StdoutPipe, because Wait closes those while the
	// client may still be reading.
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, plugin.HandshakeReply{}, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, plugin.HandshakeReply{}, err
	}
	p := &proc{stderr: &tail{}, stdin: inW, exited: make(chan struct{})}
	p.cmd = exec.Command(path)
	p.cmd.Dir = dir
	p.cmd.Env = pluginEnv()
	p.cmd.Stdin, p.cmd.Stdout, p.cmd.Stderr = inR, outW, p.stderr
	err = p.cmd.Start()
	inR.Close()
	outW.Close()
	if err != nil {
		inW.Close()
		outR.Close()
		return nil, plugin.HandshakeReply{}, fmt.Errorf("starting plugin: %w", err)
	}
	go func() {
		p.cmd.Wait()
		outR.Close()
		close(p.exited)
	}()
	p.client = plugin.NewClient(outR, inW)
	hctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	h, err := p.client.Handshake(hctx, tess)
	if err != nil {
		p.stop()
		return nil, h, p.explain(err)
	}
	return p, h, nil
}

// explain adds what the plugin printed before failing.
func (p *proc) explain(err error) error {
	if s := p.stderr.String(); s != "" {
		return fmt.Errorf("%w (plugin output: %s)", err, s)
	}
	return err
}

// stop closes the plugin's input, which ends a well-behaved plugin, then kills it.
func (p *proc) stop() {
	p.stdin.Close()
	select {
	case <-p.exited:
	case <-time.After(3 * time.Second):
		p.cmd.Process.Kill()
		<-p.exited
	}
}

func (p *proc) alive() bool {
	select {
	case <-p.exited:
		return false
	case <-p.client.Done():
		return false
	default:
		return true
	}
}

// Tile is an installed plugin. Its process starts on first use, after its binary is checked
// against the hash pinned at install.
type Tile struct {
	rec  Record
	dir  string
	tess string // this tess's version
	mu   sync.Mutex
	proc *proc
}

func (t *Tile) Manifest() tile.Manifest { return t.rec.Manifest }

// Record returns what was installed.
func (t *Tile) Record() Record { return t.rec }

func (t *Tile) Open(ctx context.Context, cfg tile.ConnConfig, env tile.Env) (tile.Conn, error) {
	p, err := t.start(ctx)
	if err != nil {
		return nil, err
	}
	c, err := p.client.Open(ctx, cfg, env)
	if err != nil && !p.alive() {
		return nil, p.explain(fmt.Errorf("plugin %s exited: %w", t.rec.Name, err))
	}
	return c, err
}

func (t *Tile) start(ctx context.Context) (*proc, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.proc != nil && t.proc.alive() {
		return t.proc, nil
	}
	path := t.rec.binaryPath(t.dir)
	if err := checkHash(path, t.rec.SHA256); err != nil {
		return nil, fmt.Errorf("plugin %s: %w; reinstall it with tess plugin install --force", t.rec.Name, err)
	}
	p, h, err := launch(ctx, path, t.rec.pluginDir(t.dir), t.tess)
	if err != nil {
		return nil, fmt.Errorf("plugin %s: %w", t.rec.Name, err)
	}
	if h.Manifest.ID != t.rec.Name {
		p.stop()
		return nil, fmt.Errorf("plugin %s now calls itself %q; reinstall it", t.rec.Name, h.Manifest.ID)
	}
	// The binary is the authority, since the record may predate a tess upgrade.
	if err := version.Supports(t.tess, h.Manifest.MinTess, h.Manifest.MaxTess); err != nil {
		p.stop()
		return nil, fmt.Errorf("plugin %s %w", t.rec.Name, err)
	}
	t.proc = p
	return p, nil
}

// Close stops the plugin process, if running.
func (t *Tile) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.proc != nil {
		t.proc.stop()
		t.proc = nil
	}
}

// warnTile is a Tile whose plugin implements tile.ConfigWarner.
type warnTile struct{ *Tile }

func (t warnTile) ConfigWarnings(cfg tile.ConnConfig) []string {
	p, err := t.start(context.Background())
	if err != nil {
		return []string{err.Error()}
	}
	return p.client.ConfigWarnings(cfg)
}

var errHash = errors.New("binary does not match the hash pinned at install")

func checkHash(path, want string) error {
	got, err := hashFile(path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, want) {
		return errHash
	}
	return nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// looksLikeJSON reports whether b starts like a JSON object.
func looksLikeJSON(b []byte) bool {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	return bytes.HasPrefix(bytes.TrimLeft(b, " \t\r\n"), []byte("{"))
}
