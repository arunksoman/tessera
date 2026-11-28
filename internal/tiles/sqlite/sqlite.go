// Package sqlite is the SQLite tile, on the pure-Go modernc.org/sqlite driver that tess
// already uses for its own state, so it adds no dependency and needs no cgo.
package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"github.com/arunksoman/tessera/internal/lang/sqlish"
	"github.com/arunksoman/tessera/internal/tiles/sqlgen"
	"github.com/arunksoman/tessera/tile"
)

const (
	batchRows  = 1000
	batchBytes = 1 << 20
)

// Tile is the SQLite plugin.
type Tile struct{}

func (Tile) Manifest() tile.Manifest {
	return tile.Manifest{
		ID: "sqlite", Name: "SQLite", APIVersion: tile.APIVersion,
		Style: tile.Style{Color: "verdigris", Glyph: "S"},
		ConnForm: tile.Form{Fields: []tile.FormField{
			{Name: "path", Label: "Database file", Type: "file", Required: true},
			{Name: "create", Label: "Create the file if it is missing", Type: "bool"},
		}},
	}
}

// dsn builds a file: URI from the path alone. The path is percent-encoded, so a '?' or '#' in
// it cannot add a parameter. A missing file is an error unless create is set, so a typo does not
// open a new empty database.
func dsn(values map[string]string) (string, error) {
	path := values["path"]
	if strings.TrimSpace(path) == "" {
		return "", errors.New("sqlite: no database file")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	mode := "rw"
	if values["create"] == "true" {
		mode = "rwc"
	}
	p := filepath.ToSlash(abs)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // C:/x becomes file:///C:/x
	}
	q := url.Values{
		"mode": {mode},
		// Defensive mode stops statements that corrupt the file on purpose, such as writable_schema.
		"_defensive":    {"1"},
		"_busy_timeout": {"5000"},
		"_foreign_keys": {"1"},
		// Functions with side effects cannot run from a view or trigger in the file itself.
		"_pragma": {"trusted_schema(0)"},
	}
	return (&url.URL{Scheme: "file", Path: p, RawQuery: q.Encode()}).String(), nil
}

func (Tile) Open(ctx context.Context, cfg tile.ConnConfig, _ tile.Env) (tile.Conn, error) {
	name, err := dsn(cfg.Values)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", name)
	if err != nil {
		return nil, err
	}
	c := &conn{db: db}
	if err := c.Ping(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: %s: %w", cfg.Values["path"], err)
	}
	return c, nil
}

type conn struct{ db *sql.DB }

func (c *conn) Capabilities() tile.Capabilities {
	on := tile.CapInfo{Supported: true}
	return tile.Capabilities{tile.CapTxMulti: on, tile.CapTxDDL: on, tile.CapCancel: on, tile.CapStream: on, tile.CapReadOnly: on}
}

func (c *conn) Language() tile.Language { return sqlish.SQLite.Language() }

func (c *conn) Tree() tile.ObjectTree { return tree{c} }

func (c *conn) Ping(ctx context.Context) error { return c.db.PingContext(ctx) }

func (c *conn) Close() error { return c.db.Close() }

func (c *conn) Session(ctx context.Context, opt tile.SessionOptions) (tile.Session, error) {
	sc, err := c.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	s := &session{sc: sc}
	if opt.ReadOnly {
		// SQLite itself refuses writes to every attached database while this is on.
		if _, err := sc.ExecContext(ctx, `PRAGMA query_only = 1`); err != nil {
			s.Close()
			return nil, err
		}
	}
	return s, nil
}

type session struct{ sc *sql.Conn }

// Close discards the connection rather than resetting it: temporary tables, attached databases,
// and PRAGMAs all live on it, and opening a new one costs little in process.
func (s *session) Close() error {
	if s.sc == nil {
		return nil
	}
	sc := s.sc
	s.sc = nil
	// A bad connection is closed and released, rather than returned to the pool.
	sc.Raw(func(any) error { return driver.ErrBadConn })
	return nil
}

// Ping fails once the session is closed.
func (s *session) Ping(ctx context.Context) error {
	if s.sc == nil {
		return errors.New("sqlite: session closed")
	}
	return s.sc.PingContext(ctx)
}

// bind passes values as text, so SQLite's column affinity converts them as it would a literal,
// and a guard comparing a column's text matches. Only bytes stay blobs.
func bind(vs []tile.Value) []any {
	args := make([]any, len(vs))
	for i, v := range vs {
		switch v.Kind {
		case tile.KindNull:
		case tile.KindBytes:
			args[i] = v.Raw
		default:
			args[i] = string(v.Raw)
		}
	}
	return args
}

