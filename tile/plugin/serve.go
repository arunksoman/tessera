package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/arunksoman/tessera/tile"
)

// Serve runs t as a plugin on standard input and output until the host goes away. Anything a
// plugin prints must go to standard error, because standard output carries the protocol.
func Serve(t tile.Tile) {
	if err := ServeConn(os.Stdin, os.Stdout, t); err != nil && !errors.Is(err, ErrClosed) {
		fmt.Fprintf(os.Stderr, "plugin: %v\n", err)
		os.Exit(1)
	}
}

// ServeConn serves t on r and w until r ends.
func ServeConn(r io.Reader, w io.Writer, t tile.Tile) error {
	s := &server{t: t, conns: map[uint64]*served{}, sessions: map[uint64]tile.Session{}, streams: map[uint64]tile.ResultStream{}}
	s.p = newPeer(r, w, s.handle)
	err := s.p.run()
	s.closeAll()
	return err
}

type served struct {
	conn tile.Conn
	lang tile.Language
}

type server struct {
	t        tile.Tile
	p        *peer
	mu       sync.Mutex
	next     uint64
	conns    map[uint64]*served
	sessions map[uint64]tile.Session
	streams  map[uint64]tile.ResultStream
}

var errNoHandle = errors.New("plugin: unknown handle")

func (s *server) add(v any) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	switch v := v.(type) {
	case *served:
		s.conns[s.next] = v
	case tile.Session:
		s.sessions[s.next] = v
	case tile.ResultStream:
		s.streams[s.next] = v
	}
	return s.next
}

func (s *server) conn(id uint64) (*served, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.conns[id]; c != nil {
		return c, nil
	}
	return nil, errNoHandle
}

func (s *server) session(id uint64) (tile.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v := s.sessions[id]; v != nil {
		return v, nil
	}
	return nil, errNoHandle
}

func (s *server) stream(id uint64) (tile.ResultStream, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v := s.streams[id]; v != nil {
		return v, nil
	}
	return nil, errNoHandle
}

// closeAll runs when the host goes away, so database sessions do not outlive it.
func (s *server) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.streams {
		st.Close()
	}
	for _, se := range s.sessions {
		se.Close()
	}
	for _, c := range s.conns {
		c.conn.Close()
	}
}

func decode[T any](raw json.RawMessage) (T, error) {
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, fmt.Errorf("plugin protocol: %w", err)
	}
	return v, nil
}

func (s *server) handle(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	switch method {
	case MHandshake:
		req, err := decode[HandshakeRequest](raw)
		if err != nil {
			return nil, err
		}
		m := s.t.Manifest()
		if req.APIVersion != m.APIVersion {
			return nil, fmt.Errorf("plugin %s speaks tile API %d, host speaks %d", m.ID, m.APIVersion, req.APIVersion)
		}
		_, warns := s.t.(tile.ConfigWarner)
		return HandshakeReply{Manifest: m, Protocol: protocolRevision, Facets: Facets{ConfigWarner: warns}}, nil
	case MConfigWarnings:
		cfg, err := decode[tile.ConnConfig](raw)
		if err != nil {
			return nil, err
		}
		if w, ok := s.t.(tile.ConfigWarner); ok {
			return w.ConfigWarnings(cfg), nil
		}
		return []string{}, nil
	case MOpen:
		return s.open(ctx, raw)
	case MConnSession, MConnCaps, MConnPing, MConnClose, MTreeChildren, MTreeDescribe,
		MLangSplit, MLangClassify, MLangRedact, MBrowseLayout, MBrowseRequest, MEditorPlan, MExplainRequest:
		return s.handleConn(ctx, method, raw)
	case MSessionExecute:
		req, err := decode[ExecuteRequest](raw)
		if err != nil {
			return nil, err
		}
		se, err := s.session(req.Session)
		if err != nil {
			return nil, err
		}
		// A tile may tie the result to Execute's context, which ends when this call returns.
		// So the stream gets its own context: the call's cancel reaches it only until Execute
		// returns, and closing the stream ends it.
		sctx, cancel := context.WithCancel(s.p.ctx)
		stopWatch := context.AfterFunc(ctx, cancel)
		st, err := se.Execute(sctx, req.Request)
		stopWatch()
		if err != nil {
			cancel()
			return nil, err
		}
		return StreamReply{Stream: s.add(&ownedStream{ResultStream: st, cancel: cancel})}, nil
	case MSessionPing:
		ref, err := decode[Ref](raw)
		if err != nil {
			return nil, err
		}
		se, err := s.session(ref.ID)
		if err != nil {
			return nil, err
		}
		if p, ok := se.(tile.Pinger); ok {
			return nil, p.Ping(ctx)
		}
		return nil, errors.New("session does not support ping")
	case MSessionClose:
		ref, err := decode[Ref](raw)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		se := s.sessions[ref.ID]
		delete(s.sessions, ref.ID)
		s.mu.Unlock()
		if se == nil {
			return nil, nil
		}
		return nil, se.Close()
	case MStreamNext:
		ref, err := decode[Ref](raw)
		if err != nil {
			return nil, err
		}
		st, err := s.stream(ref.ID)
		if err != nil {
			return nil, err
		}
		// The frame is encoded before this returns, so Raw bytes are used while still valid.
		return st.Next(ctx)
	case MStreamClose:
		ref, err := decode[Ref](raw)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		st := s.streams[ref.ID]
		delete(s.streams, ref.ID)
		s.mu.Unlock()
		if st == nil {
			return nil, nil
		}
		return nil, st.Close()
	}
	return nil, fmt.Errorf("plugin: unknown method %q", method)
}

