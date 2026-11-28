package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/duckdb/duckdb-go/v2"

	"github.com/arunksoman/tessera/internal/lang/sqlish"
	"github.com/arunksoman/tessera/internal/tiles/sqlgen"
	"github.com/arunksoman/tessera/tile"
)

const (
	batchRows  = 1000
	batchBytes = 1 << 20
)

// Tile is the DuckDB plugin.
type Tile struct{}

func (Tile) Manifest() tile.Manifest {
	return tile.Manifest{
		ID: "duckdb", Name: "DuckDB", APIVersion: tile.APIVersion, Version: version, MinTess: minTess, Stability: "beta",
		Style: tile.Style{Color: "ochre", Glyph: "D"},
		ConnForm: tile.Form{Fields: []tile.FormField{
			{Name: "path", Label: "Database file", Type: "file", Required: true},
			{Name: "create", Label: "Create the file if it is missing", Type: "bool"},
			{Name: "external", Label: "Let SQL read and write other files and URLs", Type: "bool"},
		}},
	}
}

// dsn builds the driver's connection string from the form, field by field. The driver splits
// the path from settings at the first '?', so a path holding one is refused. Extensions never
// install or load on their own, so a view in an untrusted file cannot pull in httpfs to send data
// out. Unless external is set, SQL cannot touch any file or URL but the database itself.
func dsn(values map[string]string) (string, error) {
	path := values["path"]
	if strings.TrimSpace(path) == "" {
		return "", errors.New("duckdb: no database file")
	}
	if strings.ContainsRune(path, '?') {
		return "", errors.New("duckdb: a database file name cannot contain '?'")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	// A missing file is an error unless create is set, so a typo does not make a new database.
	if _, err := os.Stat(abs); err != nil && values["create"] != "true" {
		return "", fmt.Errorf("duckdb: %w", err)
	}
	q := url.Values{
		"autoinstall_known_extensions": {"false"},
		"autoload_known_extensions":    {"false"},
		"allow_community_extensions":   {"false"},
	}
	if values["external"] != "true" {
		q.Set("enable_external_access", "false")
	}
	return abs + "?" + q.Encode(), nil
}

func (Tile) Open(ctx context.Context, cfg tile.ConnConfig, _ tile.Env) (tile.Conn, error) {
	name, err := dsn(cfg.Values)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("duckdb", name)
	if err != nil {
		return nil, err
	}
	c := &conn{db: db}
	if err := c.Ping(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("duckdb: %s: %w", cfg.Values["path"], err)
	}
	return c, nil
}

type conn struct{ db *sql.DB }

// Capabilities leaves out server-side read-only: DuckDB sets access mode for the whole database
// when it opens, not per connection, so read-only rests on the engine's gate.
func (c *conn) Capabilities() tile.Capabilities {
	on := tile.CapInfo{Supported: true}
	return tile.Capabilities{tile.CapTxMulti: on, tile.CapTxDDL: on, tile.CapCancel: on, tile.CapStream: on}
}

func (c *conn) Language() tile.Language { return sqlish.DuckDB.Language() }

func (c *conn) Tree() tile.ObjectTree { return tree{c} }

func (c *conn) Ping(ctx context.Context) error { return c.db.PingContext(ctx) }

func (c *conn) Close() error { return c.db.Close() }

func (c *conn) Session(ctx context.Context, _ tile.SessionOptions) (tile.Session, error) {
	sc, err := c.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	return &session{sc: sc}, nil
}

type session struct{ sc *sql.Conn }

// Close discards the connection rather than resetting it: temporary tables, attached databases,
// and settings all live on it, and opening a new one costs little in process.
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
		return errors.New("duckdb: session closed")
	}
	return s.sc.PingContext(ctx)
}

// bind passes values as text, which DuckDB casts to the parameter's type. Only bytes stay blobs.
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
	st := &stream{ctx: ctx, class: sqlish.DuckDB.Classify(req.Text)}
	// The driver interrupts the statement when ctx ends, and the statement runs in this process,
	// so a call that has returned has stopped.
	rows, err := s.sc.QueryContext(ctx, req.Text, bind(req.Args)...)
	if err != nil {
		return nil, st.mapErr(err)
	}
	st.rows = rows
	return st, nil
}

