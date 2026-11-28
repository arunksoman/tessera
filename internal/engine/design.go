package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/arunksoman/tessera/tile"
)

// Classify says what a statement does, as the safety gate sees it.
func (c *Conn) Classify(stmt string) tile.Classification {
	return c.conn.Language().Classify(stmt)
}

func (c *Conn) designer() (tile.Designer, error) {
	tree := c.conn.Tree()
	if tree == nil {
		return nil, ErrUnsupported
	}
	d, ok := tree.(tile.Designer)
	if !ok {
		return nil, ErrUnsupported
	}
	return d, nil
}

// Structure describes an object's fields, indexes, relations, and triggers.
func (c *Conn) Structure(ctx context.Context, ref tile.ObjectRef) (tile.Structure, error) {
	d, err := c.designer()
	if err != nil {
		return tile.Structure{}, err
	}
	return d.DescribeStructure(ctx, ref)
}

// CreateScript returns text that creates the object as it is now.
func (c *Conn) CreateScript(ctx context.Context, ref tile.ObjectRef) (string, error) {
	d, err := c.designer()
	if err != nil {
		return "", err
	}
	return d.CreateScript(ctx, ref)
}

// PlanStructure turns structure changes into exact steps, with no side effects. Apply runs
// them, through the gate, like edits.
func (c *Conn) PlanStructure(ref tile.ObjectRef, changes []tile.StructureChange) (*Pending, error) {
	d, err := c.designer()
	if err != nil {
		return nil, err
	}
	p, err := d.PlanStructure(ref, changes)
	if err != nil {
		return nil, err
	}
	if len(p.Steps) == 0 {
		return nil, errors.New("nothing to change")
	}
	return &Pending{Plan: p, hash: planHash(p)}, nil
}

// InsertScript renders records as text that adds them, for an SQL export.
func (c *Conn) InsertScript(ref tile.ObjectRef, fields []string, rows [][]tile.Value) (string, error) {
	l, ok := c.conn.(tile.Loader)
	if !ok {
		return "", ErrUnsupported
	}
	return l.InsertScript(ref, fields, rows)
}

// CanInsertScript reports whether InsertScript is supported, so an export can fail before
// it starts.
func (c *Conn) CanInsertScript() bool {
	_, ok := c.conn.(tile.Loader)
	return ok
}

const (
	loadRows = 500
	// SQLite allows 32766 bound values in one statement, PostgreSQL and MySQL 65535.
	loadValues = 30_000
	// exportPage is the page size for stores paged by a server cursor. Other stores stream
	// the whole object from one request.
	exportPage = 10_000
)

// Load adds records read from next until it returns io.EOF, in batches on one session. Every
// batch passes the gate. Where the tile's plan is atomic, all records go in one transaction,
// so a failure adds none. It returns how many records were added.
func (c *Conn) Load(ctx context.Context, ref tile.ObjectRef, fields []string, next func() ([]tile.Value, error), opt RunOptions) (int64, error) {
	l, ok := c.conn.(tile.Loader)
	if !ok {
		return 0, ErrUnsupported
	}
	if len(fields) == 0 {
		return 0, errors.New("no fields to load")
	}
	batch := max(1, min(loadRows, loadValues/len(fields)))
	lang := c.conn.Language()
	var (
		sess  tile.Session
		first tile.Plan
		total int64
		rows  = make([][]tile.Value, 0, batch)
	)
	control := func(ctx context.Context, req tile.Request) error {
		if req.Text == "" || sess == nil {
			return nil
		}
		_, _, err := execute(ctx, sess, req, func(int, tile.Frame) error { return nil }, 0)
		return err
	}
	flush := func() error {
		if len(rows) == 0 {
			return nil
		}
		p, err := l.InsertPlan(ref, fields, rows)
		if err != nil {
			return err
		}
		classes := make([]tile.Classification, len(p.Steps))
		for i, st := range p.Steps {
			classes[i] = lang.Classify(st.Request.Text)
			if err := c.gate(st.Request.Text, classes[i], opt.Confirm); err != nil {
				return err
			}
		}
		if sess == nil {
			if sess, err = c.conn.Session(ctx, tile.SessionOptions{}); err != nil {
				return err
			}
			first = p
			if err := control(ctx, p.Begin); err != nil {
				return err
			}
		}
		for i, st := range p.Steps {
			start := time.Now()
			n, affected, err := execute(ctx, sess, st.Request, func(int, tile.Frame) error { return nil }, 0)
			if err == nil && st.ExpectRows >= 0 && affected != st.ExpectRows {
				err = fmt.Errorf("%w: a batch added %d records, want %d", ErrConflict, affected, st.ExpectRows)
			}
			// The text has placeholders, so no loaded value reaches history or audit.
			if recErr := c.record(lang.Redact(st.Request.Text), classes[i], start, n, affected, err); recErr != nil && err == nil {
				err = fmt.Errorf("recording statement: %w", recErr)
			}
			if err != nil {
				return err
			}
		}
		total += int64(len(rows))
		rows = rows[:0]
		return nil
	}
	fail := func(cause error) (int64, error) {
		if sess == nil {
			return 0, cause
		}
		defer sess.Close()
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if first.Atomicity == tile.AtomicAll {
			total = 0
		}
		return total, errors.Join(cause, control(rctx, first.Rollback))
	}
	for {
		row, err := next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fail(err)
		}
		if len(row) != len(fields) {
			return fail(fmt.Errorf("record %d has %d values, want %d", total+int64(len(rows))+1, len(row), len(fields)))
		}
		rows = append(rows, row)
		if len(rows) == batch {
			if err := flush(); err != nil {
				return fail(err)
			}
		}
	}
	if err := flush(); err != nil {
		return fail(err)
	}
	if sess == nil {
		return 0, nil
	}
	defer sess.Close()
	if err := control(ctx, first.Commit); err != nil {
		return fail(err)
	}
	return total, nil
}

// Export streams every record of an object on read-only sessions: one Schema frame, then
// Batches. Like Browse, it is engine-generated reading, so it skips the gate and history.
func (c *Conn) Export(ctx context.Context, ref tile.ObjectRef, sink func(tile.Frame) error) error {
	l, err := c.Layout(ctx, ref)
	if err != nil {
		return err
	}
	q := tile.BrowseQuery{Limit: math.MaxInt}
	if l.Paging == tile.PagingCursor {
		q.Limit = exportPage
	}
	var schema bool
	var read int64
	for {
		rows, cursor := 0, ""
		err := c.Browse(ctx, ref, l, q, func(f tile.Frame) error {
			switch {
			case f.Schema != nil:
				if schema {
					return nil
				}
				schema = true
			case f.Batch != nil:
				rows += f.Batch.Rows
			case f.Done != nil:
				cursor = f.Done.Cursor
				return nil
			}
			return sink(f)
		})
		if err != nil {
			return err
		}
		read += int64(rows)
		if !MorePages(l, rows, q.Limit, cursor) {
			return nil
		}
		q = PageQuery(l, q, nil, read, cursor)
	}
}
