package server

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

var assets = fstest.MapFS{
	"index.html": {Data: []byte("<!doctype html><title>Tessera</title>")},
	"app.js":     {Data: []byte("// placeholder")},
}

func newServer(t *testing.T, cfg Config) *Server {
	t.Helper()
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:0"
	}
	if cfg.Assets == nil {
		cfg.Assets = assets
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func get(h http.Handler, target string, mod ...func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Host = "127.0.0.1:7000"
	for _, m := range mod {
		m(req)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func withCookies(w *httptest.ResponseRecorder) func(*http.Request) {
	return func(r *http.Request) {
		for _, c := range w.Result().Cookies() {
			r.AddCookie(c)
		}
	}
}

func TestBindingRules(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		bad  string // substring of the error, or "" for accepted
	}{
		{"loopback v4", Config{Addr: "127.0.0.1:0"}, ""},
		{"loopback v6", Config{Addr: "[::1]:0"}, ""},
		{"localhost", Config{Addr: "localhost:8080"}, ""},
		{"wildcard without TLS", Config{Addr: ":8080"}, "TLS"},
		{"all interfaces without TLS", Config{Addr: "0.0.0.0:8080"}, "TLS"},
		{"lan address without TLS", Config{Addr: "192.168.1.5:8080"}, "TLS"},
		{"proxy without hosts", Config{Addr: "0.0.0.0:8080", BehindProxy: true}, "--allow-host"},
		{"proxy with host", Config{Addr: "0.0.0.0:8080", BehindProxy: true, AllowedHosts: []string{"db.example.com"}}, ""},
		{"lan address with proxy names itself", Config{Addr: "192.168.1.5:8080", BehindProxy: true}, ""},
		{"TLS needs both files", Config{Addr: "127.0.0.1:0", CertFile: "c.pem"}, "--tls-key"},
		{"bad address", Config{Addr: "nope"}, "address"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.cfg.Assets = assets
			_, err := New(c.cfg)
			switch {
			case c.bad == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case c.bad != "" && err == nil:
				t.Fatal("accepted")
			case c.bad != "" && !strings.Contains(err.Error(), c.bad):
				t.Fatalf("error %q lacks %q", err, c.bad)
			}
		})
	}
}

func TestOneTimeTokenGrantsSession(t *testing.T) {
	s := newServer(t, Config{})
	h := s.Handler()

	if w := get(h, "/"); w.Code != http.StatusUnauthorized {
		t.Fatalf("no session: %d", w.Code)
	}
	if w := get(h, "/auth?token=wrong"); w.Code != http.StatusForbidden {
		t.Fatalf("wrong token: %d", w.Code)
	}

	w := get(h, "/auth?token="+s.token)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Fatalf("exchange: %d %q", w.Code, w.Header().Get("Location"))
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].Path != "/" {
		t.Fatalf("cookie: %+v", cookies)
	}
	if cookies[0].Value == s.token {
		t.Fatal("the session cookie reuses the launch token")
	}

	page := get(h, "/", withCookies(w))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "<title>Tessera</title>") {
		t.Fatalf("index: %d %s", page.Code, page.Body)
	}
	if js := get(h, "/app.js", withCookies(w)); js.Code != http.StatusOK {
		t.Fatalf("asset: %d", js.Code)
	}

	if again := get(h, "/auth?token="+s.token); again.Code != http.StatusForbidden {
		t.Fatalf("token reused: %d", again.Code)
	}
	forged := func(r *http.Request) { r.AddCookie(&http.Cookie{Name: cookies[0].Name, Value: "forged"}) }
	if f := get(h, "/", forged); f.Code != http.StatusUnauthorized {
		t.Fatalf("forged session: %d", f.Code)
	}
}

