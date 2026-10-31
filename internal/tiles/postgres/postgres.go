// Package postgres is the PostgreSQL tile, built directly on pgconn so values
// pass through as raw text without per-value decoding.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/arunksoman/tessera/internal/lang/sqlish"
	"github.com/arunksoman/tessera/tile"
)

const (
	batchRows        = 1000
	batchBytes       = 1 << 20
	maxIdle          = 4
	sqlStateCanceled = "57014"
	// The driver default, prefer, silently falls back to plaintext.
	defaultSSLMode = "verify-full"
)

// Tile is the PostgreSQL plugin.
type Tile struct{}

func (Tile) Manifest() tile.Manifest {
	return tile.Manifest{
		ID: "postgres", Name: "PostgreSQL", APIVersion: tile.APIVersion,
		Style: tile.Style{Color: "lapis", Glyph: "P"},
		ConnForm: tile.Form{Fields: []tile.FormField{
			{Name: "host", Label: "Host or socket directory", Type: "text", Required: true, Default: "127.0.0.1"},
			{Name: "port", Label: "Port", Type: "int", Default: "5432"},
			{Name: "user", Label: "User", Type: "text", Required: true},
			{Name: "password", Label: "Password", Type: "secret"},
			{Name: "database", Label: "Database", Type: "text", Required: true},
			{Name: "sslmode", Label: "TLS mode", Type: "enum", Default: defaultSSLMode,
				Options: []string{"disable", "allow", "prefer", "require", "verify-ca", "verify-full"}},
			{Name: "sslrootcert", Label: "CA certificate", Type: "file"},
			{Name: "sslcert", Label: "Client certificate", Type: "file"},
			{Name: "sslkey", Label: "Client key", Type: "file"},
		}},
	}
}

// parseKeys are the only values passed to pgconn's parser.
var parseKeys = []string{"host", "port", "user", "database", "sslmode", "sslrootcert", "sslcert", "sslkey"}