type stream struct {
	ctx      context.Context
	class    tile.Classification
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
		st.fields = make([]tile.Field, len(types))
		st.vals = make([]any, len(types))
		st.dest = make([]any, len(types))
		for i, t := range types {
			st.fields[i] = tile.Field{Name: t.Name(), NativeType: t.DatabaseTypeName(), Kind: kindOf(t.DatabaseTypeName())}
			st.dest[i] = &st.vals[i]
		}
		if st.pending = st.rows.Next(); st.pending {
			if err := st.rows.Scan(st.dest...); err != nil {
				return tile.Frame{}, st.mapErr(err)
			}
			st.scanned = true
		}
		if sum, ok := st.command(); ok {
			return st.done(sum)
		}
		return tile.Frame{Schema: &tile.ResultSchema{Shape: tile.ShapeTabular, Fields: st.fields}}, nil
	}
	if st.pending {
		return st.batch()
	}
	return st.done(&tile.ResultSummary{})
}

// command recognises the result DuckDB returns for a statement that is not a query: one Count
// column for a write, one Success column for anything else. The language must agree that the
// statement is not a read, so a query naming its column Count still returns rows.
// shortcut: a write with RETURNING whose only column is a BIGINT named Count reads as a count;
// switch to the driver's statement type if that ever matters.
func (st *stream) command() (*tile.ResultSummary, bool) {
	if st.class.ReadOnly && st.class.Tx == tile.TxNone || len(st.fields) != 1 {
		return nil, false
	}
	switch f := st.fields[0]; {
	case f.Name == "Success" && f.NativeType == "BOOLEAN":
		return &tile.ResultSummary{}, true
	case f.Name == "Count" && f.NativeType == "BIGINT":
		sum := &tile.ResultSummary{}
		if n, ok := st.vals[0].(int64); ok && st.scanned {
			sum.HasAffected, sum.RowsAffected = true, n
		} else if !st.class.Schema {
			sum.HasAffected = true // a write that matched nothing
		}
		return sum, true
	}
	return nil, false
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
			var kind tile.ValueKind
			st.arena, kind = appendValue(st.arena, st.fields[i], v)
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

func (st *stream) done(sum *tile.ResultSummary) (tile.Frame, error) {
	st.finished = true
	err := st.rows.Err()
	if closeErr := st.rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return tile.Frame{}, st.mapErr(err)
	}
	return tile.Frame{Done: sum}, nil
}

// Close stops the statement without reading the rows left.
func (st *stream) Close() error {
	if st.finished {
		return nil
	}
	st.finished = true
	return st.rows.Close()
}

// kindOf maps a DuckDB type name to a value kind.
func kindOf(name string) tile.ValueKind {
	switch {
	case name == "BOOLEAN":
		return tile.KindBool
	case slices.Contains(intTypes, name):
		return tile.KindInt
	case strings.HasPrefix(name, "DECIMAL"):
		return tile.KindDecimal
	case name == "FLOAT" || name == "DOUBLE":
		return tile.KindFloat
	case name == "VARCHAR" || name == "BIT" || strings.HasPrefix(name, "ENUM("):
		return tile.KindText
	case name == "BLOB":
		return tile.KindBytes
	case name == "DATE" || strings.HasPrefix(name, "TIME"):
		return tile.KindTime
	case name == "JSON" || strings.HasSuffix(name, "]") || strings.HasPrefix(name, "STRUCT(") ||
		strings.HasPrefix(name, "MAP(") || strings.HasPrefix(name, "UNION("):
		return tile.KindJSON
	}
	return tile.KindOther
}

var intTypes = []string{"TINYINT", "SMALLINT", "INTEGER", "BIGINT", "HUGEINT", "UTINYINT", "USMALLINT",
	"UINTEGER", "UBIGINT", "UHUGEINT", "BIGNUM", "VARINT"}

