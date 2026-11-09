package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/arunksoman/tessera/internal/engine"
	"github.com/arunksoman/tessera/internal/store"
	"github.com/arunksoman/tessera/tile"
)

const (
	// Results stop here, like the TUI grid, then the statement is cancelled.
	defaultMaxRows  = 50_000
	defaultMaxBytes = 64 << 20
	browseLimit     = 200
	browseMax       = 1000
	treePage        = 500
	connectTimeout  = 15 * time.Second
	consoleIdle     = 30 * time.Minute
	planTTL         = 30 * time.Minute
	maxBody         = 4 << 20
)

// api holds the state behind /api: shared connections, one console per editor tab, and
// plans awaiting apply.
type api struct {
	e *engine.Engine
	// allowFiles permits form fields that name files on this machine. Only a loopback
	// server allows them, since there the browser user owns the machine.
	allowFiles bool
	mgr        *engine.Manager
	mu         sync.Mutex
	consoles   map[string]*console
	plans      map[string]*pendingPlan
}

type console struct {
	mu      sync.Mutex // held while a script runs
	profile string
	k       *engine.Console
	conn    *engine.Conn
	release func()
	last    time.Time
}

type pendingPlan struct {
	session, profile string
	p                *engine.Pending
	created          time.Time
}

func newAPI(e *engine.Engine) *api {
	return &api{e: e, mgr: engine.NewManager(e, 16, 10*time.Minute), consoles: map[string]*console{}, plans: map[string]*pendingPlan{}}
}

func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/tiles", s.tiles)
	mux.HandleFunc("GET /api/profiles", s.profiles)
	mux.HandleFunc("PUT /api/profiles/{name}", s.saveProfile)
	mux.HandleFunc("DELETE /api/profiles/{name}", s.deleteProfile)
	mux.HandleFunc("POST /api/conns/{name}/tree", s.tree)
	mux.HandleFunc("POST /api/conns/{name}/browse", s.browse)
	mux.HandleFunc("POST /api/conns/{name}/exec", s.exec)
	mux.HandleFunc("DELETE /api/conns/{name}/tabs/{tab}", s.closeTab)
	mux.HandleFunc("POST /api/conns/{name}/plan", s.plan)
	mux.HandleFunc("POST /api/conns/{name}/apply", s.apply)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "", "no such API")
	})
}

// Close ends every console and connection.
func (s *Server) Close() {
	if s.api == nil {
		return
	}
	a := s.api
	a.mu.Lock()
	for key, c := range a.consoles {
		c.close()
		delete(a.consoles, key)
	}
	a.mu.Unlock()
	a.mgr.Close()
}

// sweep closes consoles and plans left idle, and idle connections.
func (a *api) sweep(now time.Time) {
	a.mu.Lock()
	for key, c := range a.consoles {
		if now.Sub(c.last) > consoleIdle && c.mu.TryLock() {
			c.close()
			c.mu.Unlock()
			delete(a.consoles, key)
		}
	}
	for id, p := range a.plans {
		if now.Sub(p.created) > planTTL {
			delete(a.plans, id)
		}
	}
	a.mu.Unlock()
	a.mgr.CloseIdle()
}

func (c *console) close() {
	c.k.Close()
	c.release()
}

// --- errors and JSON ---

type errorJSON struct {
	Error   string       `json:"error"`
	Code    string       `json:"code,omitempty"`
	Prompts []promptJSON `json:"prompts,omitempty"`
}