// connString quotes each value, so a value cannot inject another setting. The password is
// not included: it is set on the parsed config, so it can never appear in a parse error.
func connString(values map[string]string) string {
	escape := strings.NewReplacer(`\`, `\\`, `'`, `\'`)
	var b strings.Builder
	for _, k := range parseKeys {
		v := values[k]
		if k == "sslmode" && v == "" {
			v = defaultSSLMode
		}
		if v != "" {
			fmt.Fprintf(&b, "%s='%s' ", k, escape.Replace(v))
		}
	}
	return b.String()
}

func (Tile) Open(ctx context.Context, cfg tile.ConnConfig, env tile.Env) (tile.Conn, error) {
	parsed, err := pgconn.ParseConfig(connString(cfg.Values))
	if err != nil {
		return nil, err
	}
	if env.Secret != nil {
		pw, err := env.Secret(ctx, "password")
		if err != nil {
			return nil, err
		}
		if len(pw) > 0 {
			parsed.Password = string(pw)
		}
	}
	if env.Dial != nil {
		dial := env.Dial
		parsed.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dial(ctx, network, addr)
		}
	}
	// A cancelled context sends a server-side cancel, keeping the connection usable.
	parsed.BuildContextWatcherHandler = func(c *pgconn.PgConn) ctxwatch.Handler {
		return &pgconn.CancelRequestContextWatcherHandler{Conn: c, DeadlineDelay: 2 * time.Second}
	}
	c := &conn{cfg: parsed}
	if err := c.Ping(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

// shortcut: no cap on open connections, add one when the engine schedules sessions.
type conn struct {
	cfg    *pgconn.Config
	mu     sync.Mutex
	idle   []*pgconn.PgConn
	closed bool
}

func (c *conn) Capabilities() tile.Capabilities {
	on := tile.CapInfo{Supported: true}
	return tile.Capabilities{tile.CapTxMulti: on, tile.CapTxDDL: on, tile.CapCancel: on, tile.CapStream: on, tile.CapReadOnly: on}
}

func (c *conn) Language() tile.Language { return sqlish.Postgres.Language() }

func (c *conn) Tree() tile.ObjectTree { return tree{c} }

func (c *conn) Ping(ctx context.Context) error {
	pg, err := c.acquire(ctx)
	if err != nil {
		return err
	}
	defer c.release(pg)
	return pg.Ping(ctx)
}

func (c *conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	for _, pg := range c.idle {
		pg.Close(context.Background())
	}
	c.idle = nil
	return nil
}

func (c *conn) acquire(ctx context.Context) (*pgconn.PgConn, error) {
	c.mu.Lock()
	for len(c.idle) > 0 {
		pg := c.idle[len(c.idle)-1]
		c.idle = c.idle[:len(c.idle)-1]
		if !pg.IsClosed() {
			c.mu.Unlock()
			return pg, nil
		}
	}
	c.mu.Unlock()
	return pgconn.ConnectConfig(ctx, c.cfg)
}

func (c *conn) release(pg *pgconn.PgConn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || pg.IsClosed() || len(c.idle) >= maxIdle {
		pg.Close(context.Background())
		return
	}
	c.idle = append(c.idle, pg)
}

func (c *conn) Session(ctx context.Context, opt tile.SessionOptions) (tile.Session, error) {
	pg, err := c.acquire(ctx)
	if err != nil {
		return nil, err
	}
	if opt.ReadOnly {
		if err := pg.Exec(ctx, "SET SESSION CHARACTERISTICS AS TRANSACTION READ ONLY").Close(); err != nil {
			pg.Close(context.Background())
			return nil, err
		}
	}
	return &session{c: c, pg: pg}, nil
}

type session struct {
	c  *conn
	pg *pgconn.PgConn
}

// Close resets all session state, so nothing leaks to the next user of the connection.
func (s *session) Close() error {
	if s.pg == nil {
		return nil
	}
	pg := s.pg
	s.pg = nil
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if pg.TxStatus() != 'I' || pg.Exec(ctx, "DISCARD ALL").Close() != nil {
		pg.Close(ctx)
	}
	s.c.release(pg)
	return nil
}

func (s *session) Execute(ctx context.Context, req tile.Request) (tile.ResultStream, error) {
	args := make([][]byte, len(req.Args))
	for i, a := range req.Args {
		if a.Kind != tile.KindNull {
			args[i] = a.Raw
		}
	}
	// ExecParams is the extended protocol, so one Request is always exactly one statement.
	return &stream{ctx: ctx, pg: s.pg, rr: s.pg.ExecParams(ctx, req.Text, args, nil, nil, nil)}, nil
}

type stream struct {
	ctx      context.Context
	pg       *pgconn.PgConn
	rr       *pgconn.ResultReader
	fields   []tile.Field
	started  bool
	pending  bool // rr is on a row not yet copied into a batch
	finished bool
	arena    []byte
	cells    []tile.Value
}

func (st *stream) Next(context.Context) (tile.Frame, error) {
	if st.finished {
		return tile.Frame{}, io.EOF
	}
	if !st.started {
		st.started = true
		st.pending = st.rr.NextRow() // also surfaces errors and the field list
		if fd := st.rr.FieldDescriptions(); len(fd) > 0 {
			st.fields = fieldsOf(fd)
			return tile.Frame{Schema: &tile.ResultSchema{Shape: tile.ShapeTabular, Fields: st.fields}}, nil
		}
	}
	if st.pending {
		return st.batch(), nil
	}
	return st.done()
}

func (st *stream) batch() tile.Frame {
	st.arena, st.cells = st.arena[:0], st.cells[:0]
	rows := 0
	for st.pending && rows < batchRows && len(st.arena) < batchBytes {
		for i, v := range st.rr.Values() {
			cell := tile.Value{Kind: tile.KindNull}
			if v != nil {
				// Earlier cells keep the old array if append reallocates, so they stay valid.
				from := len(st.arena)
				st.arena = append(st.arena, v...)
				cell = tile.Value{Kind: st.fields[i].Kind, Raw: st.arena[from:len(st.arena):len(st.arena)]}
			}
			st.cells = append(st.cells, cell)
		}
		rows++
		st.pending = st.rr.NextRow()
	}
	return tile.Frame{Batch: &tile.Batch{Rows: rows, Cells: st.cells}}
}

func (st *stream) done() (tile.Frame, error) {
	st.finished = true
	tag, err := st.rr.Close()
	if err != nil {
		if cause := st.ctx.Err(); cause != nil {
			return tile.Frame{}, fmt.Errorf("%w: %v", cause, err)
		}
		return tile.Frame{}, err
	}
	s := &tile.ResultSummary{Tag: tag.String()}
	if tag.Insert() || tag.Update() || tag.Delete() {
		s.HasAffected, s.RowsAffected = true, tag.RowsAffected()
	}
	return tile.Frame{Done: s}, nil
}

// Close stops the server before draining, so an early close never reads the whole result.
func (st *stream) Close() error {
	if st.finished {
		return nil
	}
	st.finished = true
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := st.pg.CancelRequest(ctx); err != nil {
		st.pg.Close(ctx)
		return nil
	}
	_, err := st.rr.Close()
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == sqlStateCanceled {
		return nil
	}
	return err
}

var typeNames = pgtype.NewMap()

func fieldsOf(fd []pgconn.FieldDescription) []tile.Field {
	out := make([]tile.Field, len(fd))
	for i, f := range fd {
		out[i] = tile.Field{Name: f.Name, NativeType: "oid:" + strconv.FormatUint(uint64(f.DataTypeOID), 10), Kind: kindOf(f.DataTypeOID)}
		if t, ok := typeNames.TypeForOID(f.DataTypeOID); ok {
			out[i].NativeType = t.Name
		}
	}
	return out
}

func kindOf(oid uint32) tile.ValueKind {
	switch oid {
	case pgtype.BoolOID:
		return tile.KindBool
	case pgtype.Int2OID, pgtype.Int4OID, pgtype.Int8OID, pgtype.OIDOID:
		return tile.KindInt
	case pgtype.NumericOID:
		return tile.KindDecimal
	case pgtype.Float4OID, pgtype.Float8OID:
		return tile.KindFloat
	case pgtype.TextOID, pgtype.VarcharOID, pgtype.BPCharOID, pgtype.NameOID:
		return tile.KindText
	case pgtype.ByteaOID:
		return tile.KindBytes
	case pgtype.DateOID, pgtype.TimeOID, pgtype.TimestampOID, pgtype.TimestamptzOID:
		return tile.KindTime
	case pgtype.JSONOID, pgtype.JSONBOID:
		return tile.KindJSON
	}
	return tile.KindOther
}

// query runs a catalog statement with bound text arguments.
func (c *conn) query(ctx context.Context, text string, args ...string) ([][][]byte, []pgconn.FieldDescription, error) {
	pg, err := c.acquire(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer c.release(pg)
	params := make([][]byte, len(args))
	for i, a := range args {
		params[i] = []byte(a)
	}
	res := pg.ExecParams(ctx, text, params, nil, nil, nil).Read()
	return res.Rows, res.FieldDescriptions, res.Err
}

type tree struct{ c *conn }

const (
	schemasSQL = `SELECT nspname FROM pg_namespace
		WHERE nspname !~ '^pg_' AND nspname <> 'information_schema' AND nspname > $1::name
		ORDER BY nspname LIMIT $2::int`
	relationsSQL = `SELECT c.relname, CASE WHEN c.relkind IN ('v', 'm') THEN 'view' ELSE 'table' END
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relkind IN ('r', 'p', 'v', 'm', 'f') AND c.relname > $2::name
		ORDER BY c.relname LIMIT $3::int`
	columnsSQL = `SELECT a.attname, format_type(a.atttypid, a.atttypmod),
			CASE WHEN a.attnotnull THEN 'NO' ELSE 'YES' END, pg_get_expr(d.adbin, d.adrelid)
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		WHERE n.nspname = $1 AND c.relname = $2 AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY a.attnum`
	indexesSQL = `SELECT i.relname, pg_get_indexdef(x.indexrelid)
		FROM pg_index x
		JOIN pg_class c ON c.oid = x.indrelid
		JOIN pg_class i ON i.oid = x.indexrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = $2
		ORDER BY i.relname`
)

func (t tree) Children(ctx context.Context, parent tile.ObjectRef, p tile.Page) ([]tile.Node, string, error) {
	limit := p.Limit
	if limit <= 0 {
		limit = 200
	}
	fetch := strconv.Itoa(limit + 1)
	var rows [][][]byte
	var err error
	switch len(parent.Path) {
	case 0:
		rows, _, err = t.c.query(ctx, schemasSQL, p.Cursor, fetch)
	case 1:
		rows, _, err = t.c.query(ctx, relationsSQL, parent.Path[0].Name, p.Cursor, fetch)
	default:
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = string(rows[limit-1][0])
	}
	nodes := make([]tile.Node, len(rows))
	for i, r := range rows {
		name := string(r[0])
		if len(parent.Path) == 0 {
			nodes[i] = tile.Node{Ref: parent.Child("schema", name), Kind: "schema", Label: name, HasChildren: true}
			continue
		}
		kind := string(r[1])
		nodes[i] = tile.Node{Ref: parent.Child(kind, name), Kind: kind, Label: name, Browsable: true}
	}
	return nodes, next, nil
}

func (t tree) Describe(ctx context.Context, ref tile.ObjectRef) (tile.ObjectDetail, error) {
	if len(ref.Path) != 2 {
		return tile.ObjectDetail{}, fmt.Errorf("postgres: cannot describe %v", ref.Path)
	}
	schema, name := ref.Path[0].Name, ref.Path[1].Name
	var d tile.ObjectDetail
	for _, part := range []struct{ title, sql string }{{"Columns", columnsSQL}, {"Indexes", indexesSQL}} {
		rows, fd, err := t.c.query(ctx, part.sql, schema, name)
		if err != nil {
			return d, err
		}
		if part.title == "Columns" && len(rows) == 0 {
			return d, fmt.Errorf("postgres: %s.%s not found", schema, name)
		}
		d.Sections = append(d.Sections, section(part.title, rows, fd))
	}
	return d, nil
}

func section(title string, rows [][][]byte, fd []pgconn.FieldDescription) tile.DetailSection {
	s := tile.DetailSection{Title: title, Fields: fieldsOf(fd)}
	for _, r := range rows {
		row := make([]tile.Value, len(r))
		for i, v := range r {
			row[i] = tile.Value{Kind: tile.KindNull}
			if v != nil {
				row[i] = tile.Value{Kind: tile.KindText, Raw: v}
			}
		}
		s.Rows = append(s.Rows, row)
	}
	return s
}
