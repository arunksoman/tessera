// Package mysql is the MySQL and MariaDB tile.
package mysql

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	driver "github.com/go-sql-driver/mysql"

	"github.com/arunksoman/tessera/internal/lang/sqlish"
	"github.com/arunksoman/tessera/internal/tiles/sqlgen"
	"github.com/arunksoman/tessera/tile"
)

const (
	batchRows  = 1000
	batchBytes = 1 << 20
	killWait   = 2 * time.Second
)

// Tile is the MySQL and MariaDB plugin.
type Tile struct{}

func (Tile) Manifest() tile.Manifest {
	return tile.Manifest{
		ID: "mysql", Name: "MySQL / MariaDB", APIVersion: tile.APIVersion,
		Style: tile.Style{Color: "ochre", Glyph: "M"},
		ConnForm: tile.Form{Fields: []tile.FormField{
			{Name: "host", Label: "Host", Type: "text", Default: "127.0.0.1"},
			{Name: "socket", Label: "Unix socket", Type: "text"},
			{Name: "port", Label: "Port", Type: "int", Default: "3306"},
			{Name: "user", Label: "User", Type: "text", Required: true},
			{Name: "password", Label: "Password", Type: "secret"},
			{Name: "database", Label: "Database", Type: "text"},
			{Name: "tlsmode", Label: "TLS mode", Type: "enum", Default: "VERIFY_IDENTITY", Options: tlsModes},
			{Name: "tlsca", Label: "CA certificate", Type: "file"},
			{Name: "tlscert", Label: "Client certificate", Type: "file"},
			{Name: "tlskey", Label: "Client key", Type: "file"},
		}},
	}
}

var tlsModes = []string{"DISABLED", "PREFERRED", "REQUIRED", "VERIFY_CA", "VERIFY_IDENTITY"}

// config builds the driver config field by field, never from a DSN string,
// so a value cannot inject another setting.
func config(values map[string]string, password string) (*driver.Config, error) {
	cfg := driver.NewConfig()
	cfg.User = values["user"]
	cfg.Passwd = password
	cfg.DBName = values["database"]
	cfg.ClientFoundRows = true // otherwise an update to the same value reports 0 rows
	cfg.MultiStatements = false
	cfg.AllowAllFiles = false // a malicious server could otherwise read local files
	cfg.AllowCleartextPasswords = false
	cfg.AllowOldPasswords = false
	if s := values["socket"]; s != "" {
		cfg.Net, cfg.Addr = "unix", s
		return cfg, nil // TLS does not apply to a local socket
	}
	host, port := values["host"], values["port"]
	if host == "" {
		host = "127.0.0.1"
	}
	if port == "" {
		port = "3306"
	}
	cfg.Net, cfg.Addr = "tcp", net.JoinHostPort(host, port)
	t, err := tlsConfig(values, host)
	if err != nil {
		return nil, err
	}
	cfg.TLS = t
	if values["tlsmode"] == "PREFERRED" {
		cfg.AllowFallbackToPlaintext = true
	}
	return cfg, nil
}

func tlsConfig(values map[string]string, host string) (*tls.Config, error) {
	mode := values["tlsmode"]
	if mode == "" {
		mode = "VERIFY_IDENTITY"
	}
	t := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}
	switch mode {
	case "DISABLED":
		return nil, nil
	case "PREFERRED", "REQUIRED":
		t.InsecureSkipVerify = true // these modes encrypt without checking identity, by definition
	case "VERIFY_CA":
		// The stock check also compares the hostname, which this mode must not do.
		t.InsecureSkipVerify = true
		t.VerifyPeerCertificate = func(raw [][]byte, _ [][]*x509.Certificate) error { return verifyChain(raw, t.RootCAs) }
	case "VERIFY_IDENTITY":
	default:
		return nil, fmt.Errorf("mysql: unknown TLS mode %q", mode)
	}
	if ca := values["tlsca"]; ca != "" {
		pem, err := os.ReadFile(ca)
		if err != nil {
			return nil, err
		}
		t.RootCAs = x509.NewCertPool()
		if !t.RootCAs.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("mysql: no certificates in %s", ca)
		}
	}
	if crt, key := values["tlscert"], values["tlskey"]; crt != "" {
		pair, err := tls.LoadX509KeyPair(crt, key)
		if err != nil {
			return nil, err
		}
		t.Certificates = []tls.Certificate{pair}
	}
	return t, nil
}