func TestHostAllowlist(t *testing.T) {
	s := newServer(t, Config{})
	h := s.Handler()
	for _, host := range []string{"127.0.0.1:7000", "localhost:7000", "[::1]:7000", "LOCALHOST"} {
		if w := get(h, "/", func(r *http.Request) { r.Host = host }); w.Code == http.StatusMisdirectedRequest {
			t.Errorf("%s refused", host)
		}
	}
	for _, host := range []string{"evil.example:7000", "127.0.0.1.evil.example", ""} {
		w := get(h, "/auth?token="+s.token, func(r *http.Request) { r.Host = host })
		if w.Code != http.StatusMisdirectedRequest {
			t.Errorf("%q allowed: %d", host, w.Code)
		}
	}
	if !s.tokenFresh() {
		t.Fatal("a refused host consumed the token")
	}

	p := newServer(t, Config{Addr: "0.0.0.0:0", BehindProxy: true, AllowedHosts: []string{"DB.example.com"}})
	if w := get(p.Handler(), "/", func(r *http.Request) { r.Host = "db.example.com" }); w.Code == http.StatusMisdirectedRequest {
		t.Fatal("allowed host refused")
	}
	if w := get(p.Handler(), "/", func(r *http.Request) { r.Host = "localhost" }); w.Code != http.StatusMisdirectedRequest {
		t.Fatal("a non-loopback bind allowed localhost")
	}
}

func TestSecurityHeaders(t *testing.T) {
	s := newServer(t, Config{})
	for _, target := range []string{"/", "/auth?token=wrong", "/auth?token=" + s.token} {
		w := get(s.Handler(), target)
		hd := w.Header()
		csp := hd.Get("Content-Security-Policy")
		for _, d := range []string{"default-src 'none'", "script-src 'self'", "connect-src 'self'", "frame-ancestors 'none'"} {
			if !strings.Contains(csp, d) {
				t.Errorf("%s: CSP %q lacks %q", target, csp, d)
			}
		}
		for k, v := range map[string]string{
			"X-Content-Type-Options": "nosniff",
			"Referrer-Policy":        "no-referrer",
			"X-Frame-Options":        "DENY",
			"Cache-Control":          "no-store",
		} {
			if hd.Get(k) != v {
				t.Errorf("%s: %s = %q", target, k, hd.Get(k))
			}
		}
		if hd.Get("Strict-Transport-Security") != "" {
			t.Errorf("%s: HSTS over plain HTTP", target)
		}
	}
}

func TestCrossOriginWritesRefused(t *testing.T) {
	s := newServer(t, Config{})
	h := s.Handler()
	login := get(h, "/auth?token="+s.token)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Host = "127.0.0.1:7000"
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	withCookies(login)(req)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-site POST: %d", w.Code)
	}
}

func TestSecureCookieBehindProxy(t *testing.T) {
	s := newServer(t, Config{Addr: "0.0.0.0:0", BehindProxy: true, AllowedHosts: []string{"db.example.com"}})
	w := get(s.Handler(), "/auth?token="+s.token, func(r *http.Request) { r.Host = "db.example.com" })
	c := w.Result().Cookies()
	if len(c) != 1 || !c[0].Secure || !strings.HasPrefix(c[0].Name, "__Host-") {
		t.Fatalf("cookie: %+v", c)
	}
	if w.Header().Get("Strict-Transport-Security") == "" {
		t.Fatal("no HSTS behind a TLS proxy")
	}
	if u := s.LaunchURL("0.0.0.0:8080"); u != "https://db.example.com/auth?token="+s.token {
		t.Fatalf("launch URL %q", u)
	}
}

// TestRunServesUntilCancelled starts a real listener, as tess serve does.
func TestRunServesUntilCancelled(t *testing.T) {
	s := newServer(t, Config{})
	pr, pw := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	ready := make(chan string, 1)
	go func() { done <- s.Run(ctx, pw, func(u string) { ready <- u }); pw.Close() }()

	line, err := bufio.NewReader(pr).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	go io.Copy(io.Discard, pr)
	raw := line[strings.Index(line, "http://"):]
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() != "127.0.0.1" || u.Port() == "0" {
		t.Fatalf("printed %q", line)
	}

	if got := <-ready; got != raw {
		t.Fatalf("ready got %q, printed %q", got, raw)
	}

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	resp, err := client.Get(raw)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Tessera") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestRunWithTLS(t *testing.T) {
	certFile, keyFile := writeTestCert(t)
	s := newServer(t, Config{Addr: "0.0.0.0:0", CertFile: certFile, KeyFile: keyFile, AllowedHosts: []string{"localhost"}})
	pr, pw := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { s.Run(ctx, pw, nil); pw.Close() }()
	line, err := bufio.NewReader(pr).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	go io.Copy(io.Discard, pr)
	raw := strings.TrimSpace(line[strings.Index(line, "https://"):])
	u, _ := url.Parse(raw)
	if u.Hostname() != "localhost" {
		t.Fatalf("printed %q", line)
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 5 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // self-signed test certificate
	}}
	resp, err := client.Get(raw)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Strict-Transport-Security") == "" {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
}