func (s *session) Execute(ctx context.Context, req tile.Request) (tile.ResultStream, error) {
	st := &stream{s: s, ctx: ctx, text: req.Text}
	// changes() keeps the count of the last INSERT, UPDATE, or DELETE, even after DDL, so the
	// total before and after tells whether this statement changed anything.
	if err := s.sc.QueryRowContext(ctx, `SELECT total_changes()`).Scan(&st.before); err != nil {
		return nil, st.mapErr(err)
	}
	// The driver interrupts the statement when ctx ends; it runs in this goroutine, so a call
	// that has returned has stopped.
	rows, err := s.sc.QueryContext(ctx, req.Text, bind(req.Args)...)
	if err != nil {
		return nil, st.mapErr(err)
	}
	st.rows = rows
	return st, nil
}

type stream struct {
	s        *session
	ctx      context.Context
	text     string
	before   int64
	rows     *sql.Rows
	fields   []tile.Field
	vals     []any
	dest     []any
	started  bool
	scanned  bool // vals hold the next row already
	pending  bool
	finished bool
	arena    []byte
	cells    []tile.Value
}

func (st *stream) mapErr(err error) error {
	if cause := st.ctx.Err(); cause != nil {
		return fmt.Errorf("%w: %v", cause, err)
	}
	return err
}

func (st *stream) Next(context.Context) (tile.Frame, error) {
	if st.finished {
		return tile.Frame{}, io.EOF
	}
	if !st.started {
		st.started = true
		types, err := st.rows.ColumnTypes()
		if err != nil {
			return tile.Frame{}, st.mapErr(err)
		}
		st.pending = st.rows.Next()
		if len(types) > 0 {
			st.fields = make([]tile.Field, len(types))
			st.vals = make([]any, len(types))
			st.dest = make([]any, len(types))
			for i, t := range types {
				st.fields[i] = tile.Field{Name: t.Name(), NativeType: t.DatabaseTypeName(), Kind: kindOf(t.DatabaseTypeName())}
				st.dest[i] = &st.vals[i]
			}
			// An expression has no declared type, so it takes the kind of its first value.
			if st.pending {
				if err := st.rows.Scan(st.dest...); err != nil {
					return tile.Frame{}, st.mapErr(err)
				}
				st.scanned = true
				for i, v := range st.vals {
					if st.fields[i].NativeType == "" {
						st.fields[i].Kind = valueKind(v)
					}
				}
			}
			return tile.Frame{Schema: &tile.ResultSchema{Shape: tile.ShapeTabular, Fields: st.fields}}, nil
		}
	}
	if st.pending {
		return st.batch()
	}
	return st.done()
}

func (st *stream) batch() (tile.Frame, error) {
	st.arena, st.cells = st.arena[:0], st.cells[:0]
	rows := 0
	for st.pending && rows < batchRows && len(st.arena) < batchBytes {
		if !st.scanned {
			if err := st.rows.Scan(st.dest...); err != nil {
				return tile.Frame{}, st.mapErr(err)
			}
		}
		st.scanned = false
		for i, v := range st.vals {
			// Earlier cells keep the old array if append reallocates, so they stay valid.
			from := len(st.arena)
			kind := tile.KindNull
			switch v := v.(type) {
			case int64:
				kind = tile.KindInt
				if st.fields[i].Kind == tile.KindBool {
					kind = tile.KindBool
				}
				st.arena = strconv.AppendInt(st.arena, v, 10)
			case float64:
				kind = tile.KindFloat
				st.arena = strconv.AppendFloat(st.arena, v, 'g', -1, 64)
			case string:
				kind = textKind(st.fields[i].Kind)
				st.arena = append(st.arena, v...)
			case []byte:
				kind = tile.KindBytes
				st.arena = append(st.arena, v...)
			case time.Time:
				// The driver parses text in DATE, DATETIME, and TIMESTAMP columns. Browsing
				// selects those columns as text, so edits and paging see the stored value.
				kind = tile.KindTime
				st.arena = v.AppendFormat(st.arena, time.RFC3339Nano)
			case nil:
			default:
				kind = tile.KindOther
				st.arena = fmt.Append(st.arena, v)
			}
			cell := tile.Value{Kind: kind}
			if kind != tile.KindNull {
				cell.Raw = st.arena[from:len(st.arena):len(st.arena)]
			}
			st.cells = append(st.cells, cell)
		}
		rows++
		st.pending = st.rows.Next()
	}
	return tile.Frame{Batch: &tile.Batch{Rows: rows, Cells: st.cells}}, nil
}

