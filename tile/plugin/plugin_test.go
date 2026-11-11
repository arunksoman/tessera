package plugin

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arunksoman/tessera/tile"
)

// fakeTile streams rows "1".."n" for "rows n", blocks for "sleep", and records secrets.
type fakeTile struct {
	batch     int // rows per batch, 100 if zero
	mu        sync.Mutex
	secret    string
	cancelled bool
	closed    int
	execCtx   context.Context
}

func (f *fakeTile) Manifest() tile.Manifest {
	return tile.Manifest{ID: "fake", Name: "Fake", APIVersion: tile.APIVersion,
		ConnForm: tile.Form{Fields: []tile.FormField{{Name: "host", Label: "Host", Type: "text"}}}}
}

func (f *fakeTile) ConfigWarnings(cfg tile.ConnConfig) []string {
	if cfg.Values["tls"] == "off" {
		return []string{"tls off\x1b[31m"}
	}
	return nil
}

func (f *fakeTile) Open(ctx context.Context, cfg tile.ConnConfig, env tile.Env) (tile.Conn, error) {
	pw, err := env.Secret(ctx, "password")
	if err != nil {
		return nil, err
	}
	if string(pw) != "hunter2" {
		return nil, errors.New("bad password")
	}
	f.mu.Lock()
	f.secret = string(pw)
	f.mu.Unlock()
	return &fakeConn{t: f, env: env}, nil
}

type fakeConn struct {
	t   *fakeTile
	env tile.Env
}

func (c *fakeConn) Capabilities() tile.Capabilities {
	return tile.Capabilities{tile.CapCancel: {Supported: true}}
}
func (c *fakeConn) Ping(context.Context) error { return nil }
func (c *fakeConn) Close() error {
	c.t.mu.Lock()
	c.t.closed++
	c.t.mu.Unlock()
	return nil
}
func (c *fakeConn) Session(context.Context, tile.SessionOptions) (tile.Session, error) {
	return &fakeSession{c: c}, nil
}
func (c *fakeConn) Tree() tile.ObjectTree   { return nil }
func (c *fakeConn) Language() tile.Language { return fakeLang{} }
func (c *fakeConn) Layout(context.Context, tile.ObjectRef) (tile.Layout, error) {
	return tile.Layout{Fields: []tile.Field{{Name: "n", Kind: tile.KindInt}}, Key: []string{"n"}}, nil
}
func (c *fakeConn) BrowseRequest(ref tile.ObjectRef, _ tile.Layout, q tile.BrowseQuery) (tile.Request, error) {
	return tile.Request{Text: "rows " + strconv.Itoa(q.Limit)}, nil
}

type fakeLang struct{}

func (fakeLang) ID() string { return "fake" }
func (fakeLang) Split(text string) []tile.Span {
	var out []tile.Span
	start := 0
	for i := 0; i <= len(text); i++ {
		if i == len(text) || text[i] == ';' {
			if i > start {
				out = append(out, tile.Span{Start: start, End: i})
			}
			start = i + 1
		}
	}
	return out
}
func (fakeLang) Classify(stmt string) tile.Classification {
	if strings.HasPrefix(stmt, "rows") {
		return tile.Classification{Verb: "ROWS", ReadOnly: true}
	}
	return tile.Classification{Verb: "OTHER", Risk: tile.RiskWrite}
}
func (fakeLang) Redact(stmt string) string { return strings.ReplaceAll(stmt, "hunter2", "***") }

type fakeSession struct{ c *fakeConn }

func (s *fakeSession) Close() error               { return nil }
func (s *fakeSession) Ping(context.Context) error { return nil }
func (s *fakeSession) Execute(ctx context.Context, req tile.Request) (tile.ResultStream, error) {
	s.c.t.mu.Lock()
	s.c.t.execCtx = ctx
	s.c.t.mu.Unlock()
	switch {
	case strings.HasPrefix(req.Text, "rows "):
		n, _ := strconv.Atoi(strings.TrimPrefix(req.Text, "rows "))
		return &fakeStream{n: n, ctx: ctx, batch: s.c.t.batch}, nil
	case req.Text == "sleep":
		return &fakeStream{sleep: true, t: s.c.t, ctx: ctx}, nil
	case req.Text == "bad frame":
		return &fakeStream{bad: true, ctx: ctx}, nil
	}
	return nil, errors.New("syntax error near \x07" + req.Text)
}

