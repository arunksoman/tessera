// Package engine holds what every front-end shares: opening profiles, the safety gate,
// statement execution, history, and audit.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/arunksoman/tessera/internal/secrets"
	"github.com/arunksoman/tessera/internal/store"
	"github.com/arunksoman/tessera/tile"
)

var (
	ErrReadOnly     = errors.New("this connection is read-only")
	ErrNeedsConfirm = errors.New("this statement needs confirmation")
	ErrDeclined     = errors.New("declined")
)

// EnvProduction raises confirmations on writes.
const EnvProduction = "production"

// SecretName is a profile secret's vault key.
func SecretName(profile, name string) string { return profile + "/" + name }

type Engine struct {
	store *store.Store
	vault *secrets.Vault
	actor string
	tiles map[string]tile.Tile
}

func New(st *store.Store, vault *secrets.Vault, actor string, tiles ...tile.Tile) *Engine {
	m := make(map[string]tile.Tile, len(tiles))
	for _, t := range tiles {
		m[t.Manifest().ID] = t
	}
	return &Engine{store: st, vault: vault, actor: actor, tiles: m}
}

// Tile returns a registered tile.
func (e *Engine) Tile(id string) (tile.Tile, bool) {
	t, ok := e.tiles[id]
	return t, ok
}

// Conn is an open profile.
type Conn struct {
	e       *Engine
	profile store.Profile
	conn    tile.Conn
}

func (e *Engine) Open(ctx context.Context, profile string) (*Conn, error) {
	p, err := e.store.Profile(profile)
	if err != nil {
		return nil, err
	}
	t, ok := e.tiles[p.Tile]
	if !ok {
		return nil, fmt.Errorf("profile %q uses unknown tile %q", p.Name, p.Tile)
	}
	env := tile.Env{Secret: func(_ context.Context, name string) ([]byte, error) {
		return e.vault.Get(SecretName(p.Name, name))
	}}
	c, err := t.Open(ctx, tile.ConnConfig{Values: p.Values}, env)
	if err != nil {
		return nil, fmt.Errorf("connecting to %q: %w", p.Name, err)
	}
	return &Conn{e: e, profile: p, conn: c}, nil
}

func (c *Conn) Close() error { return c.conn.Close() }

func (c *Conn) Profile() store.Profile { return c.profile }

func (c *Conn) Ping(ctx context.Context) error { return c.conn.Ping(ctx) }

// Tree returns the object tree.
func (c *Conn) Tree() tile.ObjectTree { return c.conn.Tree() }

// Prompt describes a statement awaiting confirmation.
type Prompt struct {
	Statement string
	Class     tile.Classification
	Env       string
}

// Confirm asks the user. A nil Confirm refuses anything that needs confirmation.
type Confirm func(Prompt) bool

type RunOptions struct {
	Confirm Confirm
}

// Sink receives each statement's frames. Raw bytes are valid only during the call.
type Sink func(statement int, f tile.Frame) error

// Run executes a script. Every statement passes the gate before the first one runs,
// so a refused script has no partial effect.
func (c *Conn) Run(ctx context.Context, text string, opt RunOptions, sink Sink) error {
	lang := c.conn.Language()
	spans := lang.Split(text)
	stmts := make([]string, len(spans))
	classes := make([]tile.Classification, len(spans))
	for i, s := range spans {
		stmts[i] = text[s.Start:s.End]
		classes[i] = lang.Classify(stmts[i])
		if err := c.gate(stmts[i], classes[i], opt.Confirm); err != nil {
			return err
		}
	}
	if len(stmts) == 0 {
		return nil
	}

	sess, err := c.conn.Session(ctx, tile.SessionOptions{ReadOnly: c.profile.ReadOnly})
	if err != nil {
		return err
	}
	defer sess.Close()
	for i, stmt := range stmts {
		start := time.Now()
		rows, affected, err := runOne(ctx, sess, stmt, i, sink)
		if recErr := c.record(lang.Redact(stmt), classes[i], start, rows, affected, err); recErr != nil && err == nil {
			err = fmt.Errorf("recording statement: %w", recErr)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *Conn) gate(stmt string, class tile.Classification, confirm Confirm) error {
	if c.profile.ReadOnly && !class.ReadOnly {
		return fmt.Errorf("%w: %s", ErrReadOnly, class.Reason)
	}
	needs := class.Risk == tile.RiskDestructive || (c.profile.Env == EnvProduction && class.Risk != tile.RiskNone)
	if !needs {
		return nil
	}
	if confirm == nil {
		return fmt.Errorf("%w: %s", ErrNeedsConfirm, class.Reason)
	}
	if !confirm(Prompt{Statement: stmt, Class: class, Env: c.profile.Env}) {
		return ErrDeclined
	}
	return nil
}

func runOne(ctx context.Context, sess tile.Session, stmt string, index int, sink Sink) (rows, affected int64, err error) {
	st, err := sess.Execute(ctx, tile.Request{Text: stmt})
	if err != nil {
		return 0, 0, err
	}
	defer st.Close()
	for {
		f, err := st.Next(ctx)
		if errors.Is(err, io.EOF) {
			return rows, affected, nil
		}
		if err != nil {
			return rows, affected, err
		}
		if f.Batch != nil {
			rows += int64(f.Batch.Rows)
		}
		if f.Done != nil && f.Done.HasAffected {
			affected = f.Done.RowsAffected
		}
		if err := sink(index, f); err != nil {
			return rows, affected, err
		}
	}
}

// record keeps redacted text only, so history and audit never hold a password.
func (c *Conn) record(redacted string, class tile.Classification, start time.Time, rows, affected int64, runErr error) error {
	msg := ""
	if runErr != nil {
		msg = runErr.Error()
	}
	err := c.e.store.AddHistory(store.HistoryEntry{
		Profile: c.profile.Name, Statement: redacted, ElapsedMS: time.Since(start).Milliseconds(), Rows: rows + affected, Error: msg,
	})
	if class.ReadOnly {
		return err
	}
	return errors.Join(err, c.e.store.AddAudit(store.AuditEntry{
		Actor: c.e.actor, Profile: c.profile.Name, Statement: redacted, Rows: affected, Error: msg,
	}))
}
