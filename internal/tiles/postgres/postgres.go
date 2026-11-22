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
	"github.com/arunksoman/tessera/internal/tiles/sqlgen"
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
	// System schemas come after user ones. Toast and temporary schemas stay hidden.
	schemasSQL = `SELECT nspname FROM pg_namespace
		WHERE nspname !~ '^pg_(toast|temp_)' AND (nspname IN ('pg_catalog', 'information_schema')) = $1::bool
			AND nspname > $2::name
		ORDER BY nspname LIMIT $3::int`
	relationsSQL = `SELECT c.relname, CASE WHEN c.relkind IN ('v', 'm') THEN 'view' ELSE 'table' END
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relkind IN ('r', 'p', 'v', 'm', 'f') AND c.relname > $2::name
		ORDER BY c.relname LIMIT $3::int`
	// A routine's ref names its signature, which tells overloads apart.
	signature   = `(p.proname || '(' || pg_get_function_identity_arguments(p.oid) || ')') COLLATE "C"`
	routinesSQL = `SELECT ` + signature + `, p.proname,
			CASE p.prokind WHEN 'p' THEN 'procedure' WHEN 'a' THEN 'aggregate' ELSE 'function' END
		FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = $1 AND ` + signature + ` > $2::text
		ORDER BY 1 LIMIT $3::int`
	// One row per argument, then the result, which procedures lack.
	routineSQL = `SELECT COALESCE(NULLIF(a.name, ''), '$' || a.ord),
			CASE a.mode WHEN 'o' THEN 'OUT ' WHEN 'b' THEN 'INOUT ' WHEN 'v' THEN 'VARIADIC ' WHEN 't' THEN 'OUT ' ELSE '' END
				|| format_type(a.typ, NULL),
			'parameter', a.ord
		FROM pg_proc p
		JOIN pg_namespace n ON n.oid = p.pronamespace
		CROSS JOIN LATERAL unnest(COALESCE(p.proallargtypes, p.proargtypes::oid[]), p.proargnames, p.proargmodes)
			WITH ORDINALITY AS a(typ, name, mode, ord)
		WHERE n.nspname = $1 AND ` + signature + ` = $2
		UNION ALL
		SELECT 'returns', pg_get_function_result(p.oid), 'return', 1000000
		FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = $1 AND ` + signature + ` = $2 AND p.prokind <> 'p'
		ORDER BY 4`
	// One row per column of the primary key, then of each foreign key, in key order.
	keyColumnsSQL = `SELECT con.contype::text, con.conname, a.attname, fn.nspname, fc.relname, fa.attname
		FROM pg_constraint con
		JOIN pg_class c ON c.oid = con.conrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		CROSS JOIN LATERAL unnest(con.conkey, con.confkey) WITH ORDINALITY AS k(attnum, fattnum, ord)
		JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum
		LEFT JOIN pg_class fc ON fc.oid = con.confrelid
		LEFT JOIN pg_namespace fn ON fn.oid = fc.relnamespace
		LEFT JOIN pg_attribute fa ON fa.attrelid = con.confrelid AND fa.attnum = k.fattnum
		WHERE con.contype IN ('p', 'f') AND n.nspname = $1 AND c.relname = $2
		ORDER BY con.contype DESC, con.conname, k.ord`
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

// routineKinds are the kinds of routine nodes, whose children are parameters.
var routineKinds = map[string]bool{"function": true, "procedure": true, "aggregate": true}

func (t tree) Children(ctx context.Context, parent tile.ObjectRef, p tile.Page) ([]tile.Node, string, error) {
	// stage lists nodes from one catalog query, whose bound arguments end with after and n.
	stage := func(text string, node func(r [][]byte) tile.Node, args ...string) sqlgen.Stage {
		return func(after string, n int) ([]tile.Node, error) {
			rows, _, err := t.c.query(ctx, text, append(args, after, strconv.Itoa(n))...)
			nodes := make([]tile.Node, len(rows))
			for i, r := range rows {
				nodes[i] = node(r)
			}
			return nodes, err
		}
	}
	switch len(parent.Path) {
	case 0:
		schema := func(r [][]byte) tile.Node {
			name := string(r[0])
			return tile.Node{Ref: parent.Child("schema", name), Kind: "schema", Label: name, HasChildren: true}
		}
		return sqlgen.PageStages(p.Limit, p.Cursor, stage(schemasSQL, schema, "false"), stage(schemasSQL, schema, "true"))
	case 1:
		relation := func(r [][]byte) tile.Node {
			name, kind := string(r[0]), string(r[1])
			return tile.Node{Ref: parent.Child(kind, name), Kind: kind, Label: name, Browsable: true}
		}
		routine := func(r [][]byte) tile.Node {
			kind := string(r[2])
			return tile.Node{Ref: parent.Child(kind, string(r[0])), Kind: kind, Label: string(r[1]), HasChildren: true}
		}
		schema := parent.Path[0].Name
		return sqlgen.PageStages(p.Limit, p.Cursor, stage(relationsSQL, relation, schema), stage(routinesSQL, routine, schema))
	case 2:
		if !routineKinds[parent.Path[1].Kind] {
			return nil, "", nil
		}
		rows, _, err := t.c.query(ctx, routineSQL, parent.Path[0].Name, parent.Path[1].Name)
		if err != nil {
			return nil, "", err
		}
		nodes := make([]tile.Node, len(rows))
		for i, r := range rows {
			name, kind := string(r[0]), string(r[2])
			nodes[i] = tile.Node{Ref: parent.Child(kind, name), Kind: kind, Label: name, Detail: string(r[1])}
		}
		return sqlgen.PageSlice(nodes, p)
	}
	return nil, "", nil
}