type promptJSON struct {
	Statement string `json:"statement"`
	Verb      string `json:"verb"`
	Reason    string `json:"reason"`
	Risk      string `json:"risk"`
	Env       string `json:"env,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errorJSON{Error: msg, Code: code})
}

// badRequest marks an error as the caller's fault.
type badRequest struct{ error }

func badf(format string, args ...any) error { return badRequest{fmt.Errorf(format, args...)} }

// fail maps an error to a status. Database errors pass through as messages, since the
// database's own wording is what the user needs.
func fail(w http.ResponseWriter, err error) {
	var bad badRequest
	switch {
	case errors.As(err, &bad):
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	case errors.Is(err, errNoProfile):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, engine.ErrReadOnly):
		writeError(w, http.StatusForbidden, "read_only", err.Error())
	case errors.Is(err, engine.ErrNeedsConfirm), errors.Is(err, engine.ErrDeclined):
		writeError(w, http.StatusConflict, "confirm", err.Error())
	case errors.Is(err, engine.ErrConflict), errors.Is(err, engine.ErrPlanChanged):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, engine.ErrUnsupported):
		writeError(w, http.StatusNotImplemented, "unsupported", err.Error())
	case errors.Is(err, engine.ErrTooManyConns):
		writeError(w, http.StatusServiceUnavailable, "busy", err.Error())
	case errors.Is(err, context.Canceled):
		// The browser went away; nobody reads this.
	default:
		writeError(w, http.StatusBadGateway, "database", err.Error())
	}
}

var errNoProfile = errors.New("no such connection")

// readJSON decodes a JSON body strictly. Requiring the JSON type also keeps out plain
// cross-site form posts, on top of CrossOriginProtection.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "bad_request", "send application/json")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "bad JSON: "+err.Error())
		return false
	}
	return true
}

func sessionOf(r *http.Request) string {
	s, _ := r.Context().Value(sessionKey{}).(string)
	return s
}

// --- tiles and profiles ---

type tileJSON struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Style tile.Style      `json:"style"`
	Form  []formFieldJSON `json:"form"`
}

type formFieldJSON struct {
	Name     string   `json:"name"`
	Label    string   `json:"label"`
	Type     string   `json:"type"`
	Required bool     `json:"required,omitempty"`
	Options  []string `json:"options,omitempty"`
	Default  string   `json:"default,omitempty"`
}

func (s *Server) tiles(w http.ResponseWriter, r *http.Request) {
	out := []tileJSON{}
	for _, t := range s.api.e.Tiles() {
		m := t.Manifest()
		tj := tileJSON{ID: m.ID, Name: m.Name, Style: m.Style, Form: []formFieldJSON{}}
		for _, f := range m.ConnForm.Fields {
			tj.Form = append(tj.Form, formFieldJSON{f.Name, f.Label, f.Type, f.Required, f.Options, f.Default})
		}
		out = append(out, tj)
	}
	writeJSON(w, http.StatusOK, out)
}

type profileJSON struct {
	Name      string            `json:"name"`
	Tile      string            `json:"tile"`
	Env       string            `json:"env,omitempty"`
	ReadOnly  bool              `json:"readOnly"`
	NoHistory bool              `json:"noHistory"`
	Values    map[string]string `json:"values"`
	// Secrets says which secrets are set. Their values never leave the server.
	Secrets  map[string]bool `json:"secrets"`
	Warnings []string        `json:"warnings,omitempty"`
}

func (s *Server) describeProfile(p store.Profile) profileJSON {
	out := profileJSON{Name: p.Name, Tile: p.Tile, Env: p.Env, ReadOnly: p.ReadOnly, NoHistory: p.NoHistory,
		Values: p.Values, Secrets: map[string]bool{}, Warnings: s.api.e.Warnings(p)}
	if out.Values == nil {
		out.Values = map[string]string{}
	}
	if t, ok := s.api.e.Tile(p.Tile); ok {
		for _, f := range t.Manifest().ConnForm.Fields {
			if f.Type == "secret" {
				has, _ := s.api.e.HasSecret(p.Name, f.Name)
				out.Secrets[f.Name] = has
			}
		}
	}
	return out
}

func (s *Server) profiles(w http.ResponseWriter, r *http.Request) {
	ps, err := s.api.e.Store().Profiles()
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]profileJSON, 0, len(ps))
	for _, p := range ps {
		out = append(out, s.describeProfile(p))
	}
	writeJSON(w, http.StatusOK, out)
}

type saveProfileJSON struct {
	From      string            `json:"from"`
	Tile      string            `json:"tile"`
	Env       string            `json:"env"`
	ReadOnly  bool              `json:"readOnly"`
	NoHistory bool              `json:"noHistory"`
	Values    map[string]string `json:"values"`
	Secrets   map[string]string `json:"secrets"`
}

var envs = []string{"", "development", "staging", engine.EnvProduction}

// checkProfile builds settings field by field from the tile's form. Nothing outside the form
// is accepted, so no raw connection string or file-reading option can arrive this way.
func (a *api) checkProfile(name string, in saveProfileJSON) (store.Profile, error) {
	t, ok := a.e.Tile(in.Tile)
	if !ok {
		return store.Profile{}, badf("unknown database type %q", in.Tile)
	}
	if !slices.Contains(envs, in.Env) {
		return store.Profile{}, badf("environment must be one of development, staging, production, or empty")
	}
	p := store.Profile{Name: name, Tile: in.Tile, Env: in.Env, ReadOnly: in.ReadOnly, NoHistory: in.NoHistory, Values: map[string]string{}}
	fields := map[string]tile.FormField{}
	for _, f := range t.Manifest().ConnForm.Fields {
		fields[f.Name] = f
	}
	for k, v := range in.Values {
		f, ok := fields[k]
		switch {
		case !ok:
			return p, badf("%q is not a setting of %s", k, in.Tile)
		case f.Type == "secret":
			return p, badf("%s is a secret; send it in secrets", f.Label)
		case f.Type == "file" && !a.allowFiles && strings.TrimSpace(v) != "":
			return p, badf("%s names a file on the server, which only a local tess serve allows", f.Label)
		}
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		switch f.Type {
		case "int":
			if strings.Trim(v, "0123456789") != "" {
				return p, badf("%s must be a number", f.Label)
			}
		case "enum":
			if !slices.Contains(f.Options, v) {
				return p, badf("%s must be one of %s", f.Label, strings.Join(f.Options, ", "))
			}
		case "bool":
			if v != "true" && v != "false" {
				return p, badf("%s must be true or false", f.Label)
			}
		}
		p.Values[k] = v
	}
	for k := range in.Secrets {
		if f, ok := fields[k]; !ok || f.Type != "secret" {
			return p, badf("%q is not a secret of %s", k, in.Tile)
		}
	}
	for _, f := range fields {
		if f.Required && f.Type != "secret" && p.Values[f.Name] == "" {
			return p, badf("%s is required", f.Label)
		}
	}
	return p, nil
}

type savedJSON struct {
	profileJSON
	Connected bool   `json:"connected"`
	Error     string `json:"error,omitempty"`
}

func (s *Server) saveProfile(w http.ResponseWriter, r *http.Request) {
	a := s.api
	var in saveProfileJSON
	if !readJSON(w, r, &in) {
		return
	}
	name := r.PathValue("name")
	p, err := a.checkProfile(name, in)
	if err != nil {
		fail(w, err)
		return
	}
	st := a.e.Store()
	if in.From != "" && in.From != name {
		if _, err := st.Profile(in.From); err != nil {
			fail(w, badf("cannot rename: %v", err))
			return
		}
	}
	if err := st.SaveProfile(p); err != nil {
		fail(w, badRequest{err})
		return
	}
	for k, v := range in.Secrets {
		if v == "" {
			continue // empty keeps the saved secret
		}
		if err := a.e.SetSecret(name, k, []byte(v)); err != nil {
			fail(w, err)
			return
		}
	}
	if in.From != "" && in.From != name {
		t, _ := a.e.Tile(p.Tile)
		for _, f := range t.Manifest().ConnForm.Fields {
			if _, typed := in.Secrets[f.Name]; f.Type == "secret" && (!typed || in.Secrets[f.Name] == "") {
				if err := a.e.MoveSecret(in.From, name, f.Name); err != nil {
					fail(w, err)
					return
				}
			}
		}
		a.forget(in.From)
		st.DeleteProfile(in.From)
	}
	a.forget(name)

	// Connecting now means a mistake shows while the form is still open.
	out := savedJSON{profileJSON: s.describeProfile(p)}
	ctx, cancel := context.WithTimeout(r.Context(), connectTimeout)
	defer cancel()
	if conn, err := a.e.Open(ctx, name); err != nil {
		out.Error = err.Error()
	} else {
		err = conn.Ping(ctx)
		conn.Close()
		out.Connected = err == nil
		if err != nil {
			out.Error = err.Error()
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// forget closes a profile's consoles and connection, so changed settings take effect.
func (a *api) forget(profile string) {
	a.mu.Lock()
	for key, c := range a.consoles {
		if c.profile == profile && c.mu.TryLock() {
			c.close()
			c.mu.Unlock()
			delete(a.consoles, key)
		}
	}
	a.mu.Unlock()
	a.mgr.Forget(profile)
}

func (s *Server) deleteProfile(w http.ResponseWriter, r *http.Request) {
	a := s.api
	name := r.PathValue("name")
	p, err := a.e.Store().Profile(name)
	if err != nil {
		fail(w, fmt.Errorf("%w: %q", errNoProfile, name))
		return
	}
	a.forget(name)
	if err := a.e.Store().DeleteProfile(name); err != nil {
		fail(w, err)
		return
	}
	if t, ok := a.e.Tile(p.Tile); ok {
		for _, f := range t.Manifest().ConnForm.Fields {
			if f.Type == "secret" {
				a.e.DeleteSecret(name, f.Name)
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- connections ---

// acquire opens the profile's shared connection.
func (a *api) acquire(r *http.Request) (*engine.Conn, func(), error) {
	name := r.PathValue("name")
	if _, err := a.e.Store().Profile(name); err != nil {
		return nil, nil, fmt.Errorf("%w: %q", errNoProfile, name)
	}
	ctx, cancel := context.WithTimeout(r.Context(), connectTimeout)
	defer cancel()
	return a.mgr.Acquire(ctx, name)
}

type nodeJSON struct {
	Ref         refJSON `json:"ref"`
	Kind        string  `json:"kind"`
	Label       string  `json:"label"`
	HasChildren bool    `json:"hasChildren"`
	Browsable   bool    `json:"browsable"`
}

func (s *Server) tree(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Ref    refJSON `json:"ref"`
		Cursor string  `json:"cursor"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	conn, release, err := s.api.acquire(r)
	if err != nil {
		fail(w, err)
		return
	}
	defer release()
	tree := conn.Tree()
	if tree == nil {
		fail(w, engine.ErrUnsupported)
		return
	}
	nodes, next, err := tree.Children(r.Context(), in.Ref.ref(), tile.Page{Cursor: in.Cursor, Limit: treePage})
	if err != nil {
		fail(w, err)
		return
	}
	out := struct {
		Nodes []nodeJSON `json:"nodes"`
		Next  string     `json:"next"`
	}{Nodes: make([]nodeJSON, len(nodes)), Next: next}
	for i, n := range nodes {
		out.Nodes[i] = nodeJSON{encodeRef(n.Ref), n.Kind, n.Label, n.HasChildren, n.Browsable}
	}
	writeJSON(w, http.StatusOK, out)
}

