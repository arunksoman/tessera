// Package server implements tess serve: the HTTP front-end for the web app.
//
// It binds to loopback by default and admits a browser through a one-time
// URL that carries a random per-launch token. The token is exchanged for a
// session cookie and cannot be used again.
package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/arunksoman/tessera/internal/engine"
)

// Config says where and how tess serve listens.
type Config struct {
	// Addr is host:port. A port of 0 picks a free one.
	Addr string
	// CertFile and KeyFile turn on TLS. They go together.
	CertFile, KeyFile string
	// BehindProxy says a TLS-terminating proxy sits in front, so a
	// non-loopback bind is allowed without CertFile.
	BehindProxy bool
	// AllowedHosts are extra names accepted in the Host header.
	AllowedHosts []string
	// Assets is the built frontend, served at the root. Without an index.html, a page
	// says how to build it.
	Assets fs.FS
	// Engine runs everything behind /api.
	Engine *engine.Engine
}

// Server is one launch of tess serve.
type Server struct {
	cfg      Config
	tls      *tls.Config
	loopback bool
	hosts    map[string]bool
	token    string
	api      *api
	maxRows  int
	// scriptHashes allow the frontend's inline bootstrap scripts under the CSP.
	scriptHashes []string
	// styleHashes allow the static style attributes in the frontend's own bundle, such as
	// SvelteKit's route announcer. No database value reaches a style attribute.
	styleHashes []string

	mu        sync.Mutex
	tokenUsed bool
	sessions  map[[32]byte]bool
}

// New checks the binding rules and prepares a launch token.
func New(cfg Config) (*Server, error) {
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("bad address %q: %w", cfg.Addr, err)
	}
	if (cfg.CertFile == "") != (cfg.KeyFile == "") {
		return nil, errors.New("--tls-cert and --tls-key go together")
	}
	if cfg.Assets == nil {
		return nil, errors.New("no frontend assets")
	}
	s := &Server{cfg: cfg, hosts: map[string]bool{}, sessions: map[[32]byte]bool{}}

	ip := net.ParseIP(host)
	s.loopback = strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())
	secure := cfg.CertFile != "" || cfg.BehindProxy
	if !s.loopback && !secure {
		return nil, fmt.Errorf("refusing to serve plain HTTP on %q: bind to 127.0.0.1, or add TLS with --tls-cert and --tls-key, or put a TLS proxy in front and pass --behind-proxy", cfg.Addr)
	}

	if s.loopback {
		for _, h := range []string{"localhost", "127.0.0.1", "::1"} {
			s.hosts[h] = true
		}
	}
	if host != "" && (ip == nil || !ip.IsUnspecified()) {
		s.hosts[normalHost(host)] = true
	}
	for _, h := range cfg.AllowedHosts {
		s.hosts[normalHost(h)] = true
	}
	if len(s.hosts) == 0 {
		return nil, fmt.Errorf("listening on every interface (%q) needs at least one --allow-host NAME, the name browsers will use", cfg.Addr)
	}

	if cfg.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load TLS certificate: %w", err)
		}
		s.tls = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	}

	if cfg.Engine != nil {
		s.api = newAPI(cfg.Engine)
		s.api.allowFiles = s.loopback
	}
	s.maxRows = defaultMaxRows
	s.scriptHashes = inlineScriptHashes(cfg.Assets)
	s.styleHashes = styleAttrHashes(cfg.Assets)
	s.token = randomString()
	return s, nil
}

func randomString() string {
	b := make([]byte, 32)
	rand.Read(b) // never returns an error; it panics if the system source fails
	return base64.RawURLEncoding.EncodeToString(b)
}

// normalHost lowercases a host name and strips a trailing dot and IPv6 brackets.
func normalHost(h string) string {
	h = strings.TrimSuffix(strings.ToLower(h), ".")
	return strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
}

func (s *Server) secure() bool { return s.tls != nil || s.cfg.BehindProxy }

func (s *Server) cookieName() string {
	if s.secure() {
		return "__Host-tess-session"
	}
	return "tess-session"
}

