package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/arunksoman/tessera/tile"
)

// callTimeout bounds calls whose tile method takes no context.
const callTimeout = 30 * time.Second

// Client is the host's side of one plugin process. Conns it opens behave like in-process
// ones, so the engine cannot tell the two apart.
type Client struct {
	p       *peer
	mu      sync.Mutex
	envs    map[uint64]tile.Env
	nextEnv uint64
}

// NewClient talks to a plugin that reads requests from w and writes replies to r.
func NewClient(r io.Reader, w io.Writer) *Client {
	c := &Client{envs: map[uint64]tile.Env{}}
	c.p = newPeer(r, w, c.handle)
	go c.p.run()
	return c
}

// Done is closed when the plugin goes away.
func (c *Client) Done() <-chan struct{} { return c.p.done }

// Handshake checks the plugin speaks this host's tile API and returns what it is.
func (c *Client) Handshake(ctx context.Context) (HandshakeReply, error) {
	var reply HandshakeReply
	err := c.p.call(ctx, MHandshake, HandshakeRequest{APIVersion: tile.APIVersion, Protocol: protocolRevision}, &reply)
	if err != nil {
		return reply, fmt.Errorf("plugin handshake: %w", err)
	}
	if reply.Manifest.APIVersion != tile.APIVersion {
		return reply, fmt.Errorf("plugin %q speaks tile API %d, this tess speaks %d", reply.Manifest.ID, reply.Manifest.APIVersion, tile.APIVersion)
	}
	if reply.Protocol != protocolRevision {
		return reply, fmt.Errorf("plugin %q speaks protocol %d, this tess speaks %d", reply.Manifest.ID, reply.Protocol, protocolRevision)
	}
	return reply, nil
}

// handle serves the plugin's calls back into the host. A plugin may ask only for secrets of
// connections it was asked to open, and only while they are open.
func (c *Client) handle(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	if method != MHostSecret {
		return nil, fmt.Errorf("host: unknown method %q", method)
	}
	req, err := decode[SecretRequest](raw)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	env, ok := c.envs[req.Env]
	c.mu.Unlock()
	if !ok {
		return nil, errors.New("host: no such connection")
	}
	if env.Secret == nil {
		return SecretReply{}, nil
	}
	v, err := env.Secret(ctx, req.Name)
	return SecretReply{Value: v}, err
}

func (c *Client) addEnv(env tile.Env) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextEnv++
	c.envs[c.nextEnv] = env
	return c.nextEnv
}

func (c *Client) dropEnv(id uint64) {
	c.mu.Lock()
	delete(c.envs, id)
	c.mu.Unlock()
}

// ConfigWarnings asks the plugin to name weak settings. A failure yields no warnings.
func (c *Client) ConfigWarnings(cfg tile.ConnConfig) []string {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	var out []string
	if err := c.p.call(ctx, MConfigWarnings, cfg, &out); err != nil {
		return []string{"plugin could not check this config: " + err.Error()}
	}
	for i, w := range out {
		out[i] = cleanText(w, 1024)
	}
	return out
}

// Open opens a connection in the plugin. env.Dial is not carried across: a plugin dials
// for itself.
func (c *Client) Open(ctx context.Context, cfg tile.ConnConfig, env tile.Env) (tile.Conn, error) {
	envID := c.addEnv(env)
	var reply OpenReply
	if err := c.p.call(ctx, MOpen, OpenRequest{Config: cfg, Env: envID}, &reply); err != nil {
		c.dropEnv(envID)
		return nil, err
	}
	rc := &remoteConn{c: c, id: reply.Conn, env: envID, lang: reply.Language, tree: reply.Facets.Tree}
	return wrapConn(rc, reply.Facets), nil
}

func short() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), callTimeout)
}

type connReq struct {
	Conn uint64 `json:"conn"`
}

type remoteConn struct {
	c    *Client
	id   uint64
	env  uint64
	lang string
	tree bool
	once sync.Once
}

func (r *remoteConn) call(ctx context.Context, method string, params, out any) error {
	return r.c.p.call(ctx, method, params, out)
}

// Capabilities returns none if the plugin cannot answer, so nothing is assumed supported.
func (r *remoteConn) Capabilities() tile.Capabilities {
	ctx, cancel := short()
	defer cancel()
	var caps tile.Capabilities
	if err := r.call(ctx, MConnCaps, connReq{r.id}, &caps); err != nil || caps == nil {
		return tile.Capabilities{}
	}
	return caps
}

func (r *remoteConn) Ping(ctx context.Context) error {
	return r.call(ctx, MConnPing, connReq{r.id}, nil)
}