type layoutJSON struct {
	Fields   []fieldJSON `json:"fields"`
	Key      []string    `json:"key"`
	Note     string      `json:"note,omitempty"`
	Estimate int64       `json:"estimate"`
}

var filterOps = []tile.FilterOp{tile.OpEq, tile.OpNe, tile.OpLt, tile.OpLe, tile.OpGt, tile.OpGe, tile.OpContains, tile.OpNull, tile.OpNotNull}

func kindOf(l tile.Layout, field string) (tile.ValueKind, bool) {
	for _, f := range l.Fields {
		if f.Name == field {
			return f.Kind, true
		}
	}
	return 0, false
}

func (s *Server) browse(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Ref  refJSON `json:"ref"`
		Sort []struct {
			Field string `json:"field"`
			Desc  bool   `json:"desc"`
		} `json:"sort"`
		Filter []struct {
			Field string `json:"field"`
			Op    string `json:"op"`
			Value Cell   `json:"value"`
		} `json:"filter"`
		After  []Cell `json:"after"`
		Offset int64  `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	conn, release, err := s.api.acquire(r)
	if err != nil {
		fail(w, err)
		return
	}
	defer release()
	ref := in.Ref.ref()
	l, err := conn.Layout(r.Context(), ref)
	if err != nil {
		fail(w, err)
		return
	}
	q := tile.BrowseQuery{Offset: max(0, in.Offset), Limit: in.Limit}
	if q.Limit <= 0 {
		q.Limit = browseLimit
	}
	q.Limit = min(q.Limit, browseMax)
	for _, k := range in.Sort {
		if _, ok := kindOf(l, k.Field); !ok {
			fail(w, badf("unknown field %q", k.Field))
			return
		}
		q.Sort = append(q.Sort, tile.SortKey{Field: k.Field, Desc: k.Desc})
	}
	for _, c := range in.Filter {
		kind, ok := kindOf(l, c.Field)
		if !ok {
			fail(w, badf("unknown field %q", c.Field))
			return
		}
		op := tile.FilterOp(c.Op)
		if !slices.Contains(filterOps, op) {
			fail(w, badf("unknown filter %q", c.Op))
			return
		}
		q.Filter = append(q.Filter, tile.Condition{Field: c.Field, Op: op, Value: c.Value.value(kind)})
	}
	if in.After != nil {
		if len(in.After) != len(l.Fields) {
			fail(w, badf("after has %d values, the object has %d fields", len(in.After), len(l.Fields)))
			return
		}
		for i, c := range in.After {
			q.After = append(q.After, c.value(l.Fields[i].Kind))
		}
	}
	out := struct {
		Layout layoutJSON  `json:"layout"`
		Fields []fieldJSON `json:"fields"`
		Rows   [][]Cell    `json:"rows"`
	}{Layout: layoutJSON{encodeFields(l.Fields), l.Key, l.Note, l.Estimate}, Rows: [][]Cell{}}
	if out.Layout.Key == nil {
		out.Layout.Key = []string{}
	}
	err = conn.Browse(r.Context(), ref, l, q, func(f tile.Frame) error {
		switch {
		case f.Schema != nil:
			out.Fields = encodeFields(f.Schema.Fields)
		case f.Batch != nil:
			out.Rows = append(out.Rows, encodeRows(f.Batch)...)
		}
		return nil
	})
	if err != nil {
		// Bad sort or filter input surfaces from the request builder, before any I/O.
		if strings.HasPrefix(err.Error(), "unknown ") {
			err = badRequest{err}
		}
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// --- running scripts ---

func (a *api) console(r *http.Request, tab string) (*console, error) {
	profile := r.PathValue("name")
	key := sessionOf(r) + "\x00" + profile + "\x00" + tab
	a.mu.Lock()
	c, ok := a.consoles[key]
	a.mu.Unlock()
	if ok {
		return c, nil
	}
	conn, release, err := a.acquire(r)
	if err != nil {
		return nil, err
	}
	c = &console{profile: profile, k: conn.NewConsole(), conn: conn, release: release, last: time.Now()}
	a.mu.Lock()
	defer a.mu.Unlock()
	if prev, ok := a.consoles[key]; ok {
		release() // another request for this tab won the race
		return prev, nil
	}
	a.consoles[key] = c
	return c, nil
}

func (s *Server) closeTab(w http.ResponseWriter, r *http.Request) {
	a := s.api
	key := sessionOf(r) + "\x00" + r.PathValue("name") + "\x00" + r.PathValue("tab")
	a.mu.Lock()
	c, ok := a.consoles[key]
	if ok {
		delete(a.consoles, key)
	}
	a.mu.Unlock()
	if ok {
		// Wait for a running script to stop; the browser cancels it before closing a tab.
		c.mu.Lock()
		c.close()
		c.mu.Unlock()
	}
	w.WriteHeader(http.StatusNoContent)
}

type execEventJSON struct {
	T           string      `json:"t"`
	Stmt        int         `json:"stmt"`
	Fields      []fieldJSON `json:"fields,omitempty"`
	Rows        [][]Cell    `json:"rows,omitempty"`
	Affected    int64       `json:"affected,omitempty"`
	HasAffected bool        `json:"hasAffected,omitempty"`
	Tag         string      `json:"tag,omitempty"`
	Error       string      `json:"error,omitempty"`
	Code        string      `json:"code,omitempty"`
	InTx        bool        `json:"inTx,omitempty"`
	DDL         bool        `json:"ddl,omitempty"`
}

var riskNames = map[tile.Risk]string{tile.RiskNone: "none", tile.RiskWrite: "write", tile.RiskDestructive: "destructive"}

// exec runs a script on the tab's console and streams frames as NDJSON. Every statement
// passes the gate before any runs; statements that need confirmation come back as 409 with
// prompts, and the browser repeats the request naming the statements the user confirmed.
// Closing the request cancels the statement on the server.
func (s *Server) exec(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Tab     string   `json:"tab"`
		SQL     string   `json:"sql"`
		Confirm []string `json:"confirm"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.Tab == "" {
		fail(w, badf("tab is required"))
		return
	}
	c, err := s.api.console(r, in.Tab)
	if err != nil {
		fail(w, err)
		return
	}
	if !c.mu.TryLock() {
		writeError(w, http.StatusConflict, "busy", "this tab is still running a script")
		return
	}
	defer c.mu.Unlock()
	c.last = time.Now()
	defer func() { c.last = time.Now() }()

	prompts, err := c.conn.Prompts(in.SQL)
	if err != nil {
		fail(w, err)
		return
	}
	var pending []promptJSON
	for _, p := range prompts {
		if !slices.Contains(in.Confirm, p.Statement) {
			pending = append(pending, promptJSON{p.Statement, p.Class.Verb, p.Class.Reason, riskNames[p.Class.Risk], p.Env})
		}
	}
	if len(pending) > 0 {
		writeJSON(w, http.StatusConflict, errorJSON{Error: "needs confirmation", Code: "confirm", Prompts: pending})
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	enc := json.NewEncoder(w)
	send := func(ev execEventJSON) error {
		if err := enc.Encode(ev); err != nil {
			return err
		}
		return rc.Flush()
	}
	rows, size, at := 0, 0, -1
	confirm := func(p engine.Prompt) bool { return slices.Contains(in.Confirm, p.Statement) }
	err = c.k.Run(r.Context(), in.SQL, engine.RunOptions{Confirm: confirm}, func(stmt int, f tile.Frame) error {
		if stmt != at {
			at, rows, size = stmt, 0, 0
		}
		switch {
		case f.Schema != nil:
			return send(execEventJSON{T: "schema", Stmt: stmt, Fields: encodeFields(f.Schema.Fields)})
		case f.Batch != nil:
			b := *f.Batch
			if left := s.maxRows - rows; b.Rows > left {
				width := 0
				if b.Rows > 0 {
					width = len(b.Cells) / b.Rows
				}
				b.Rows, b.Cells = left, b.Cells[:left*width]
			}
			for _, v := range b.Cells {
				size += len(v.Raw)
			}
			rows += b.Rows
			if b.Rows > 0 {
				if err := send(execEventJSON{T: "rows", Stmt: stmt, Rows: encodeRows(&b)}); err != nil {
					return err
				}
			}
			if rows >= s.maxRows || size >= defaultMaxBytes || b.Rows < f.Batch.Rows {
				send(execEventJSON{T: "truncated", Stmt: stmt, Affected: int64(rows)})
				return engine.ErrStop
			}
		case f.Done != nil:
			return send(execEventJSON{T: "done", Stmt: stmt, Affected: f.Done.RowsAffected, HasAffected: f.Done.HasAffected, Tag: f.Done.Tag})
		}
		return nil
	})
	if err != nil && r.Context().Err() == nil {
		send(execEventJSON{T: "error", Stmt: max(at, 0), Error: err.Error()})
	}
	send(execEventJSON{T: "end", InTx: c.k.InTx(), DDL: c.conn.IsDDL(in.SQL)})
}

// --- edits ---

type changeJSON struct {
	Key []Cell          `json:"key"`
	Old map[string]Cell `json:"old"`
	Set map[string]Cell `json:"set"`
}

func (s *Server) plan(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Ref     refJSON      `json:"ref"`
		Changes []changeJSON `json:"changes"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if len(in.Changes) == 0 {
		fail(w, badf("no changes"))
		return
	}
	conn, release, err := s.api.acquire(r)
	if err != nil {
		fail(w, err)
		return
	}
	defer release()
	ref := in.Ref.ref()
	l, err := conn.Layout(r.Context(), ref)
	if err != nil {
		fail(w, err)
		return
	}
	if len(l.Key) == 0 {
		fail(w, badf("this object cannot be edited: %s", l.Note))
		return
	}
	var cs tile.ChangeSet
	for _, ch := range in.Changes {
		if len(ch.Key) != len(l.Key) {
			fail(w, badf("a change needs %d key values", len(l.Key)))
			return
		}
		out := tile.Change{Old: map[string]tile.Value{}, Set: map[string]tile.Value{}}
		for i, k := range ch.Key {
			kind, _ := kindOf(l, l.Key[i])
			out.Key = append(out.Key, k.value(kind))
		}
		for name, v := range ch.Set {
			kind, ok := kindOf(l, name)
			if !ok {
				fail(w, badf("unknown field %q", name))
				return
			}
			old, ok := ch.Old[name]
			if !ok {
				fail(w, badf("a change to %q needs the value it replaces", name))
				return
			}
			out.Set[name] = v.value(kind)
			out.Old[name] = old.value(kind)
		}
		cs.Changes = append(cs.Changes, out)
	}
	p, err := conn.PlanChanges(ref, l, cs)
	if err != nil {
		fail(w, badRequest{err})
		return
	}
	id := randomString()
	s.api.mu.Lock()
	s.api.plans[id] = &pendingPlan{session: sessionOf(r), profile: r.PathValue("name"), p: p, created: time.Now()}
	s.api.mu.Unlock()

	type stepJSON struct {
		Preview    string `json:"preview"`
		ExpectRows int64  `json:"expectRows"`
	}
	out := struct {
		ID     string     `json:"id"`
		Steps  []stepJSON `json:"steps"`
		Atomic bool       `json:"atomic"`
	}{ID: id, Atomic: p.Plan.Atomicity == tile.AtomicAll}
	for _, st := range p.Plan.Steps {
		out.Steps = append(out.Steps, stepJSON{st.Preview, st.ExpectRows})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) apply(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID      string `json:"id"`
		Confirm bool   `json:"confirm"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	a := s.api
	a.mu.Lock()
	p, ok := a.plans[in.ID]
	ok = ok && p.session == sessionOf(r) && p.profile == r.PathValue("name")
	a.mu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no such plan; preview the changes again")
		return
	}
	conn, release, err := a.acquire(r)
	if err != nil {
		fail(w, err)
		return
	}
	defer release()
	err = conn.Apply(r.Context(), p.p, engine.RunOptions{Confirm: func(engine.Prompt) bool { return in.Confirm }})
	if errors.Is(err, engine.ErrNeedsConfirm) || errors.Is(err, engine.ErrDeclined) {
		fail(w, err) // the plan stays, so the browser can confirm and apply it
		return
	}
	a.mu.Lock()
	delete(a.plans, in.ID)
	a.mu.Unlock()
	if err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