func (st *stream) done() (tile.Frame, error) {
	st.finished = true
	err := st.rows.Err()
	if closeErr := st.rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return tile.Frame{}, st.mapErr(err)
	}
	sum := &tile.ResultSummary{}
	if st.fields == nil {
		var changes, total int64
		q := st.s.sc.QueryRowContext(context.WithoutCancel(st.ctx), `SELECT changes(), total_changes()`)
		if err := q.Scan(&changes, &total); err != nil {
			return tile.Frame{}, err
		}
		if total != st.before {
			sum.HasAffected, sum.RowsAffected = true, changes
		} else if c := sqlish.SQLite.Classify(st.text); !c.ReadOnly && !c.Schema && c.Tx == tile.TxNone {
			sum.HasAffected = true // a write that matched nothing
		}
	}
	return tile.Frame{Done: sum}, nil
}

// Close finalizes the statement, which stops it without reading the rows left.
func (st *stream) Close() error {
	if st.finished {
		return nil
	}
	st.finished = true
	return st.rows.Close()
}

// kindOf reads a declared type by SQLite's affinity rules, refined by common type names.
func kindOf(decl string) tile.ValueKind {
	t := strings.ToUpper(decl)
	has := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(t, s) {
				return true
			}
		}
		return false
	}
	switch {
	case t == "":
		return tile.KindOther
	case has("BOOL"):
		return tile.KindBool
	case has("INT"):
		return tile.KindInt
	case has("DATE", "TIME"):
		return tile.KindTime
	case has("JSON"):
		return tile.KindJSON
	case has("CHAR", "CLOB", "TEXT"):
		return tile.KindText
	case has("BLOB"):
		return tile.KindBytes
	case has("REAL", "FLOA", "DOUB"):
		return tile.KindFloat
	case has("DEC", "NUM"):
		return tile.KindDecimal
	}
	return tile.KindOther
}

// valueKind is the kind of a value as the driver returns it.
func valueKind(v any) tile.ValueKind {
	switch v.(type) {
	case int64:
		return tile.KindInt
	case float64:
		return tile.KindFloat
	case string:
		return tile.KindText
	case []byte:
		return tile.KindBytes
	case time.Time:
		return tile.KindTime
	}
	return tile.KindOther
}

// textKind is the kind of a text value in a field of the given kind. SQLite keeps any value in
// any column, so text in an integer column is still text.
func textKind(field tile.ValueKind) tile.ValueKind {
	switch field {
	case tile.KindTime, tile.KindJSON, tile.KindDecimal:
		return field
	}
	return tile.KindText
}

var dialect = sqlgen.Dialect{
	Quote:  quote,
	Mark:   func(int) string { return "?" },
	Text:   func(c string) string { return "CAST(" + c + " AS TEXT)" },
	Like:   "LIKE", // case-insensitive for ASCII
	Escape: ` ESCAPE '\'`,
	Column: func(f tile.Field) string {
		switch strings.ToUpper(f.NativeType) {
		case "DATE", "DATETIME", "TIMESTAMP":
			return "CAST(" + quote(f.Name) + " AS TEXT) AS " + quote(f.Name)
		}
		return quote(f.Name)
	},
	Begin:  "BEGIN",
	Family: sqlgen.FamilySQLite,
}

func quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func (c *conn) BrowseRequest(ref tile.ObjectRef, l tile.Layout, q tile.BrowseQuery) (tile.Request, error) {
	return dialect.Select(ref, l, q)
}

func (c *conn) Plan(ref tile.ObjectRef, l tile.Layout, cs tile.ChangeSet) (tile.Plan, error) {
	return dialect.Plan(ref, l, cs)
}

// ExplainRequest has no JSON form: SQLite plans only as rows.
func (c *conn) ExplainRequest(stmt string, format tile.ExplainFormat) tile.Request {
	if format == tile.ExplainJSON {
		return tile.Request{}
	}
	return tile.Request{Text: "EXPLAIN QUERY PLAN " + stmt}
}