type fakeStream struct {
	n, sent    int
	batch      int
	schema     bool
	done       bool
	sleep, bad bool
	t          *fakeTile
	ctx        context.Context
}

func (s *fakeStream) Next(ctx context.Context) (tile.Frame, error) {
	if s.ctx.Err() != nil {
		return tile.Frame{}, s.ctx.Err()
	}
	if s.bad {
		return tile.Frame{Batch: &tile.Batch{Rows: 1, Cells: nil}}, nil
	}
	if s.sleep {
		<-ctx.Done()
		s.t.mu.Lock()
		s.t.cancelled = true
		s.t.mu.Unlock()
		return tile.Frame{}, ctx.Err()
	}
	if !s.schema {
		s.schema = true
		return tile.Frame{Schema: &tile.ResultSchema{Fields: []tile.Field{{Name: "n", Kind: tile.KindInt}}}}, nil
	}
	if s.sent < s.n {
		b := &tile.Batch{}
		size := s.batch
		if size == 0 {
			size = 100
		}
		for ; s.sent < s.n && b.Rows < size; s.sent++ {
			b.Rows++
			b.Cells = append(b.Cells, tile.Value{Kind: tile.KindInt, Raw: []byte(strconv.Itoa(s.sent + 1))})
		}
		return tile.Frame{Batch: b}, nil
	}
	if !s.done {
		s.done = true
		return tile.Frame{Done: &tile.ResultSummary{Tag: "ROWS"}}, nil
	}
	return tile.Frame{}, io.EOF
}
func (s *fakeStream) Close() error { return nil }

// pair serves ft in-process over pipes and returns the host's client.
func pair(t *testing.T, ft *fakeTile) (*Client, func()) {
	t.Helper()
	hostR, pluginW := io.Pipe()
	pluginR, hostW := io.Pipe()
	served := make(chan error, 1)
	go func() { served <- ServeConn(pluginR, pluginW, ft) }()
	c := NewClient(hostR, hostW)
	var once sync.Once
	stop := func() {
		once.Do(func() {
			hostW.Close()
			<-served
			pluginW.Close()
		})
	}
	t.Cleanup(stop)
	return c, stop
}

var testEnv = tile.Env{Secret: func(_ context.Context, name string) ([]byte, error) {
	if name == "password" {
		return []byte("hunter2"), nil
	}
	return nil, nil
}}