func verifyChain(raw [][]byte, roots *x509.CertPool) error {
	opts := x509.VerifyOptions{Roots: roots, Intermediates: x509.NewCertPool()}
	var leaf *x509.Certificate
	for i, der := range raw {
		c, err := x509.ParseCertificate(der)
		if err != nil {
			return err
		}
		if i == 0 {
			leaf = c
		} else {
			opts.Intermediates.AddCert(c)
		}
	}
	if leaf == nil {
		return errors.New("mysql: server sent no certificate")
	}
	_, err := leaf.Verify(opts)
	return err
}

func (Tile) Open(ctx context.Context, cfg tile.ConnConfig, env tile.Env) (tile.Conn, error) {
	password := ""
	if env.Secret != nil {
		pw, err := env.Secret(ctx, "password")
		if err != nil {
			return nil, err
		}
		password = string(pw)
	}
	dc, err := config(cfg.Values, password)
	if err != nil {
		return nil, err
	}
	if env.Dial != nil {
		dc.DialFunc = env.Dial
	}
	connector, err := driver.NewConnector(dc)
	if err != nil {
		return nil, err
	}
	c := &conn{db: sql.OpenDB(connector), schema: cfg.Values["database"]}
	if err := c.Ping(ctx); err != nil {
		c.db.Close()
		return nil, err
	}
	return c, nil
}

type conn struct {
	db     *sql.DB
	schema string
}

func (c *conn) Capabilities() tile.Capabilities {
	on := tile.CapInfo{Supported: true}
	return tile.Capabilities{
		tile.CapTxMulti:  on,
		tile.CapTxDDL:    {Note: "MySQL commits implicitly on DDL"},
		tile.CapCancel:   on,
		tile.CapStream:   on,
		tile.CapReadOnly: on,
	}
}

func (c *conn) Language() tile.Language { return sqlish.MySQL.Language() }

func (c *conn) Tree() tile.ObjectTree { return tree{c} }

func (c *conn) Ping(ctx context.Context) error { return c.db.PingContext(ctx) }

func (c *conn) Close() error { return c.db.Close() }

func (c *conn) Session(ctx context.Context, opt tile.SessionOptions) (tile.Session, error) {
	sc, err := c.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	s := &session{c: c, sc: sc}
	if err := sc.QueryRowContext(ctx, `SELECT CONNECTION_ID()`).Scan(&s.id); err != nil {
		sc.Close()
		return nil, err
	}
	if opt.ReadOnly {
		if _, err := sc.ExecContext(ctx, `SET SESSION TRANSACTION READ ONLY`); err != nil {
			sc.Close()
			return nil, err
		}
		s.readOnly = true
	}
	return s, nil
}

type session struct {
	c        *conn
	sc       *sql.Conn
	id       int64
	readOnly bool
}

// Close resets the session, so its state cannot leak to the next user of the connection.
func (s *session) Close() error {
	if s.sc == nil {
		return nil
	}
	sc := s.sc
	s.sc = nil
	ctx, cancel := context.WithTimeout(context.Background(), killWait)
	defer cancel()
	// ResetSession on next use clears variables and temp tables, not the access mode set above,
	// so restore that here and discard the connection if it cannot be restored.
	if s.readOnly {
		if _, err := sc.ExecContext(ctx, `SET SESSION TRANSACTION READ WRITE`); err != nil {
			sc.Raw(func(any) error { return driver.ErrInvalidConn })
		}
	}
	if _, err := sc.ExecContext(ctx, `ROLLBACK`); err != nil {
		sc.Raw(func(any) error { return driver.ErrInvalidConn })
	}
	return sc.Close()
}

// kill stops the statement on a second connection. Cancelling a driver context only closes
// the socket, and the server keeps working until it next writes to it.
func (s *session) kill() {
	ctx, cancel := context.WithTimeout(context.Background(), killWait)
	defer cancel()
	// shortcut: a kill can land on a following statement if the query ends at that instant.
	// Sessions run one statement at a time and streams kill only while unfinished, so the window is small.
	s.c.db.ExecContext(ctx, `KILL QUERY `+strconv.FormatInt(s.id, 10))
}