// query runs a catalog statement and reads every value as text.
func (c *conn) query(ctx context.Context, text string, args ...any) ([][]sql.NullString, error) {
	rows, err := c.db.QueryContext(ctx, text, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out [][]sql.NullString
	for rows.Next() {
		r := make([]sql.NullString, len(cols))
		dest := make([]any, len(cols))
		for i := range r {
			dest[i] = &r[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type tree struct{ c *conn }

const (
	// temp is per connection, so a pooled tree cannot show what a session put there.
	schemasSQL = `SELECT name FROM pragma_database_list WHERE name <> 'temp' AND name > ? ORDER BY name LIMIT ?`
	tablesSQL  = `SELECT name, CASE type WHEN 'view' THEN 'view' ELSE 'table' END FROM pragma_table_list
		WHERE schema = ? AND type IN ('table', 'view', 'virtual') AND name NOT LIKE 'sqlite\_%' ESCAPE '\' AND name > ?
		ORDER BY name LIMIT ?`
	// Hidden columns of virtual tables are left out; generated ones are kept.
	columnsSQL = `SELECT name, type, CASE WHEN "notnull" THEN 'NO' ELSE 'YES' END, dflt_value, pk, hidden
		FROM pragma_table_xinfo(?1, ?2) WHERE hidden <> 1 ORDER BY cid`
	indexesSQL = `SELECT il.name,
			(SELECT group_concat(coalesce(name, '(expression)'), ', ') FROM
				(SELECT name FROM pragma_index_xinfo(il.name, ?2) WHERE key ORDER BY seqno)),
			CASE WHEN il."unique" THEN 'YES' ELSE 'NO' END
		FROM pragma_index_list(?1, ?2) il ORDER BY il.name`
	primarySQL   = `SELECT name FROM pragma_table_info(?1, ?2) WHERE pk > 0 ORDER BY pk`
	foreignSQL   = `SELECT id, "from", "table", "to", on_update, on_delete FROM pragma_foreign_key_list(?1, ?2) ORDER BY id, seq`
	uniqueKeySQL = `SELECT il.name, ii.name, ti."notnull"
		FROM pragma_index_list(?1, ?2) il
		JOIN pragma_index_xinfo(il.name, ?2) ii ON ii.key
		LEFT JOIN pragma_table_info(?1, ?2) ti ON ti.name = ii.name
		WHERE il."unique" AND NOT il.partial AND il.origin <> 'pk'
		ORDER BY il.name, ii.seqno`
)

func (t tree) Children(ctx context.Context, parent tile.ObjectRef, p tile.Page) ([]tile.Node, string, error) {
	stage := func(text string, node func(r []sql.NullString) tile.Node, args ...any) sqlgen.Stage {
		return func(after string, n int) ([]tile.Node, error) {
			rows, err := t.c.query(ctx, text, append(args, after, n)...)
			nodes := make([]tile.Node, len(rows))
			for i, r := range rows {
				nodes[i] = node(r)
			}
			return nodes, err
		}
	}
	switch len(parent.Path) {
	case 0:
		return sqlgen.PageStages(p.Limit, p.Cursor, stage(schemasSQL, func(r []sql.NullString) tile.Node {
			return tile.Node{Ref: parent.Child("schema", r[0].String), Kind: "schema", Label: r[0].String, HasChildren: true}
		}))
	case 1:
		return sqlgen.PageStages(p.Limit, p.Cursor, stage(tablesSQL, func(r []sql.NullString) tile.Node {
			name, kind := r[0].String, r[1].String
			return tile.Node{Ref: parent.Child(kind, name), Kind: kind, Label: name, Browsable: true}
		}, parent.Path[0].Name))
	}
	return nil, "", nil
}

func target(ref tile.ObjectRef) (string, string, error) {
	if len(ref.Path) != 2 {
		return "", "", fmt.Errorf("sqlite: %v is not a table or view", ref.Path)
	}
	return ref.Path[0].Name, ref.Path[1].Name, nil
}

func (t tree) Describe(ctx context.Context, ref tile.ObjectRef) (tile.ObjectDetail, error) {
	schema, name, err := target(ref)
	if err != nil {
		return tile.ObjectDetail{}, err
	}
	cols, err := t.c.query(ctx, columnsSQL, name, schema)
	if err != nil {
		return tile.ObjectDetail{}, err
	}
	if len(cols) == 0 {
		return tile.ObjectDetail{}, fmt.Errorf("sqlite: %s.%s not found", schema, name)
	}
	idx, err := t.c.query(ctx, indexesSQL, name, schema)
	if err != nil {
		return tile.ObjectDetail{}, err
	}
	return tile.ObjectDetail{Sections: []tile.DetailSection{
		section("Columns", []string{"name", "type", "nullable", "default"}, cols),
		section("Indexes", []string{"name", "columns", "unique"}, idx),
	}}, nil
}

func section(title string, names []string, rows [][]sql.NullString) tile.DetailSection {
	s := tile.DetailSection{Title: title}
	for _, n := range names {
		s.Fields = append(s.Fields, tile.Field{Name: n, Kind: tile.KindText})
	}
	for _, r := range rows {
		row := make([]tile.Value, len(names))
		for i := range names {
			row[i] = tile.Value{Kind: tile.KindNull}
			if r[i].Valid {
				row[i] = tile.Text(r[i].String)
			}
		}
		s.Rows = append(s.Rows, row)
	}
	return s
}

// DescribeKeys reads the primary key and the foreign keys. SQLite keeps no names for foreign
// keys, so each is named after its position and target.
func (t tree) DescribeKeys(ctx context.Context, ref tile.ObjectRef) (tile.Keys, error) {
	schema, name, err := target(ref)
	if err != nil {
		return tile.Keys{}, err
	}
	var k tile.Keys
	if k.Primary, err = t.primary(ctx, schema, name); err != nil {
		return k, err
	}
	fks, err := t.foreign(ctx, schema, name)
	for _, fk := range fks {
		k.Foreign = append(k.Foreign, fk.ForeignKey)
	}
	return k, err
}

func (t tree) primary(ctx context.Context, schema, name string) ([]string, error) {
	rows, err := t.c.query(ctx, primarySQL, name, schema)
	var out []string
	for _, r := range rows {
		out = append(out, r[0].String)
	}
	return out, err
}

// foreign reads foreign keys. A key that names no target columns refers to the target's
// primary key.
func (t tree) foreign(ctx context.Context, schema, name string) ([]tile.Relation, error) {
	rows, err := t.c.query(ctx, foreignSQL, name, schema)
	if err != nil {
		return nil, err
	}
	var out []tile.Relation
	for i := 0; i < len(rows); {
		id, table := rows[i][0].String, rows[i][2].String
		rel := tile.Relation{
			ForeignKey: tile.ForeignKey{Name: "fk" + id + "_" + table, Target: tile.ObjectRef{}.Child("schema", schema).Child("table", table)},
			OnUpdate:   rows[i][4].String, OnDelete: rows[i][5].String,
		}
		implicit := false
		for ; i < len(rows) && rows[i][0].String == id; i++ {
			rel.Fields = append(rel.Fields, rows[i][1].String)
			rel.TargetFields = append(rel.TargetFields, rows[i][3].String)
			implicit = implicit || !rows[i][3].Valid
		}
		if implicit {
			if rel.TargetFields, err = t.primary(ctx, schema, table); err != nil {
				return nil, err
			}
		}
		out = append(out, rel)
	}
	return out, nil
}

func (c *conn) Layout(ctx context.Context, ref tile.ObjectRef) (tile.Layout, error) {
	schema, name, err := target(ref)
	if err != nil {
		return tile.Layout{}, err
	}
	t := tree{c}
	cols, err := c.query(ctx, columnsSQL, name, schema)
	if err != nil {
		return tile.Layout{}, err
	}
	if len(cols) == 0 {
		return tile.Layout{}, fmt.Errorf("sqlite: %s.%s not found", schema, name)
	}
	l := tile.Layout{Estimate: -1}
	for _, r := range cols {
		l.Fields = append(l.Fields, tile.Field{Name: r[0].String, NativeType: r[1].String, Kind: kindOf(r[1].String)})
	}
	// A primary key identifies a record even where SQLite lets its columns hold NULL: a guard
	// on a NULL key matches nothing, which is reported as a conflict.
	if l.Key, err = t.primary(ctx, schema, name); err != nil {
		return tile.Layout{}, err
	}
	if len(l.Key) == 0 {
		rows, err := c.query(ctx, uniqueKeySQL, name, schema)
		if err != nil {
			return tile.Layout{}, err
		}
		l.Key = firstUsableKey(rows)
	}
	if len(l.Key) == 0 {
		l.Note = "no primary key or unique not-null key"
	}
	return l, nil
}

// firstUsableKey takes rows of (index, column, not null) and returns the first index whose
// columns are all NOT NULL. An expression column has no table column, so its index is skipped.
func firstUsableKey(rows [][]sql.NullString) []string {
	for i := 0; i < len(rows); {
		j, usable := i, true
		var key []string
		for ; j < len(rows) && rows[j][0].String == rows[i][0].String; j++ {
			key = append(key, rows[j][1].String)
			usable = usable && rows[j][1].Valid && rows[j][2].String == "1"
		}
		if usable {
			return key
		}
		i = j
	}
	return nil
}