func (r *remoteConn) Close() error {
	var err error
	r.once.Do(func() {
		ctx, cancel := short()
		defer cancel()
		err = r.call(ctx, MConnClose, connReq{r.id}, nil)
		r.c.dropEnv(r.env)
	})
	return err
}

func (r *remoteConn) Session(ctx context.Context, opt tile.SessionOptions) (tile.Session, error) {
	var reply SessionReply
	if err := r.call(ctx, MConnSession, SessionRequest{Conn: r.id, Options: opt}, &reply); err != nil {
		return nil, err
	}
	s := &remoteSession{c: r.c, id: reply.Session}
	if reply.Facets.Pinger {
		return pingSession{s}, nil
	}
	return s, nil
}

func (r *remoteConn) Tree() tile.ObjectTree {
	if !r.tree {
		return nil
	}
	return remoteTree{r}
}

func (r *remoteConn) Language() tile.Language { return remoteLang{r} }

type remoteTree struct{ r *remoteConn }

func (t remoteTree) Children(ctx context.Context, parent tile.ObjectRef, p tile.Page) ([]tile.Node, string, error) {
	var reply ChildrenReply
	err := t.r.call(ctx, MTreeChildren, ChildrenRequest{Conn: t.r.id, Parent: parent, Page: p}, &reply)
	return reply.Nodes, reply.Next, err
}

func (t remoteTree) Describe(ctx context.Context, ref tile.ObjectRef) (tile.ObjectDetail, error) {
	var d tile.ObjectDetail
	err := t.r.call(ctx, MTreeDescribe, RefRequest{Conn: t.r.id, Ref: ref}, &d)
	return d, err
}

// remoteLang fails safe: the safety gate relies on it, so when the plugin cannot answer, a
// statement counts as destructive and redaction hides the whole statement.
type remoteLang struct{ r *remoteConn }

func (l remoteLang) ID() string { return l.r.lang }

func (l remoteLang) Split(text string) []tile.Span {
	ctx, cancel := short()
	defer cancel()
	var spans []tile.Span
	if err := l.r.call(ctx, MLangSplit, TextRequest{Conn: l.r.id, Text: text}, &spans); err != nil {
		return []tile.Span{{Start: 0, End: len(text)}}
	}
	for _, s := range spans {
		if s.Start < 0 || s.End < s.Start || s.End > len(text) {
			return []tile.Span{{Start: 0, End: len(text)}}
		}
	}
	return spans
}

func (l remoteLang) Classify(stmt string) tile.Classification {
	ctx, cancel := short()
	defer cancel()
	var c tile.Classification
	if err := l.r.call(ctx, MLangClassify, TextRequest{Conn: l.r.id, Text: stmt}, &c); err != nil {
		return tile.Classification{Risk: tile.RiskDestructive, Reason: "plugin could not classify: " + err.Error()}
	}
	if c.Risk > tile.RiskDestructive {
		c.Risk = tile.RiskDestructive
	}
	return c
}

func (l remoteLang) Redact(stmt string) string {
	ctx, cancel := short()
	defer cancel()
	var out string
	if err := l.r.call(ctx, MLangRedact, TextRequest{Conn: l.r.id, Text: stmt}, &out); err != nil {
		return "[redacted: plugin unavailable]"
	}
	return out
}

type browserFacet struct{ r *remoteConn }

func (b browserFacet) Layout(ctx context.Context, ref tile.ObjectRef) (tile.Layout, error) {
	var l tile.Layout
	err := b.r.call(ctx, MBrowseLayout, RefRequest{Conn: b.r.id, Ref: ref}, &l)
	return l, err
}

func (b browserFacet) BrowseRequest(ref tile.ObjectRef, l tile.Layout, q tile.BrowseQuery) (tile.Request, error) {
	ctx, cancel := short()
	defer cancel()
	var req tile.Request
	err := b.r.call(ctx, MBrowseRequest, BrowseRequest{Conn: b.r.id, Ref: ref, Layout: l, Query: q}, &req)
	return req, err
}

type editorFacet struct{ r *remoteConn }

func (e editorFacet) Plan(ref tile.ObjectRef, l tile.Layout, cs tile.ChangeSet) (tile.Plan, error) {
	ctx, cancel := short()
	defer cancel()
	var p tile.Plan
	err := e.r.call(ctx, MEditorPlan, PlanRequest{Conn: e.r.id, Ref: ref, Layout: l, Changes: cs}, &p)
	return p, err
}

type explainerFacet struct{ r *remoteConn }

