package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/arunksoman/tessera/tile"
)

// Console is a long-lived session for one editor tab, so SET state, temporary tables, and
// explicit transactions last between runs. It runs one script at a time.
type Console struct {
	c    *Conn
	sess tile.Session
	inTx atomic.Bool // read by the UI while a script runs
}

func (c *Conn) NewConsole() *Console { return &Console{c: c} }

// InTx reports whether a transaction opened in this console is still open.
func (k *Console) InTx() bool { return k.inTx.Load() }

// Close ends the session, which rolls back any open transaction.
func (k *Console) Close() error {
	k.inTx.Store(false)
	if k.sess == nil {
		return nil
	}
	s := k.sess
	k.sess = nil
	return s.Close()
}

// Run gates the script like Conn.Run, then runs it on the pinned session.
func (k *Console) Run(ctx context.Context, text string, opt RunOptions, sink Sink) error {
	stmts, classes, err := k.c.prepare(text, opt.Confirm)
	if err != nil || len(stmts) == 0 {
		return err
	}
	if err := k.ensureSession(ctx); err != nil {
		return err
	}
	return k.c.runAll(ctx, k.sess, stmts, classes, sink, k.track)
}

func (k *Console) ensureSession(ctx context.Context) error {
	if k.sess != nil {
		p, ok := k.sess.(tile.Pinger)
		if !ok || p.Ping(ctx) == nil {
			return nil
		}
		lost := k.inTx.Load()
		k.Close()
		if lost {
			return ErrSessionLost
		}
	}
	s, err := k.c.conn.Session(ctx, tile.SessionOptions{ReadOnly: k.c.profile.ReadOnly})
	if err != nil {
		return err
	}
	k.sess = s
	return nil
}

func (k *Console) track(stmt string, class tile.Classification) {
	switch class.Verb {
	case "BEGIN", "START":
		k.inTx.Store(true)
	case "COMMIT", "END":
		k.inTx.Store(false)
	case "ROLLBACK":
		if !slices.Contains(strings.Fields(strings.ToUpper(stmt)), "TO") {
			k.inTx.Store(false)
		}
	case "CREATE", "ALTER", "DROP", "RENAME", "TRUNCATE":
		// Without transactional DDL the server commits implicitly.
		if !k.c.conn.Capabilities()[tile.CapTxDDL].Supported {
			k.inTx.Store(false)
		}
	}
}

// StatementAt returns the statement around a byte offset, or the one before it when the
// offset sits between statements.
func (c *Conn) StatementAt(text string, offset int) string {
	spans := c.conn.Language().Split(text)
	found := ""
	for _, s := range spans {
		if s.Start > offset && found != "" {
			break
		}
		found = text[s.Start:s.End]
	}
	return found
}

// IsDDL reports whether a script changes object structure, so a front-end can refresh its tree.
func (c *Conn) IsDDL(text string) bool {
	lang := c.conn.Language()
	for _, s := range lang.Split(text) {
		if ddlVerbs[lang.Classify(text[s.Start:s.End]).Verb] {
			return true
		}
	}
	return false
}

// Layout describes a browsable object.
func (c *Conn) Layout(ctx context.Context, ref tile.ObjectRef) (tile.Layout, error) {
	b, ok := c.conn.(tile.Browser)
	if !ok {
		return tile.Layout{}, ErrUnsupported
	}
	return b.Layout(ctx, ref)
}

// Browse streams one page on a read-only session. It is engine-generated reading, so it
// skips the gate and history.
func (c *Conn) Browse(ctx context.Context, ref tile.ObjectRef, l tile.Layout, q tile.BrowseQuery, sink func(tile.Frame) error) error {
	b, ok := c.conn.(tile.Browser)
	if !ok {
		return ErrUnsupported
	}
	req, err := b.BrowseRequest(ref, l, q)
	if err != nil {
		return err
	}
	return c.readOnly(ctx, req, sink)
}