func (s *session) Execute(ctx context.Context, req tile.Request) (tile.ResultStream, error) {
	args := make([]any, len(req.Args))
	for i, a := range req.Args {
		if a.Kind != tile.KindNull {
			args[i] = a.Raw
		}
	}
	st := &stream{s: s, ctx: ctx}
	// The driver gets a background context on purpose: its own cancel would drop the connection.
	st.stopWatch = context.AfterFunc(ctx, func() {
		if !st.finished.Load() {
			s.kill()
		}
	})
	rows, err := s.sc.QueryContext(context.WithoutCancel(ctx), req.Text, args...)
	if err != nil {
		st.stopWatch()
		return nil, st.mapErr(err)
	}
	st.rows = rows
	return st, nil
}

type stream struct {
	s         *session
	ctx       context.Context
	rows      *sql.Rows
	stopWatch func() bool
	fields    []tile.Field
	dest      []any
	raw       []sql.RawBytes
	started   bool
	pending   bool
	finished  atomic.Bool
	arena     []byte
	cells     []tile.Value
}

func (st *stream) mapErr(err error) error {
	if cause := st.ctx.Err(); cause != nil {
		return fmt.Errorf("%w: %v", cause, err)
	}
	return err
}

func (st *stream) Next(context.Context) (tile.Frame, error) {
	if st.finished.Load() {
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
			st.setFields(types)
			return tile.Frame{Schema: &tile.ResultSchema{Shape: tile.ShapeTabular, Fields: st.fields}}, nil
		}
	}
	if st.pending {
		return st.batch()
	}
	return st.done()
}

func (st *stream) setFields(types []*sql.ColumnType) {
	st.fields = make([]tile.Field, len(types))
	st.raw = make([]sql.RawBytes, len(types))
	st.dest = make([]any, len(types))
	for i, t := range types {
		st.fields[i] = tile.Field{Name: t.Name(), NativeType: t.DatabaseTypeName(), Kind: kindOf(t.DatabaseTypeName())}
		st.dest[i] = &st.raw[i]
	}
}