func TestStaticAppAndInlineScripts(t *testing.T) {
	boot := "\n\t__sveltekit = {};\n"
	app := fstest.MapFS{
		"index.html":          {Data: []byte("<!doctype html><script src=\"/x.js\"></script><script>" + boot + "</script><title>Tessera</title>")},
		"_app/immutable/a.js": {Data: []byte("export {}")},
		"_app/immutable/b.js": {Data: []byte("var t=`<div style=\"clip: rect(0 0 0 0)\"></div>`")},
	}
	s := newServer(t, Config{Assets: app})
	login := get(s.Handler(), "/auth?token="+s.token)

	sum := sha256.Sum256([]byte(boot))
	want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	w := get(s.Handler(), "/", withCookies(login))
	csp := w.Header().Get("Content-Security-Policy")
	crlf := newServer(t, Config{Assets: fstest.MapFS{"index.html": {Data: []byte("<script>" + strings.ReplaceAll(boot, "\n", "\r\n") + "</script>")}}})
	if !strings.Contains(crlf.csp(""), want) {
		t.Fatalf("CRLF script hashed without newline normalization: %s", crlf.csp(""))
	}
	if !strings.Contains(csp, "script-src 'self' "+want) || strings.Count(csp, "sha256-") != 2 {
		t.Fatalf("CSP %q, want %s", csp, want)
	}
	styleSum := sha256.Sum256([]byte("clip: rect(0 0 0 0)"))
	if want := "'unsafe-hashes' 'sha256-" + base64.StdEncoding.EncodeToString(styleSum[:]) + "';"; !strings.Contains(csp, want) {
		t.Fatalf("CSP %q lacks %q", csp, want)
	}
	if w := get(s.Handler(), "/connections/dev", withCookies(login)); w.Code != 200 || !strings.Contains(w.Body.String(), "<title>Tessera</title>") {
		t.Fatalf("app route: %d", w.Code)
	}
	if w := get(s.Handler(), "/_app/immutable/a.js", withCookies(login)); w.Code != 200 || w.Body.String() != "export {}" {
		t.Fatalf("asset: %d", w.Code)
	}

	bare := newServer(t, Config{Assets: fstest.MapFS{".keep": {}}})
	login = get(bare.Handler(), "/auth?token="+bare.token)
	if w := get(bare.Handler(), "/", withCookies(login)); w.Code != 200 || !strings.Contains(w.Body.String(), "build-web.sh") {
		t.Fatalf("unbuilt: %d %s", w.Code, w.Body)
	}
}

func TestIndexCarriesStyleNonce(t *testing.T) {
	app := fstest.MapFS{"index.html": {Data: []byte(`<head><meta name="tess-csp-nonce" content=""></head>`)}}
	s := newServer(t, Config{Assets: app})
	login := get(s.Handler(), "/auth?token="+s.token)
	nonces := map[string]bool{}
	for _, path := range []string{"/", "/some/route"} {
		w := get(s.Handler(), path, withCookies(login))
		m := regexp.MustCompile(`content="([A-Za-z0-9_-]{20,})"`).FindStringSubmatch(w.Body.String())
		if m == nil {
			t.Fatalf("%s: no nonce in %s", path, w.Body)
		}
		if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "style-src 'self' 'nonce-"+m[1]+"'") {
			t.Fatalf("%s: CSP %q lacks nonce %s", path, csp, m[1])
		}
		nonces[m[1]] = true
	}
	if len(nonces) != 2 {
		t.Fatal("nonce reused across responses")
	}
}
