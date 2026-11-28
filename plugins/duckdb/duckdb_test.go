package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/arunksoman/tessera/internal/lang/sqlish"
	"github.com/arunksoman/tessera/tile"
	"github.com/arunksoman/tessera/tile/tiletest"
)

// DuckDB needs no server, so the conformance suite runs with the unit tests.

func TestMain(m *testing.M) {
	tiletest.ServeIfChild(Tile{})
	os.Exit(m.Run())
}

func TestConformance(t *testing.T) { tiletest.Run(t, harness(t, Tile{})) }

// TestConformanceOutOfProcess runs the same suite with the tile behind the plugin protocol, as
// tess runs it.
func TestConformanceOutOfProcess(t *testing.T) { tiletest.Run(t, harness(t, tiletest.OutOfProcess(t))) }

func harness(t *testing.T, tl tile.Tile) tiletest.Harness {
	path := fixtureDB(t, "../../test/fixtures/duckdb.sql")
	return tiletest.Harness{
		Open:   func(t *testing.T) tile.Conn { return openPath(t, tl, path) },
		Schema: tile.ObjectRef{}.Child("schema", "main"),
		ManyRowsSQL: func(n int) string {
			return `SELECT range, md5(range::VARCHAR) FROM range(` + strconv.Itoa(n) + `)`
		},
		// Runs until interrupted.
		SleepSQL: `SELECT count(*) FROM range(1000000000000)`,
		ArgMark:  "$1",
		HostileTable: func(t *testing.T, s tile.Session) string {
			name := "we\"ird`; DROP TABLE orders;--'x]"
			quoted := `"we""ird` + "`" + `; DROP TABLE orders;--'x]"`
			exec(t, s, `CREATE TABLE `+quoted+` (id int)`)
			t.Cleanup(func() { exec(t, s, `DROP TABLE IF EXISTS `+quoted) })
			return name
		},
	}
}