// appendValue renders a value as DuckDB's own text for it, so integers and decimals stay exact.
func appendValue(b []byte, f tile.Field, v any) ([]byte, tile.ValueKind) {
	switch v := v.(type) {
	case nil:
		return b, tile.KindNull
	case bool:
		return strconv.AppendBool(b, v), tile.KindBool
	case int8, int16, int32, int64:
		return strconv.AppendInt(b, reflectInt(v), 10), tile.KindInt
	case uint8, uint16, uint32, uint64:
		return strconv.AppendUint(b, reflectUint(v), 10), tile.KindInt
	case *big.Int:
		return v.Append(b, 10), tile.KindInt
	case duckdb.Decimal:
		return append(b, decimalText(v)...), tile.KindDecimal
	case float32:
		return strconv.AppendFloat(b, float64(v), 'g', -1, 32), tile.KindFloat
	case float64:
		return strconv.AppendFloat(b, v, 'g', -1, 64), tile.KindFloat
	case string:
		return append(b, v...), textKind(f.Kind)
	case []byte:
		if f.NativeType == "UUID" && len(v) == 16 {
			return appendUUID(b, v), tile.KindOther
		}
		return append(b, v...), tile.KindBytes
	case time.Time:
		return appendTime(b, f.NativeType, v), tile.KindTime
	case duckdb.Interval:
		return append(b, intervalText(v)...), tile.KindOther
	}
	// Lists, structs, maps, unions, and JSON arrive as Go values and leave as JSON.
	j, err := json.Marshal(jsonable(v))
	if err != nil {
		return fmt.Append(b, v), tile.KindOther
	}
	return append(b, j...), tile.KindJSON
}

func reflectInt(v any) int64 {
	switch v := v.(type) {
	case int8:
		return int64(v)
	case int16:
		return int64(v)
	case int32:
		return int64(v)
	}
	return v.(int64)
}

func reflectUint(v any) uint64 {
	switch v := v.(type) {
	case uint8:
		return uint64(v)
	case uint16:
		return uint64(v)
	case uint32:
		return uint64(v)
	}
	return v.(uint64)
}

// textKind keeps a string's field kind where text is that kind's form.
func textKind(field tile.ValueKind) tile.ValueKind {
	if field == tile.KindOther {
		return tile.KindOther
	}
	return tile.KindText
}

// decimalText keeps every digit of the scale, as DuckDB prints a DECIMAL.
func decimalText(d duckdb.Decimal) string {
	digits := new(big.Int).Abs(d.Value).String()
	sign := ""
	if d.Value.Sign() < 0 {
		sign = "-"
	}
	scale := int(d.Scale)
	if scale == 0 {
		return sign + digits
	}
	if len(digits) <= scale {
		digits = strings.Repeat("0", scale-len(digits)+1) + digits
	}
	return sign + digits[:len(digits)-scale] + "." + digits[len(digits)-scale:]
}

func appendUUID(b []byte, u []byte) []byte {
	var s [36]byte
	hex.Encode(s[0:8], u[0:4])
	s[8] = '-'
	hex.Encode(s[9:13], u[4:6])
	s[13] = '-'
	hex.Encode(s[14:18], u[6:8])
	s[18] = '-'
	hex.Encode(s[19:23], u[8:10])
	s[23] = '-'
	hex.Encode(s[24:], u[10:])
	return append(b, s[:]...)
}

// appendTime formats as DuckDB casts the type to text.
func appendTime(b []byte, native string, t time.Time) []byte {
	switch {
	case native == "DATE":
		return t.AppendFormat(b, time.DateOnly)
	case strings.HasPrefix(native, "TIME WITH"):
		return t.AppendFormat(b, "15:04:05.999999-07:00")
	case strings.HasPrefix(native, "TIME"):
		if strings.HasPrefix(native, "TIMESTAMP") {
			if strings.HasSuffix(native, "TZ") || strings.HasSuffix(native, "TIME ZONE") {
				return t.AppendFormat(b, "2006-01-02 15:04:05.999999-07:00")
			}
			return t.AppendFormat(b, "2006-01-02 15:04:05.999999999")
		}
		return t.AppendFormat(b, "15:04:05.999999")
	}
	return t.AppendFormat(b, time.RFC3339Nano)
}

