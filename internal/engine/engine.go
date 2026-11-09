// Package engine holds what every front-end shares: opening profiles, the safety gate,
// statement execution, history, and audit.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/arunksoman/tessera/internal/meta"
	"github.com/arunksoman/tessera/internal/secrets"
	"github.com/arunksoman/tessera/internal/store"
	"github.com/arunksoman/tessera/tile"
)

var (
	ErrReadOnly     = errors.New("this connection is read-only")
	ErrNeedsConfirm = errors.New("this statement needs confirmation")
	ErrDeclined     = errors.New("declined")
	// ErrStop is returned by a Sink to end the current statement early. It is not a failure:
	// the server's work stops, and the script continues with the next statement.
	ErrStop         = errors.New("stop reading")
	ErrSessionLost  = errors.New("the connection was lost and the open transaction rolled back")
	ErrConflict     = errors.New("conflict")
	ErrPlanChanged  = errors.New("the plan changed after it was previewed")
	ErrUnsupported  = errors.New("not supported by this connection")
	ErrTooManyConns = errors.New("too many open connections")
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
	meta    *meta.Cache
}

const metaTTL = 5 * time.Minute

// ddlVerbs change what the metadata cache holds.
var ddlVerbs = map[string]bool{"CREATE": true, "ALTER": true, "DROP": true, "RENAME": true}

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
	if settings, err := e.store.Settings(); err == nil && settings.HistoryKeep > 0 {
		e.store.PruneHistory(settings.HistoryKeep) // a failed prune must not stop a connection
	}
	var cache *meta.Cache
	if tree := c.Tree(); tree != nil {
		cache = meta.New(tree, metaTTL)
	}
	return &Conn{e: e, profile: p, conn: c, meta: cache}, nil
}

// Warnings names weak settings in a profile, such as a TLS mode that allows plaintext.
func (e *Engine) Warnings(p store.Profile) []string {
	if w, ok := e.tiles[p.Tile].(tile.ConfigWarner); ok {
		return w.ConfigWarnings(tile.ConnConfig{Values: p.Values})
	}
	return nil
}

// Store returns the engine's store, for front-ends that manage profiles.
func (e *Engine) Store() *store.Store { return e.store }

// SetSecret stores one of a profile's secrets in the vault.
func (e *Engine) SetSecret(profile, name string, value []byte) error {
	return e.vault.Set(SecretName(profile, name), value)
}

// HasSecret reports whether a profile's secret is set, without revealing it.
func (e *Engine) HasSecret(profile, name string) (bool, error) {
	v, err := e.vault.Get(SecretName(profile, name))
	return len(v) > 0, err
}

// DeleteSecret removes one of a profile's secrets from the vault.
func (e *Engine) DeleteSecret(profile, name string) error {
	return e.vault.Delete(SecretName(profile, name))
}

// MoveSecret keeps a secret when a profile is renamed.
func (e *Engine) MoveSecret(from, to, name string) error {
	v, err := e.vault.Get(SecretName(from, name))
	if err != nil || len(v) == 0 {
		return err
	}
	if err := e.vault.Set(SecretName(to, name), v); err != nil {
		return err
	}
	return e.vault.Delete(SecretName(from, name))
}

// Tiles returns the registered tiles sorted by ID.
func (e *Engine) Tiles() []tile.Tile {
	out := make([]tile.Tile, 0, len(e.tiles))
	for _, t := range e.tiles {
		out = append(out, t)
	}
	slices.SortFunc(out, func(a, b tile.Tile) int { return strings.Compare(a.Manifest().ID, b.Manifest().ID) })
	return out
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
	stmts, classes, err := c.prepare(text, opt.Confirm)
	if err != nil || len(stmts) == 0 {
		return err
	}
	sess, err := c.conn.Session(ctx, tile.SessionOptions{ReadOnly: c.profile.ReadOnly})
	if err != nil {
		return err
	}
	defer sess.Close()
	return c.runAll(ctx, sess, stmts, classes, sink, func(string, tile.Classification) {})
}

// Prompts lists the statements in a script that need confirmation, without running any.
// A read-only profile's refusal is returned as an error, since no confirmation lifts it.
func (c *Conn) Prompts(text string) ([]Prompt, error) {
	var out []Prompt
	_, _, err := c.prepare(text, func(p Prompt) bool { out = append(out, p); return true })
	return out, err
}

func (c *Conn) prepare(text string, confirm Confirm) ([]string, []tile.Classification, error) {
	lang := c.conn.Language()
	spans := lang.Split(text)
	stmts := make([]string, len(spans))
	classes := make([]tile.Classification, len(spans))
	for i, s := range spans {
		stmts[i] = text[s.Start:s.End]
		classes[i] = lang.Classify(stmts[i])
		if err := c.gate(stmts[i], classes[i], confirm); err != nil {
			return nil, nil, err
		}
	}
	return stmts, classes, nil
}

func (c *Conn) runAll(ctx context.Context, sess tile.Session, stmts []string, classes []tile.Classification, sink Sink, ran func(string, tile.Classification)) error {
	lang := c.conn.Language()
	for i, stmt := range stmts {
		start := time.Now()
		rows, affected, err := runOne(ctx, sess, stmt, i, sink)
		if errors.Is(err, ErrStop) {
			err = nil
		}
		if recErr := c.record(lang.Redact(stmt), classes[i], start, rows, affected, err); recErr != nil && err == nil {
			err = fmt.Errorf("recording statement: %w", recErr)
		}
		if err != nil {
			return err
		}
		ran(stmt, classes[i])
		if !classes[i].ReadOnly && ddlVerbs[classes[i].Verb] && c.meta != nil {
			c.meta.Invalidate()
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
	return execute(ctx, sess, tile.Request{Text: stmt}, sink, index)
}

func execute(ctx context.Context, sess tile.Session, req tile.Request, sink Sink, index int) (rows, affected int64, err error) {
	st, err := sess.Execute(ctx, req)
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
	var err error
	if !c.profile.NoHistory {
		err = c.e.store.AddHistory(store.HistoryEntry{
			Profile: c.profile.Name, Statement: redacted, ElapsedMS: time.Since(start).Milliseconds(), Rows: rows + affected, Error: msg,
		})
	}
	if class.ReadOnly {
		return err
	}
	return errors.Join(err, c.e.store.AddAudit(store.AuditEntry{
		Actor: c.e.actor, Profile: c.profile.Name, Statement: redacted, Rows: affected, Error: msg,
	}))
}