// tokenFresh reports whether the launch token is still unused.
func (s *Server) tokenFresh() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.tokenUsed
}

// LaunchURL is the one-time URL for a server listening on listenAddr.
func (s *Server) LaunchURL(listenAddr string) string {
	bindHost, _, _ := net.SplitHostPort(s.cfg.Addr)
	_, port, _ := net.SplitHostPort(listenAddr)
	name := bindHost
	switch {
	case len(s.cfg.AllowedHosts) > 0:
		name = s.cfg.AllowedHosts[0]
	case s.loopback && name == "":
		name = "127.0.0.1"
	}
	if s.cfg.BehindProxy {
		// The proxy's public port is unknown, so assume the default.
		return "https://" + name + "/auth?token=" + s.token
	}
	scheme := "http"
	if s.tls != nil {
		scheme = "https"
	}
	return scheme + "://" + net.JoinHostPort(normalHost(name), port) + "/auth?token=" + s.token
}

// Handler is the whole HTTP surface, with every protection applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth", s.exchange)
	if s.api != nil {
		s.routes(mux)
	}
	mux.Handle("/", s.static())
	return s.headers(s.checkHost(http.NewCrossOriginProtection().Handler(s.requireSession(mux))))
}

func (s *Server) headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", s.csp(""))
		h.Set("X-Content-Type-Options", "nosniff")
		// The launch URL carries the token, so it must never leak as a referrer.
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Cache-Control", "no-store")
		if s.secure() {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

// checkHost defeats DNS rebinding: a page on another name that resolves to
// this address still sends its own name in Host.
func (s *Server) checkHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if host == "" || !s.hosts[normalHost(host)] {
			http.Error(w, "unknown host", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) exchange(w http.ResponseWriter, r *http.Request) {
	given := r.URL.Query().Get("token")
	s.mu.Lock()
	ok := !s.tokenUsed && subtle.ConstantTimeCompare([]byte(given), []byte(s.token)) == 1
	var session string
	if ok {
		s.tokenUsed = true
		session = randomString()
		s.sessions[sha256.Sum256([]byte(session))] = true
	}
	s.mu.Unlock()
	if !ok {
		http.Error(w, "This link is invalid or was already used. Restart tess serve for a new one.", http.StatusForbidden)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: s.cookieName(), Value: session, Path: "/",
		HttpOnly: true, Secure: s.secure(), SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

type sessionKey struct{}

// requireSession admits only browsers that exchanged the launch token, and puts the session
// in the request context, so per-browser state such as editor tabs stays apart.
func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth" {
			next.ServeHTTP(w, r)
			return
		}
		c, err := r.Cookie(s.cookieName())
		ok := false
		var id [32]byte
		if err == nil {
			id = sha256.Sum256([]byte(c.Value))
			s.mu.Lock()
			ok = s.sessions[id]
			s.mu.Unlock()
		}
		if !ok {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeError(w, http.StatusUnauthorized, "signed_out", "not signed in: open the link that tess serve printed")
			} else {
				http.Error(w, "Not signed in. Open the link that tess serve printed.", http.StatusUnauthorized)
			}
			return
		}
		ctx := context.WithValue(r.Context(), sessionKey{}, base64.RawURLEncoding.EncodeToString(id[:]))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Run listens, prints the launch URL to out, and serves until ctx ends.
// ready, if not nil, gets the launch URL once the server accepts connections.
func (s *Server) Run(ctx context.Context, out io.Writer, ready func(url string)) error {
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return err
	}
	addr := ln.Addr().String()
	if s.tls != nil {
		ln = tls.NewListener(ln, s.tls)
	}
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	defer s.Close()
	if s.api != nil {
		go func() {
			t := time.NewTicker(time.Minute)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case now := <-t.C:
					s.api.sweep(now)
				}
			}
		}()
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	link := s.LaunchURL(addr)
	fmt.Fprintf(out, "tess serve: open %s\n", link)
	if ready != nil {
		ready(link)
	}
	fmt.Fprintln(out, "The link works once. Press Ctrl+C to stop.")

	select {
	case err := <-served:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		return err
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// static serves the frontend. Paths without a file get index.html, so the app's own
// routes survive a reload.
func (s *Server) static() http.Handler {
	files := http.FileServerFS(s.cfg.Assets)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := fs.Stat(s.cfg.Assets, "index.html"); err != nil {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			fmt.Fprintln(w, "tess serve is running, but this build has no web app.")
			fmt.Fprintln(w, "Build it with scripts/build-web.sh, then rebuild tess.")
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" || name == "index.html" {
			s.index(w)
			return
		}
		if _, err := fs.Stat(s.cfg.Assets, name); err != nil {
			s.index(w)
			return
		}
		files.ServeHTTP(w, r)
	})
}