// intervalText writes an interval as DuckDB does, such as "1 year 2 months 3 days 04:05:06".
func intervalText(v duckdb.Interval) string {
	var parts []string
	unit := func(n int64, name string) {
		if n != 0 {
			s := strconv.FormatInt(n, 10) + " " + name
			if n != 1 && n != -1 {
				s += "s"
			}
			parts = append(parts, s)
		}
	}
	unit(int64(v.Months/12), "year")
	unit(int64(v.Months%12), "month")
	unit(int64(v.Days), "day")
	if v.Micros != 0 || len(parts) == 0 {
		us := v.Micros
		sign := ""
		if us < 0 {
			sign, us = "-", -us
		}
		s := fmt.Sprintf("%s%02d:%02d:%02d", sign, us/3_600_000_000, us/60_000_000%60, us/1_000_000%60)
		if frac := us % 1_000_000; frac != 0 {
			s += strings.TrimRight(fmt.Sprintf(".%06d", frac), "0")
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

// jsonable replaces values JSON would lose or garble inside lists, structs, and maps.
// shortcut: the driver decodes JSON columns into float64 before this sees them, so integers in
// JSON beyond 2^53 lose digits in query results; browsing casts JSON to text and is exact.
func jsonable(v any) any {
	switch v := v.(type) {
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = jsonable(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			out[k] = jsonable(e)
		}
		return out
	case duckdb.OrderedMap:
		return v // keeps its order and marshals its own values
	case duckdb.Decimal:
		return json.Number(decimalText(v))
	case *big.Int:
		return json.Number(v.String())
	case duckdb.Interval:
		return intervalText(v)
	case duckdb.Union:
		return map[string]any{v.Tag: jsonable(v.Value)}
	case time.Time:
		return v.Format(time.RFC3339Nano)
	}
	return v
}

func quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// exactTypes come back from the driver already as DuckDB's own text for them. Browsing reads
// every other type as text, so a guard comparing a column's text matches what was shown.
var exactTypes = append([]string{"BOOLEAN", "VARCHAR", "BLOB", "DATE", "UUID"}, intTypes...)

var dialect = sqlgen.Dialect{
	Quote:  quote,
	Mark:   func(i int) string { return "$" + strconv.Itoa(i) },
	Text:   func(c string) string { return "CAST(" + c + " AS VARCHAR)" },
	Like:   "ILIKE",
	Escape: ` ESCAPE '\'`,
	Column: func(f tile.Field) string {
		if slices.Contains(exactTypes, f.NativeType) || strings.HasPrefix(f.NativeType, "DECIMAL") {
			return quote(f.Name)
		}
		return "CAST(" + quote(f.Name) + " AS VARCHAR) AS " + quote(f.Name)
	},
	Begin:  "BEGIN",
	Family: sqlgen.FamilyPostgres,
}

func (c *conn) BrowseRequest(ref tile.ObjectRef, l tile.Layout, q tile.BrowseQuery) (tile.Request, error) {
	return dialect.Select(ref, l, q)
}

func (c *conn) Plan(ref tile.ObjectRef, l tile.Layout, cs tile.ChangeSet) (tile.Plan, error) {
	return dialect.Plan(ref, l, cs)
}

func (c *conn) InsertPlan(ref tile.ObjectRef, fields []string, rows [][]tile.Value) (tile.Plan, error) {
	return dialect.InsertPlan(ref, fields, rows)
}

func (c *conn) InsertScript(ref tile.ObjectRef, fields []string, rows [][]tile.Value) (string, error) {
	return dialect.InsertScript(ref, fields, rows)
}

// ExplainRequest has no JSON form here: DuckDB returns its JSON plan in the second column.
func (c *conn) ExplainRequest(stmt string, format tile.ExplainFormat) tile.Request {
	if format == tile.ExplainJSON {
		return tile.Request{}
	}
	return tile.Request{Text: "EXPLAIN " + stmt}
}