func (c *Conn) readOnly(ctx context.Context, req tile.Request, sink func(tile.Frame) error) error {
	sess, err := c.conn.Session(ctx, tile.SessionOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer sess.Close()
	_, _, err = execute(ctx, sess, req, func(_ int, f tile.Frame) error { return sink(f) }, 0)
	if errors.Is(err, ErrStop) {
		return nil
	}
	return err
}

// Explain plans one statement without running it.
func (c *Conn) Explain(ctx context.Context, stmt string, format tile.ExplainFormat, sink func(tile.Frame) error) error {
	e, ok := c.conn.(tile.Explainer)
	if !ok {
		return ErrUnsupported
	}
	if spans := c.conn.Language().Split(stmt); len(spans) != 1 {
		return fmt.Errorf("explain takes one statement, got %d", len(spans))
	}
	return c.readOnly(ctx, e.ExplainRequest(stmt, format), sink)
}

// Pending is a plan awaiting apply. Its hash proves the text previewed is the text run.
type Pending struct {
	Plan tile.Plan
	hash string
}

// PlanChanges turns intended edits into exact steps, with no side effects.
func (c *Conn) PlanChanges(ref tile.ObjectRef, l tile.Layout, cs tile.ChangeSet) (*Pending, error) {
	e, ok := c.conn.(tile.Editor)
	if !ok {
		return nil, ErrUnsupported
	}
	p, err := e.Plan(ref, l, cs)
	if err != nil {
		return nil, err
	}
	return &Pending{Plan: p, hash: planHash(p)}, nil
}

// Apply runs a previewed plan on one session. Any failure or row-count mismatch rolls the
// whole plan back, where the plan is atomic.
func (c *Conn) Apply(ctx context.Context, p *Pending, opt RunOptions) error {
	if planHash(p.Plan) != p.hash {
		return ErrPlanChanged
	}
	lang := c.conn.Language()
	classes := make([]tile.Classification, len(p.Plan.Steps))
	for i, st := range p.Plan.Steps {
		classes[i] = lang.Classify(st.Request.Text)
		if err := c.gate(st.Preview, classes[i], opt.Confirm); err != nil {
			return err
		}
	}
	sess, err := c.conn.Session(ctx, tile.SessionOptions{})
	if err != nil {
		return err
	}
	defer sess.Close()
	control := func(ctx context.Context, req tile.Request) error {
		if req.Text == "" {
			return nil
		}
		_, _, err := execute(ctx, sess, req, func(int, tile.Frame) error { return nil }, 0)
		return err
	}
	if err := control(ctx, p.Plan.Begin); err != nil {
		return err
	}
	rollback := func(cause error) error {
		// The caller's context may be why this is failing.
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return errors.Join(cause, control(rctx, p.Plan.Rollback))
	}
	for i, st := range p.Plan.Steps {
		start := time.Now()
		rows, affected, err := execute(ctx, sess, st.Request, func(int, tile.Frame) error { return nil }, 0)
		if err == nil && affected != st.ExpectRows {
			err = fmt.Errorf("%w: change %d matched %d rows, want %d, so the record changed since it was read; nothing was applied",
				ErrConflict, i+1, affected, st.ExpectRows)
		}
		// Plan text has placeholders, so no bound value reaches history or audit.
		if recErr := c.record(lang.Redact(st.Request.Text), classes[i], start, rows, affected, err); recErr != nil && err == nil {
			err = fmt.Errorf("recording statement: %w", recErr)
		}
		if err != nil {
			return rollback(err)
		}
	}
	if err := control(ctx, p.Plan.Commit); err != nil {
		return rollback(err)
	}
	return nil
}

func planHash(p tile.Plan) string {
	h := sha256.New()
	req := func(r tile.Request) {
		fmt.Fprintf(h, "%d:%s|", len(r.Text), r.Text)
		for _, a := range r.Args {
			fmt.Fprintf(h, "%d/%d:%s|", a.Kind, len(a.Raw), a.Raw)
		}
	}
	req(p.Begin)
	req(p.Commit)
	req(p.Rollback)
	for _, st := range p.Steps {
		req(st.Request)
		fmt.Fprintf(h, "%d|", st.ExpectRows)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Complete returns the text a chosen candidate replaces, and ranked candidates. It may read
// the catalog through ctx, so call it off the UI goroutine.
func (c *Conn) Complete(ctx context.Context, text string, cursor int) (string, []tile.Candidate) {
	lang, ok := c.conn.Language().(tile.Completer)
	if !ok || c.meta == nil {
		return "", nil
	}
	return lang.Complete(text, cursor, c.meta.Catalog(ctx))
}

// Refresh drops cached catalog data.
func (c *Conn) Refresh() {
	if c.meta != nil {
		c.meta.Invalidate()
	}
}