// ExplainRequest returns an empty request if the plugin cannot answer, never the statement
// itself, which would run it instead of explaining it.
func (e explainerFacet) ExplainRequest(stmt string, format tile.ExplainFormat) tile.Request {
	ctx, cancel := short()
	defer cancel()
	var req tile.Request
	if err := e.r.call(ctx, MExplainRequest, ExplainRequest{Conn: e.r.id, Text: stmt, Format: format}, &req); err != nil {
		return tile.Request{}
	}
	return req
}

// wrapConn returns a value whose type assertions match the facets the plugin reported.
func wrapConn(r *remoteConn, f Facets) tile.Conn {
	b, e, x := browserFacet{r}, editorFacet{r}, explainerFacet{r}
	switch {
	case f.Browser && f.Editor && f.Explainer:
		return struct {
			*remoteConn
			browserFacet
			editorFacet
			explainerFacet
		}{r, b, e, x}
	case f.Browser && f.Editor:
		return struct {
			*remoteConn
			browserFacet
			editorFacet
		}{r, b, e}
	case f.Browser && f.Explainer:
		return struct {
			*remoteConn
			browserFacet
			explainerFacet
		}{r, b, x}
	case f.Editor && f.Explainer:
		return struct {
			*remoteConn
			editorFacet
			explainerFacet
		}{r, e, x}
	case f.Browser:
		return struct {
			*remoteConn
			browserFacet
		}{r, b}
	case f.Editor:
		return struct {
			*remoteConn
			editorFacet
		}{r, e}
	case f.Explainer:
		return struct {
			*remoteConn
			explainerFacet
		}{r, x}
	}
	return r
}

type remoteSession struct {
	c    *Client
	id   uint64
	once sync.Once
}

func (s *remoteSession) Execute(ctx context.Context, req tile.Request) (tile.ResultStream, error) {
	var reply StreamReply
	if err := s.c.p.call(ctx, MSessionExecute, ExecuteRequest{Session: s.id, Request: req}, &reply); err != nil {
		return nil, err
	}
	return &remoteStream{c: s.c, id: reply.Stream}, nil
}

func (s *remoteSession) Close() error {
	var err error
	s.once.Do(func() {
		ctx, cancel := short()
		defer cancel()
		err = s.c.p.call(ctx, MSessionClose, Ref{ID: s.id}, nil)
	})
	return err
}

type pingSession struct{ *remoteSession }

func (s pingSession) Ping(ctx context.Context) error {
	return s.c.p.call(ctx, MSessionPing, Ref{ID: s.id}, nil)
}

// remoteStream checks the plugin keeps the frame order and shape the engine relies on.
type remoteStream struct {
	c      *Client
	id     uint64
	fields int
	state  int // 0 before Schema, 1 in batches, 2 after Done, 3 at EOF
	once   sync.Once
}

func (s *remoteStream) Next(ctx context.Context) (tile.Frame, error) {
	if s.state == 3 {
		return tile.Frame{}, io.EOF
	}
	var f tile.Frame
	if err := s.c.p.call(ctx, MStreamNext, Ref{ID: s.id}, &f); err != nil {
		if errors.Is(err, io.EOF) {
			s.state = 3
		}
		return tile.Frame{}, err
	}
	if err := s.check(f); err != nil {
		s.Close()
		s.state = 3
		return tile.Frame{}, err
	}
	return f, nil
}

func (s *remoteStream) check(f tile.Frame) error {
	set := 0
	for _, ok := range []bool{f.Schema != nil, f.Batch != nil, f.Done != nil} {
		if ok {
			set++
		}
	}
	bad := func(why string) error { return fmt.Errorf("plugin sent a bad frame: %s", why) }
	if set != 1 {
		return bad("a frame must set exactly one part")
	}
	switch {
	case f.Schema != nil:
		if s.state != 0 {
			return bad("schema after rows")
		}
		for _, fd := range f.Schema.Fields {
			if fd.Kind > tile.KindOther {
				return bad("unknown value kind")
			}
		}
		s.fields, s.state = len(f.Schema.Fields), 1
	case f.Batch != nil:
		if s.state != 1 {
			return bad("rows without a schema")
		}
		if f.Batch.Rows < 0 || len(f.Batch.Cells) != f.Batch.Rows*s.fields {
			return bad("cell count does not match rows and fields")
		}
		for _, v := range f.Batch.Cells {
			if v.Kind > tile.KindOther {
				return bad("unknown value kind")
			}
		}
	case f.Done != nil:
		if s.state == 2 {
			return bad("two summaries")
		}
		s.state = 2
	}
	return nil
}

func (s *remoteStream) Close() error {
	var err error
	s.once.Do(func() {
		ctx, cancel := short()
		defer cancel()
		err = s.c.p.call(ctx, MStreamClose, Ref{ID: s.id}, nil)
	})
	return err
}