func open(t *testing.T, c *Client) tile.Conn {
	t.Helper()
	conn, err := c.Open(context.Background(), tile.ConnConfig{Values: map[string]string{"host": "x"}}, testEnv)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func TestHandshake(t *testing.T) {
	c, _ := pair(t, &fakeTile{})
	h, err := c.Handshake(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if h.Manifest.ID != "fake" || len(h.Manifest.ConnForm.Fields) != 1 || !h.Facets.ConfigWarner {
		t.Fatalf("handshake = %+v", h)
	}
	if w := c.ConfigWarnings(tile.ConnConfig{Values: map[string]string{"tls": "off"}}); len(w) != 1 || w[0] != "tls off[31m" {
		t.Fatalf("warnings = %q, want control characters stripped", w)
	}
}

func TestStreamAcrossProcessBoundary(t *testing.T) {
	ft := &fakeTile{}
	c, _ := pair(t, ft)
	conn := open(t, c)
	if ft.secret != "hunter2" {
		t.Fatalf("plugin got secret %q through the host callback", ft.secret)
	}
	if !conn.Capabilities()[tile.CapCancel].Supported {
		t.Fatal("capabilities lost")
	}
	if _, ok := conn.(tile.Browser); !ok {
		t.Fatal("browser facet lost")
	}
	if _, ok := conn.(tile.Editor); ok {
		t.Fatal("conn claims an editor facet the plugin lacks")
	}
	if conn.Tree() != nil {
		t.Fatal("conn claims a tree the plugin lacks")
	}
	s, err := conn.Session(context.Background(), tile.SessionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.(tile.Pinger); !ok {
		t.Fatal("pinger facet lost")
	}
	st, err := s.Execute(context.Background(), tile.Request{Text: "rows 250"})
	if err != nil {
		t.Fatal(err)
	}
	var rows int
	var last string
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
			last = string(f.Batch.Cells[len(f.Batch.Cells)-1].Raw)
		}
	}
	if rows != 250 || last != "250" {
		t.Fatalf("got %d rows ending %q", rows, last)
	}
	st.Close()
	// The stream outlived the Execute call, so its context must too.
	if ft.execCtx.Err() == nil {
		t.Fatal("closing the stream should end its context")
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if ft.closed != 1 {
		t.Fatal("conn not closed in plugin")
	}
}

func TestLanguage(t *testing.T) {
	c, _ := pair(t, &fakeTile{})
	lang := open(t, c).Language()
	if lang.ID() != "fake" {
		t.Fatal(lang.ID())
	}
	if got := lang.Split("rows 1;x"); len(got) != 2 || got[1] != (tile.Span{Start: 7, End: 8}) {
		t.Fatalf("split = %v", got)
	}
	if !lang.Classify("rows 1").ReadOnly || lang.Classify("drop").Risk != tile.RiskWrite {
		t.Fatal("classification lost")
	}
	if lang.Redact("pw hunter2") != "pw ***" {
		t.Fatal("redaction lost")
	}
}

func TestCancelReachesPlugin(t *testing.T) {
	ft := &fakeTile{}
	c, _ := pair(t, ft)
	s, err := open(t, c).Session(context.Background(), tile.SessionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.Execute(context.Background(), tile.Request{Text: "sleep"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := st.Next(ctx); !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want a context error", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("cancel took too long")
	}
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if !ft.cancelled {
		t.Fatal("the plugin's Next never saw the cancel")
	}
}

func TestErrorsAreCleaned(t *testing.T) {
	c, _ := pair(t, &fakeTile{})
	s, err := open(t, c).Session(context.Background(), tile.SessionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Execute(context.Background(), tile.Request{Text: "nope"})
	if err == nil || strings.ContainsRune(err.Error(), '\a') || !strings.Contains(err.Error(), "syntax error") {
		t.Fatalf("err = %q", err)
	}
}

func TestBadFrameIsRejected(t *testing.T) {
	c, _ := pair(t, &fakeTile{})
	s, err := open(t, c).Session(context.Background(), tile.SessionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.Execute(context.Background(), tile.Request{Text: "bad frame"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Next(context.Background()); err == nil || !strings.Contains(err.Error(), "bad frame") {
		t.Fatalf("err = %v", err)
	}
}

func TestSecretOnlyForOpenConnections(t *testing.T) {
	c, _ := pair(t, &fakeTile{})
	conn := open(t, c)
	conn.Close()
	// A plugin asking for a closed connection's secret gets nothing.
	_, err := c.handle(context.Background(), MHostSecret, []byte(`{"env":1,"name":"password"}`))
	if err == nil {
		t.Fatal("secret handed out after close")
	}
	if _, err := c.handle(context.Background(), "host.exec", nil); err == nil {
		t.Fatal("unknown host method accepted")
	}
}

func TestPluginGoneFailsSafe(t *testing.T) {
	c, stop := pair(t, &fakeTile{})
	lang := open(t, c).Language()
	stop()
	<-c.Done()
	if cl := lang.Classify("rows 1"); cl.Risk != tile.RiskDestructive || cl.ReadOnly {
		t.Fatalf("classify after exit = %+v, want destructive", cl)
	}
	if got := lang.Redact("pw hunter2"); strings.Contains(got, "hunter2") {
		t.Fatalf("redact after exit leaked %q", got)
	}
	if _, err := c.Open(context.Background(), tile.ConnConfig{}, testEnv); !errors.Is(err, ErrClosed) {
		t.Fatalf("open after exit = %v", err)
	}
}

func TestOversizedMessageIsRefused(t *testing.T) {
	r := strings.NewReader(strings.Repeat("x", 100) + "\n")
	p := newPeer(r, io.Discard, nil)
	if _, err := readLine(p.r, 50); err == nil {
		t.Fatal("long line accepted")
	}
}

// A deadline on the host must surface as a deadline, though the plugin only sees a cancel.
func TestDeadlineSurvivesTheBoundary(t *testing.T) {
	c, _ := pair(t, &fakeTile{})
	s, err := open(t, c).Session(context.Background(), tile.SessionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.Execute(context.Background(), tile.Request{Text: "sleep"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := st.Next(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want it to wrap context.DeadlineExceeded", err)
	}
}