var inlineScript = regexp.MustCompile(`(?is)<script([^>]*)>(.*?)</script>`)

// inlineScriptHashes finds inline scripts in index.html, such as SvelteKit's bootstrap and
// the theme script that must run before first paint, so the CSP can allow exactly those.
func inlineScriptHashes(assets fs.FS) []string {
	if assets == nil {
		return nil
	}
	html, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		return nil
	}
	var out []string
	for _, m := range inlineScript.FindAllSubmatch(html, -1) {
		if strings.Contains(strings.ToLower(string(m[1])), "src=") {
			continue
		}
		// Browsers hash the script after HTML newline normalization.
		text := bytes.ReplaceAll(bytes.ReplaceAll(m[2], []byte("\r\n"), []byte("\n")), []byte("\r"), []byte("\n"))
		sum := sha256.Sum256(text)
		out = append(out, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
	}
	return out
}

// csp is the policy for every response. A nonce, set only on index.html, lets the editor
// add its own style element; it is fresh for each response.
func (s *Server) csp(nonce string) string {
	nonceSrc := ""
	if nonce != "" {
		nonceSrc = " 'nonce-" + nonce + "'"
	}
	return "default-src 'none'; script-src 'self'" + s.scriptHashesCSP() + "; style-src 'self'" + nonceSrc + s.styleHashesCSP() + "; img-src 'self' data:; font-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
}

func (s *Server) scriptHashesCSP() string {
	if len(s.scriptHashes) == 0 {
		return ""
	}
	return " " + strings.Join(s.scriptHashes, " ")
}

var styleAttr = regexp.MustCompile(`style="([^"]*)"`)

// styleAttrHashes hashes the literal style attributes in the bundle's scripts, for CSP
// 'unsafe-hashes', which allows exactly those values and nothing else.
func styleAttrHashes(assets fs.FS) []string {
	if assets == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	fs.WalkDir(assets, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".js") {
			return nil
		}
		b, err := fs.ReadFile(assets, path)
		if err != nil {
			return nil
		}
		for _, m := range styleAttr.FindAllSubmatch(b, -1) {
			sum := sha256.Sum256(m[1])
			h := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
			if !seen[h] {
				seen[h] = true
				out = append(out, h)
			}
		}
		return nil
	})
	return out
}

func (s *Server) styleHashesCSP() string {
	if len(s.styleHashes) == 0 {
		return ""
	}
	return " 'unsafe-hashes' " + strings.Join(s.styleHashes, " ")
}

var noncePlaceholder = []byte(`<meta name="tess-csp-nonce" content="">`)

// index serves the app page with a fresh style nonce, which the page hands to CodeMirror.
func (s *Server) index(w http.ResponseWriter) {
	html, err := fs.ReadFile(s.cfg.Assets, "index.html")
	if err != nil {
		http.Error(w, "no web app", http.StatusInternalServerError)
		return
	}
	nonce := randomString()
	html = bytes.Replace(html, noncePlaceholder, []byte(`<meta name="tess-csp-nonce" content="`+nonce+`">`), 1)
	w.Header().Set("Content-Security-Policy", s.csp(nonce))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(html)
}