// fixtureDB creates a database in a temporary directory from the scripts given.
func fixtureDB(t *testing.T, scripts ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.duckdb")
	c, err := Tile{}.Open(context.Background(), tile.ConnConfig{Values: map[string]string{"path": path, "create": "true"}}, tile.Env{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s, err := c.Session(context.Background(), tile.SessionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, script := range scripts {
		b, err := os.ReadFile(script)
		if err != nil {
			t.Fatal(err)
		}
		text := string(b)
		for _, sp := range sqlish.DuckDB.Split(text) {
			exec(t, s, text[sp.Start:sp.End])
		}
	}
	return path
}

func open(t *testing.T, tl tile.Tile, values map[string]string) (tile.Conn, error) {
	t.Helper()
	c, err := tl.Open(context.Background(), tile.ConnConfig{Values: values}, tile.Env{})
	if err == nil {
		t.Cleanup(func() { c.Close() })
	}
	return c, err
}

func openPath(t *testing.T, tl tile.Tile, path string) tile.Conn {
	t.Helper()
	c, err := open(t, tl, map[string]string{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func newSession(t *testing.T, c tile.Conn) tile.Session {
	t.Helper()
	s, err := c.Session(context.Background(), tile.SessionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

type result struct {
	fields []tile.Field
	rows   [][]tile.Value
	done   *tile.ResultSummary
}

func run(s tile.Session, text string, args ...tile.Value) (result, error) {
	var out result
	st, err := s.Execute(context.Background(), tile.Request{Text: text, Args: args})
	if err != nil {
		return out, err
	}
	defer st.Close()
	for {
		f, err := st.Next(context.Background())
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		switch {
		case f.Schema != nil:
			out.fields = f.Schema.Fields
		case f.Batch != nil:
			w := len(out.fields)
			for r := range f.Batch.Rows {
				row := make([]tile.Value, w)
				for c := range w {
					v := f.Batch.Cells[r*w+c]
					row[c] = tile.Value{Kind: v.Kind, Raw: append([]byte(nil), v.Raw...)}
				}
				out.rows = append(out.rows, row)
			}
		case f.Done != nil:
			out.done = f.Done
		}
	}
}

func exec(t *testing.T, s tile.Session, text string, args ...tile.Value) result {
	t.Helper()
	r, err := run(s, text, args...)
	if err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	return r
}

func texts(row []tile.Value) []string {
	out := make([]string, len(row))
	for i, v := range row {
		out[i] = string(v.Raw)
	}
	return out
}

func TestOpenNeedsAnExistingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.duckdb")
	if _, err := open(t, Tile{}, map[string]string{"path": missing}); err == nil {
		t.Fatal("opened a missing file")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("a failed open created the file: %v", err)
	}
	if _, err := open(t, Tile{}, map[string]string{}); err == nil {
		t.Fatal("opened without a path")
	}
}

// A path is a file name, never one with settings of its own.
func TestPathCannotAddSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.duckdb?enable_external_access=true")
	if _, err := open(t, Tile{}, map[string]string{"path": path, "create": "true"}); err == nil {
		t.Fatal("opened a path holding settings")
	}
}

// SQL reaches no other file unless the profile allows it, and extensions never load on their
// own, so a view in an untrusted file cannot read local files or send data out.
func TestExternalAccess(t *testing.T) {
	dir := t.TempDir()
	other := filepath.Join(dir, "other.csv")
	if err := os.WriteFile(other, []byte("a\n1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	db := fixtureDB(t)
	read := `SELECT * FROM read_csv('` + filepath.ToSlash(other) + `')`

	// DuckDB opens a file once per process, with one set of settings, so this closes before the
	// file is opened again with external access.
	locked := openPath(t, Tile{}, db)
	s := newSession(t, locked)
	for _, stmt := range []string{read, `COPY (SELECT 1) TO '` + filepath.ToSlash(filepath.Join(dir, "out.csv")) + `'`,
		`ATTACH '` + filepath.ToSlash(filepath.Join(dir, "x.duckdb")) + `' AS x`, `SET enable_external_access = true`} {
		if _, err := run(s, stmt); err == nil {
			t.Errorf("ran without external access: %s", stmt)
		}
	}
	for _, setting := range []string{"autoinstall_known_extensions", "autoload_known_extensions", "allow_community_extensions"} {
		if got := texts(exec(t, s, `SELECT current_setting('`+setting+`')`).rows[0])[0]; got != "false" {
			t.Errorf("%s = %s", setting, got)
		}
	}
	s.Close()
	locked.Close()

	c, err := open(t, Tile{}, map[string]string{"path": db, "external": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if r := exec(t, newSession(t, c), read); len(r.rows) != 1 {
		t.Fatalf("external read returned %d rows", len(r.rows))
	}
}

func TestValues(t *testing.T) {
	s := newSession(t, openPath(t, Tile{}, fixtureDB(t)))
	r := exec(t, s, `SELECT 170141183460469231731687303715884105727::HUGEINT, 18446744073709551615::UBIGINT, -7::TINYINT,
		1.50::DECIMAL(10,2), -0.05::DECIMAL(10,3), 0.1::DOUBLE, true, '\x00\xff'::BLOB, 'x',
		DATE '2020-01-02', TIME '01:02:03.5', TIMESTAMP '2020-01-02 03:04:05.123456',
		INTERVAL '14 months 3 days 4 hours', '9b4a6f1e-3c2d-4e5f-8a7b-1c2d3e4f5a6b'::UUID,
		[1, 2], {'a': 1.25::DECIMAL(5,2), 'b': [true]}, MAP {'k': 1}, NULL`)
	want := []string{"170141183460469231731687303715884105727", "18446744073709551615", "-7",
		"1.50", "-0.050", "0.1", "true", "\x00\xff", "x",
		"2020-01-02", "01:02:03.5", "2020-01-02 03:04:05.123456",
		"1 year 2 months 3 days 04:00:00", "9b4a6f1e-3c2d-4e5f-8a7b-1c2d3e4f5a6b",
		"[1,2]", `{"a":1.25,"b":[true]}`, `{"k":1}`, ""}
	if got := texts(r.rows[0]); !slices.Equal(got, want) {
		t.Fatalf("values =\n%q\nwant\n%q", got, want)
	}
	kinds := []tile.ValueKind{tile.KindInt, tile.KindInt, tile.KindInt, tile.KindDecimal, tile.KindDecimal, tile.KindFloat,
		tile.KindBool, tile.KindBytes, tile.KindText, tile.KindTime, tile.KindTime, tile.KindTime, tile.KindOther, tile.KindOther,
		tile.KindJSON, tile.KindJSON, tile.KindJSON, tile.KindNull}
	for i, v := range r.rows[0] {
		if v.Kind != kinds[i] {
			t.Errorf("column %d (%s): kind %d, want %d", i, r.fields[i].NativeType, v.Kind, kinds[i])
		}
	}
}

// Every statement that is not a query ends without a result, and writes report their count.
func TestCommands(t *testing.T) {
	s := newSession(t, openPath(t, Tile{}, fixtureDB(t)))
	for _, c := range []struct {
		stmt     string
		affected int64
		has      bool
	}{
		{`CREATE TABLE c (id INT, "Count" BIGINT)`, 0, false},
		{`INSERT INTO c VALUES (1, 5), (2, 6)`, 2, true},
		{`BEGIN`, 0, false},
		{`UPDATE c SET id = id WHERE id = 99`, 0, true},
		{`DELETE FROM c WHERE id = 2`, 1, true},
		{`COMMIT`, 0, false},
		{`CREATE TABLE c2 AS SELECT * FROM c`, 1, true},
		{`SET threads = 2`, 0, false},
	} {
		r := exec(t, s, c.stmt)
		if r.fields != nil || r.done == nil || r.done.HasAffected != c.has || r.done.RowsAffected != c.affected {
			t.Errorf("%s: fields %v, done %+v", c.stmt, r.fields, r.done)
		}
	}
	// A query whose only column is named Count still returns its rows.
	if r := exec(t, s, `SELECT "Count" FROM c`); len(r.rows) != 1 || texts(r.rows[0])[0] != "5" {
		t.Fatalf("rows = %v", r.rows)
	}
	if r := exec(t, s, `UPDATE c SET id = 3 RETURNING id`); len(r.rows) != 1 || texts(r.rows[0])[0] != "3" {
		t.Fatalf("RETURNING rows = %v", r.rows)
	}
}

// Browsing reads every type whose text the tile does not render exactly as DuckDB's text, so an
// edit's guard on it matches what was shown.
func TestBrowseGuardsMatchInexactTypes(t *testing.T) {
	c := openPath(t, Tile{}, fixtureDB(t))
	s := newSession(t, c)
	exec(t, s, `CREATE TABLE g (id INT PRIMARY KEY, ts TIMESTAMPTZ, f DOUBLE, iv INTERVAL, j JSON, l INT[], label VARCHAR)`)
	exec(t, s, `INSERT INTO g VALUES (1, '2020-01-02 03:04:05.5+05:30', 1e300, INTERVAL 90 MINUTE, '{"n": 12345678901234567890}', [1, 2], 'x')`)
	ref := tile.ObjectRef{}.Child("schema", "main").Child("table", "g")
	b := c.(tile.Browser)
	l, err := b.Layout(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	req, err := b.BrowseRequest(ref, l, tile.BrowseQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	row := exec(t, s, req.Text, req.Args...).rows[0]
	if got := string(row[4].Raw); got != `{"n": 12345678901234567890}` {
		t.Fatalf("browsed JSON = %s", got)
	}
	old := map[string]tile.Value{}
	set := map[string]tile.Value{}
	for i, f := range l.Fields[1:6] {
		old[f.Name] = row[i+1]
		set[f.Name] = row[i+1]
	}
	old["label"], set["label"] = tile.Text("x"), tile.Text("y")
	p, err := c.(tile.Editor).Plan(ref, l, tile.ChangeSet{Changes: []tile.Change{{Key: []tile.Value{row[0]}, Old: old, Set: set}}})
	if err != nil {
		t.Fatal(err)
	}
	st := p.Steps[0]
	if r := exec(t, s, st.Request.Text, st.Request.Args...); r.done == nil || r.done.RowsAffected != 1 {
		t.Fatalf("guard matched %+v\n%s", r.done, st.Preview)
	}
}

func TestLoader(t *testing.T) {
	c := openPath(t, Tile{}, fixtureDB(t))
	s := newSession(t, c)
	exec(t, s, `CREATE TABLE ld (id INT PRIMARY KEY, name VARCHAR, data BLOB)`)
	ref := tile.ObjectRef{}.Child("schema", "main").Child("table", "ld")
	fields := []string{"id", "name", "data"}
	l := c.(tile.Loader)
	p, err := l.InsertPlan(ref, fields, [][]tile.Value{{tile.Text("1"), tile.Text("a"), {Kind: tile.KindBytes, Raw: []byte{0, 0xff}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range p.Steps {
		exec(t, s, st.Request.Text, st.Request.Args...)
	}
	script, err := l.InsertScript(ref, fields, [][]tile.Value{{{Kind: tile.KindInt, Raw: []byte("2")}, tile.Text(`it's a\b`), {Kind: tile.KindBytes, Raw: []byte{1}}}})
	if err != nil {
		t.Fatal(err)
	}
	exec(t, s, strings.TrimRight(script, ";\n"))
	r := exec(t, s, `SELECT name, data FROM ld ORDER BY id`)
	if got := [][]string{texts(r.rows[0]), texts(r.rows[1])}; !slices.Equal(got[0], []string{"a", "\x00\xff"}) || !slices.Equal(got[1], []string{`it's a\b`, "\x01"}) {
		t.Fatalf("loaded %q", got)
	}
}

func TestSessionStateDoesNotLeak(t *testing.T) {
	c := openPath(t, Tile{}, fixtureDB(t))
	s, err := c.Session(context.Background(), tile.SessionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	exec(t, s, `CREATE TEMP TABLE leak (a INT)`)
	exec(t, s, `SET threads = 1`)
	s.Close()
	for range 3 {
		s := newSession(t, c)
		if _, err := run(s, `SELECT * FROM leak`); err == nil {
			t.Fatal("a temporary table outlived its session")
		}
	}
}