// DescribeKeys reads the primary key and the foreign keys from the catalog.
func (t tree) DescribeKeys(ctx context.Context, ref tile.ObjectRef) (tile.Keys, error) {
	if len(ref.Path) != 2 {
		return tile.Keys{}, fmt.Errorf("postgres: cannot describe keys of %v", ref.Path)
	}
	rows, _, err := t.c.query(ctx, keyColumnsSQL, ref.Path[0].Name, ref.Path[1].Name)
	if err != nil {
		return tile.Keys{}, err
	}
	var k tile.Keys
	for _, r := range rows {
		if string(r[0]) == "p" {
			k.Primary = append(k.Primary, string(r[2]))
			continue
		}
		k.Foreign = sqlgen.AddForeign(k.Foreign, string(r[1]), string(r[2]), string(r[3]), string(r[4]), string(r[5]))
	}
	return k, nil
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

// Ping fails once the session is closed or the server has gone away.
func (s *session) Ping(ctx context.Context) error {
	if s.pg == nil {
		return errors.New("postgres: session closed")
	}
	return s.pg.Ping(ctx)
}

var dialect = sqlgen.Dialect{
	Quote:  func(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` },
	Mark:   func(i int) string { return "$" + strconv.Itoa(i) },
	Text:   func(c string) string { return c + "::text" },
	Like:   "ILIKE",
	Begin:  "BEGIN",
	Family: sqlgen.FamilyPostgres,
}

const (
	estimateSQL = `SELECT c.reltuples::bigint FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = $2`
	// One row per column of each usable unique index, primary key first.
	keysSQL = `SELECT x.indexrelid::text, a.attname, a.attnotnull::text
		FROM pg_index x
		JOIN pg_class c ON c.oid = x.indrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		CROSS JOIN LATERAL unnest(x.indkey) WITH ORDINALITY k(attnum, ord)
		JOIN pg_attribute a ON a.attrelid = x.indrelid AND a.attnum = k.attnum
		WHERE n.nspname = $1 AND c.relname = $2
			AND x.indisunique AND x.indisvalid AND x.indpred IS NULL AND x.indexprs IS NULL
		ORDER BY x.indisprimary DESC, x.indexrelid, k.ord`
)

func (c *conn) Layout(ctx context.Context, ref tile.ObjectRef) (tile.Layout, error) {
	if len(ref.Path) != 2 {
		return tile.Layout{}, fmt.Errorf("postgres: cannot browse %v", ref.Path)
	}
	schema, name := ref.Path[0].Name, ref.Path[1].Name
	cols, _, err := c.query(ctx, columnsSQL, schema, name)
	if err != nil {
		return tile.Layout{}, err
	}
	if len(cols) == 0 {
		return tile.Layout{}, fmt.Errorf("postgres: %s.%s not found", schema, name)
	}
	l := tile.Layout{Estimate: -1}
	for _, r := range cols {
		l.Fields = append(l.Fields, tile.Field{Name: string(r[0]), NativeType: string(r[1])})
	}
	rows, _, err := c.query(ctx, keysSQL, schema, name)
	if err != nil {
		return tile.Layout{}, err
	}
	l.Key = firstUsableKey(rows)
	if len(l.Key) == 0 {
		l.Note = "no primary key or unique not-null key"
	}
	est, _, err := c.query(ctx, estimateSQL, schema, name)
	if err != nil {
		return tile.Layout{}, err
	}
	if len(est) == 1 {
		// A table never analyzed reports -1 from PostgreSQL 14 on.
		if n, err := strconv.ParseInt(string(est[0][0]), 10, 64); err == nil {
			l.Estimate = n
		}
	}
	return l, nil
}

// firstUsableKey takes rows of (index, column, not null) and returns the first index whose
// columns are all NOT NULL, since a nullable unique column does not identify one record.
func firstUsableKey(rows [][][]byte) []string {
	for i := 0; i < len(rows); {
		j, usable := i, true
		var key []string
		for ; j < len(rows) && string(rows[j][0]) == string(rows[i][0]); j++ {
			key = append(key, string(rows[j][1]))
			usable = usable && string(rows[j][2]) == "true"
		}
		if usable {
			return key
		}
		i = j
	}
	return nil
}

func (c *conn) BrowseRequest(ref tile.ObjectRef, l tile.Layout, q tile.BrowseQuery) (tile.Request, error) {
	return dialect.Select(ref, l, q)
}

func (c *conn) Plan(ref tile.ObjectRef, l tile.Layout, cs tile.ChangeSet) (tile.Plan, error) {
	return dialect.Plan(ref, l, cs)
}

func (c *conn) ExplainRequest(stmt string, format tile.ExplainFormat) tile.Request {
	if format == tile.ExplainJSON {
		return tile.Request{Text: "EXPLAIN (FORMAT JSON) " + stmt}
	}
	return tile.Request{Text: "EXPLAIN " + stmt}
}

// ConfigWarnings names TLS modes that can talk to the wrong server or in plaintext.
func (Tile) ConfigWarnings(cfg tile.ConnConfig) []string {
	switch cfg.Values["sslmode"] {
	case "disable", "allow", "prefer":
		return []string{"TLS mode " + cfg.Values["sslmode"] + " can send data and passwords in plaintext"}
	case "require":
		return []string{"TLS mode require encrypts but does not verify the server's identity"}
	}
	return nil
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