func (s *server) open(ctx context.Context, raw json.RawMessage) (any, error) {
	req, err := decode[OpenRequest](raw)
	if err != nil {
		return nil, err
	}
	envID := req.Env
	env := tile.Env{Secret: func(ctx context.Context, name string) ([]byte, error) {
		var reply SecretReply
		err := s.p.call(ctx, MHostSecret, SecretRequest{Env: envID, Name: name}, &reply)
		return reply.Value, err
	}}
	c, err := s.t.Open(ctx, req.Config, env)
	if err != nil {
		return nil, err
	}
	sv := &served{conn: c, lang: c.Language()}
	_, browser := c.(tile.Browser)
	_, editor := c.(tile.Editor)
	_, explainer := c.(tile.Explainer)
	reply := OpenReply{Conn: s.add(sv), Facets: Facets{Tree: c.Tree() != nil, Browser: browser, Editor: editor, Explainer: explainer}}
	if sv.lang != nil {
		reply.Language = sv.lang.ID()
	}
	return reply, nil
}

// ownedStream ends its context once closed.
type ownedStream struct {
	tile.ResultStream
	cancel context.CancelFunc
}

func (o *ownedStream) Close() error {
	err := o.ResultStream.Close()
	o.cancel()
	return err
}

// connID reads the conn field that every connection-level request carries.
type connID struct {
	Conn uint64 `json:"conn"`
	ID   uint64 `json:"id"`
}

func (s *server) handleConn(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	ids, err := decode[connID](raw)
	if err != nil {
		return nil, err
	}
	id := ids.Conn
	if id == 0 {
		id = ids.ID
	}
	sv, err := s.conn(id)
	if err != nil {
		return nil, err
	}
	c := sv.conn
	switch method {
	case MConnCaps:
		return c.Capabilities(), nil
	case MConnPing:
		return nil, c.Ping(ctx)
	case MConnClose:
		s.mu.Lock()
		delete(s.conns, id)
		s.mu.Unlock()
		return nil, c.Close()
	case MConnSession:
		req, err := decode[SessionRequest](raw)
		if err != nil {
			return nil, err
		}
		se, err := c.Session(ctx, req.Options)
		if err != nil {
			return nil, err
		}
		_, pinger := se.(tile.Pinger)
		return SessionReply{Session: s.add(se), Facets: Facets{Pinger: pinger}}, nil
	case MTreeChildren:
		req, err := decode[ChildrenRequest](raw)
		if err != nil {
			return nil, err
		}
		tree := c.Tree()
		if tree == nil {
			return nil, errors.New("connection has no object tree")
		}
		nodes, next, err := tree.Children(ctx, req.Parent, req.Page)
		return ChildrenReply{Nodes: nodes, Next: next}, err
	case MTreeDescribe:
		req, err := decode[RefRequest](raw)
		if err != nil {
			return nil, err
		}
		tree := c.Tree()
		if tree == nil {
			return nil, errors.New("connection has no object tree")
		}
		return tree.Describe(ctx, req.Ref)
	case MLangSplit, MLangClassify, MLangRedact:
		req, err := decode[TextRequest](raw)
		if err != nil {
			return nil, err
		}
		if sv.lang == nil {
			return nil, errors.New("connection has no language")
		}
		switch method {
		case MLangSplit:
			return sv.lang.Split(req.Text), nil
		case MLangClassify:
			return sv.lang.Classify(req.Text), nil
		}
		return sv.lang.Redact(req.Text), nil
	case MBrowseLayout:
		req, err := decode[RefRequest](raw)
		if err != nil {
			return nil, err
		}
		b, ok := c.(tile.Browser)
		if !ok {
			return nil, errors.New("connection cannot browse")
		}
		return b.Layout(ctx, req.Ref)
	case MBrowseRequest:
		req, err := decode[BrowseRequest](raw)
		if err != nil {
			return nil, err
		}
		b, ok := c.(tile.Browser)
		if !ok {
			return nil, errors.New("connection cannot browse")
		}
		return b.BrowseRequest(req.Ref, req.Layout, req.Query)
	case MEditorPlan:
		req, err := decode[PlanRequest](raw)
		if err != nil {
			return nil, err
		}
		e, ok := c.(tile.Editor)
		if !ok {
			return nil, errors.New("connection cannot edit")
		}
		return e.Plan(req.Ref, req.Layout, req.Changes)
	case MExplainRequest:
		req, err := decode[ExplainRequest](raw)
		if err != nil {
			return nil, err
		}
		e, ok := c.(tile.Explainer)
		if !ok {
			return nil, errors.New("connection cannot explain")
		}
		return e.ExplainRequest(req.Text, req.Format), nil
	}
	return nil, fmt.Errorf("plugin: unknown method %q", method)
}