func (st *stream) batch() (tile.Frame, error) {
	st.arena, st.cells = st.arena[:0], st.cells[:0]
	rows := 0
	for st.pending && rows < batchRows && len(st.arena) < batchBytes {
		if err := st.rows.Scan(st.dest...); err != nil {
			return tile.Frame{}, st.mapErr(err)
		}
		for i, v := range st.raw {
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
		st.pending = st.rows.Next()
	}
	return tile.Frame{Batch: &tile.Batch{Rows: rows, Cells: st.cells}}, nil
}

func (st *stream) done() (tile.Frame, error) {
	st.finished.Store(true)
	st.stopWatch()
	err := st.rows.Err()
	closeErr := st.rows.Close()
	// SELECT SLEEP returns normally when killed on MySQL, so check the context too.
	if err == nil && st.ctx.Err() != nil {
		err = st.ctx.Err()
	}
	if err != nil {
		return tile.Frame{}, st.mapErr(err)
	}
	if closeErr != nil {
		return tile.Frame{}, st.mapErr(closeErr)
	}
	sum := &tile.ResultSummary{}
	// database/sql hides affected rows behind Exec, so ask the server on the same connection.
	if st.fields == nil {
		var n int64
		if err := st.s.sc.QueryRowContext(context.WithoutCancel(st.ctx), `SELECT ROW_COUNT()`).Scan(&n); err == nil && n >= 0 {
			sum.HasAffected, sum.RowsAffected = true, n
		}
	}
	return tile.Frame{Done: sum}, nil
}

// Close kills the statement first, because closing driver rows reads every remaining row.
func (st *stream) Close() error {
	if st.finished.Swap(true) {
		return nil
	}
	st.stopWatch()
	st.s.kill()
	err := st.rows.Close()
	var my *driver.MySQLError
	if errors.As(err, &my) && my.Number == 1317 { // query interrupted, which we asked for
		return nil
	}
	return err
}

func kindOf(dbType string) tile.ValueKind {
	t := strings.TrimPrefix(dbType, "UNSIGNED ")
	switch t {
	case "TINYINT", "SMALLINT", "MEDIUMINT", "INT", "BIGINT", "YEAR":
		return tile.KindInt
	case "DECIMAL":
		return tile.KindDecimal
	case "FLOAT", "DOUBLE":
		return tile.KindFloat
	case "VARCHAR", "CHAR", "TEXT", "TINYTEXT", "MEDIUMTEXT", "LONGTEXT", "ENUM", "SET":
		return tile.KindText
	case "BLOB", "TINYBLOB", "MEDIUMBLOB", "LONGBLOB", "BINARY", "VARBINARY", "BIT", "GEOMETRY":
		return tile.KindBytes
	case "DATE", "TIME", "DATETIME", "TIMESTAMP":
		return tile.KindTime
	case "JSON":
		return tile.KindJSON
	}
	return tile.KindOther
}

type tree struct{ c *conn }

const (
	schemasSQL = `SELECT SCHEMA_NAME FROM information_schema.SCHEMATA
		WHERE SCHEMA_NAME NOT IN ('information_schema', 'mysql', 'performance_schema', 'sys') AND SCHEMA_NAME > ?
		ORDER BY SCHEMA_NAME LIMIT ?`
	tablesSQL = `SELECT TABLE_NAME, CASE WHEN TABLE_TYPE = 'VIEW' THEN 'view' ELSE 'table' END
		FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME > ?
		ORDER BY TABLE_NAME LIMIT ?`
	columnsSQL = `SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT
		FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
		ORDER BY ORDINAL_POSITION`
	indexesSQL = `SELECT INDEX_NAME, GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX) AS COLUMNS, MIN(NON_UNIQUE) AS NON_UNIQUE
		FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
		GROUP BY INDEX_NAME ORDER BY INDEX_NAME`
)

func (t tree) Children(ctx context.Context, parent tile.ObjectRef, p tile.Page) ([]tile.Node, string, error) {
	limit := p.Limit
	if limit <= 0 {
		limit = 200
	}
	var rows *sql.Rows
	var err error
	switch len(parent.Path) {
	case 0:
		rows, err = t.c.db.QueryContext(ctx, schemasSQL, p.Cursor, limit+1)
	case 1:
		rows, err = t.c.db.QueryContext(ctx, tablesSQL, parent.Path[0].Name, p.Cursor, limit+1)
	default:
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var nodes []tile.Node
	for rows.Next() {
		var name, kind string
		if len(parent.Path) == 0 {
			err = rows.Scan(&name)
		} else {
			err = rows.Scan(&name, &kind)
		}
		if err != nil {
			return nil, "", err
		}
		if len(parent.Path) == 0 {
			nodes = append(nodes, tile.Node{Ref: parent.Child("schema", name), Kind: "schema", Label: name, HasChildren: true})
		} else {
			nodes = append(nodes, tile.Node{Ref: parent.Child(kind, name), Kind: kind, Label: name, Browsable: true})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(nodes) > limit {
		nodes = nodes[:limit]
		next = nodes[limit-1].Label
	}
	return nodes, next, nil
}

func (t tree) Describe(ctx context.Context, ref tile.ObjectRef) (tile.ObjectDetail, error) {
	if len(ref.Path) != 2 {
		return tile.ObjectDetail{}, fmt.Errorf("mysql: cannot describe %v", ref.Path)
	}
	schema, name := ref.Path[0].Name, ref.Path[1].Name
	var d tile.ObjectDetail
	for _, part := range []struct{ title, sql string }{{"Columns", columnsSQL}, {"Indexes", indexesSQL}} {
		s, err := t.section(ctx, part.title, part.sql, schema, name)
		if err != nil {
			return d, err
		}
		if part.title == "Columns" && len(s.Rows) == 0 {
			return d, fmt.Errorf("mysql: %s.%s not found", schema, name)
		}
		d.Sections = append(d.Sections, s)
	}
	return d, nil
}

// Ping fails once the session is closed or the server has gone away.
func (s *session) Ping(ctx context.Context) error {
	if s.sc == nil {
		return errors.New("mysql: session closed")
	}
	return s.sc.PingContext(ctx)
}

var dialect = sqlgen.Dialect{
	Quote: func(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" },
	Mark:  func(int) string { return "?" },
	Text:  func(c string) string { return "CAST(" + c + " AS CHAR)" },
	Like:  "LIKE",
	Begin: "START TRANSACTION",
}

const (
	estimateSQL = `SELECT TABLE_ROWS FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`
	// Index columns, primary key first, with whether each is NOT NULL.
	keysSQL = `SELECT s.INDEX_NAME, s.COLUMN_NAME, c.IS_NULLABLE
		FROM information_schema.STATISTICS s
		JOIN information_schema.COLUMNS c
			ON c.TABLE_SCHEMA = s.TABLE_SCHEMA AND c.TABLE_NAME = s.TABLE_NAME AND c.COLUMN_NAME = s.COLUMN_NAME
		WHERE s.TABLE_SCHEMA = ? AND s.TABLE_NAME = ? AND s.NON_UNIQUE = 0 AND s.SUB_PART IS NULL
		ORDER BY s.INDEX_NAME <> 'PRIMARY', s.INDEX_NAME, s.SEQ_IN_INDEX`
)

func (c *conn) Layout(ctx context.Context, ref tile.ObjectRef) (tile.Layout, error) {
	if len(ref.Path) != 2 {
		return tile.Layout{}, fmt.Errorf("mysql: cannot browse %v", ref.Path)
	}
	schema, name := ref.Path[0].Name, ref.Path[1].Name
	t := tree{c}
	cols, err := t.section(ctx, "", columnsSQL, schema, name)
	if err != nil {
		return tile.Layout{}, err
	}
	if len(cols.Rows) == 0 {
		return tile.Layout{}, fmt.Errorf("mysql: %s.%s not found", schema, name)
	}
	l := tile.Layout{Estimate: -1}
	for _, r := range cols.Rows {
		l.Fields = append(l.Fields, tile.Field{Name: string(r[0].Raw), NativeType: string(r[1].Raw)})
	}
	keys, err := t.section(ctx, "", keysSQL, schema, name)
	if err != nil {
		return tile.Layout{}, err
	}
	l.Key = firstUsableKey(keys.Rows)
	if len(l.Key) == 0 {
		l.Note = "no primary key or unique not-null key"
	}
	est, err := t.section(ctx, "", estimateSQL, schema, name)
	if err != nil {
		return tile.Layout{}, err
	}
	if len(est.Rows) == 1 {
		if n, err := strconv.ParseInt(string(est.Rows[0][0].Raw), 10, 64); err == nil {
			l.Estimate = n
		}
	}
	return l, nil
}

// firstUsableKey takes rows of (index, column, nullable) and returns the first index whose
// columns are all NOT NULL, since a nullable unique column does not identify one record.
func firstUsableKey(rows [][]tile.Value) []string {
	for i := 0; i < len(rows); {
		j, usable := i, true
		var key []string
		for ; j < len(rows) && string(rows[j][0].Raw) == string(rows[i][0].Raw); j++ {
			key = append(key, string(rows[j][1].Raw))
			usable = usable && string(rows[j][2].Raw) == "NO"
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
		return tile.Request{Text: "EXPLAIN FORMAT=JSON " + stmt}
	}
	return tile.Request{Text: "EXPLAIN " + stmt}
}

// ConfigWarnings names TLS modes that can talk to the wrong server or in plaintext.
func (Tile) ConfigWarnings(cfg tile.ConnConfig) []string {
	if cfg.Values["socket"] != "" {
		return nil
	}
	switch cfg.Values["tlsmode"] {
	case "DISABLED", "PREFERRED":
		return []string{"TLS mode " + cfg.Values["tlsmode"] + " can send data and passwords in plaintext"}
	case "REQUIRED":
		return []string{"TLS mode REQUIRED encrypts but does not verify the server's identity"}
	}
	return nil
}

func (t tree) section(ctx context.Context, title, text string, args ...any) (tile.DetailSection, error) {
	s := tile.DetailSection{Title: title}
	rows, err := t.c.db.QueryContext(ctx, text, args...)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return s, err
	}
	for _, c := range cols {
		s.Fields = append(s.Fields, tile.Field{Name: c, Kind: tile.KindText})
	}
	for rows.Next() {
		raw := make([][]byte, len(cols))
		dest := make([]any, len(cols))
		for i := range raw {
			dest[i] = &raw[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return s, err
		}
		row := make([]tile.Value, len(cols))
		for i, v := range raw {
			row[i] = tile.Value{Kind: tile.KindNull}
			if v != nil {
				row[i] = tile.Value{Kind: tile.KindText, Raw: v}
			}
		}
		s.Rows = append(s.Rows, row)
	}
	return s, rows.Err()
}
