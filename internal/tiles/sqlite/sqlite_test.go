package sqlite

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"testing"

	"github.com/arunksoman/tessera/internal/lang/sqlish"
	"github.com/arunksoman/tessera/tile"
	"github.com/arunksoman/tessera/tile/tiletest"
)

// SQLite needs no server, so the conformance suite runs with the unit tests.

func TestMain(m *testing.M) {
	tiletest.ServeIfChild(Tile{})
	os.Exit(m.Run())
}

func TestConformance(t *testing.T) { tiletest.Run(t, harness(t, Tile{})) }

// TestConformanceOutOfProcess runs the same suite with the tile behind the plugin protocol.
func TestConformanceOutOfProcess(t *testing.T) { tiletest.Run(t, harness(t, tiletest.OutOfProcess(t))) }

func harness(t *testing.T, tl tile.Tile) tiletest.Harness {
	path := fixtureDB(t, "../../../test/fixtures/sqlite.sql")
	return tiletest.Harness{
		Open:   func(t *testing.T) tile.Conn { return openPath(t, tl, path) },
		Schema: tile.ObjectRef{}.Child("schema", "main"),
		ManyRowsSQL: func(n int) string {
			return `WITH RECURSIVE g(n) AS (SELECT 0 UNION ALL SELECT n + 1 FROM g LIMIT ` + strconv.Itoa(n) + `) SELECT n, hex(randomblob(16)) FROM g`
		},
		// Runs until interrupted; SQLite has no sleep function.
		SleepSQL:       `WITH RECURSIVE g(n) AS (SELECT 0 UNION ALL SELECT n + 1 FROM g WHERE n < 1e12) SELECT count(*) FROM g`,
		ArgMark:        "?",
		InexactDecimal: true,
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
	path := filepath.Join(t.TempDir(), "test.db")
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
	exec(t, s, "BEGIN")
	// Sakila inserts staff and store, which refer to each other, so keys are checked at commit.
	exec(t, s, "PRAGMA defer_foreign_keys = ON")
	for _, script := range scripts {
		b, err := os.ReadFile(script)
		if err != nil {
			t.Fatal(err)
		}
		text := string(b)
		for _, sp := range sqlish.SQLite.Split(text) {
			exec(t, s, text[sp.Start:sp.End])
		}
	}
	exec(t, s, "COMMIT")
	return path
}

func openPath(t *testing.T, tl tile.Tile, path string) tile.Conn {
	t.Helper()
	c, err := tl.Open(context.Background(), tile.ConnConfig{Values: map[string]string{"path": path}}, tile.Env{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func newSession(t *testing.T, c tile.Conn, opt tile.SessionOptions) tile.Session {
	t.Helper()
	s, err := c.Session(context.Background(), opt)
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

func TestOpenNeedsAnExistingFile(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.db")
	if _, err := (Tile{}).Open(context.Background(), tile.ConnConfig{Values: map[string]string{"path": missing}}, tile.Env{}); err == nil {
		t.Fatal("opened a missing file")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("a failed open created the file: %v", err)
	}
	if _, err := (Tile{}).Open(context.Background(), tile.ConnConfig{}, tile.Env{}); err == nil {
		t.Fatal("opened without a path")
	}
}

// A path is a file name, never a URI with parameters of its own.
func TestPathCannotAddParameters(t *testing.T) {
	name := "a#_pragma=foreign_keys(0)&mode=memory %25.db"
	if runtime.GOOS != "windows" {
		name = "a?mode=memory&_pragma=foreign_keys(0)#x %25.db"
	}
	path := filepath.Join(t.TempDir(), name)
	c, err := Tile{}.Open(context.Background(), tile.ConnConfig{Values: map[string]string{"path": path, "create": "true"}}, tile.Env{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s := newSession(t, c, tile.SessionOptions{})
	exec(t, s, `CREATE TABLE x (a)`)
	if fk := exec(t, s, `PRAGMA foreign_keys`).rows[0][0]; string(fk.Raw) != "1" {
		t.Fatalf("foreign_keys = %s", fk.Raw)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the database is not the named file: %v", err)
	}
}

func TestValues(t *testing.T) {
	c := openPath(t, Tile{}, fixtureDB(t))
	s := newSession(t, c, tile.SessionOptions{})
	exec(t, s, `CREATE TABLE v (i INTEGER, f REAL, d DECIMAL(10,2), b BLOB, ts TIMESTAMP, flag BOOLEAN, anything)`)
	exec(t, s, `INSERT INTO v VALUES (9223372036854775807, 0.1, '1.50', x'00ff', '2006-02-15 04:34:33.000', 1, 'text')`)
	r := exec(t, s, `SELECT * FROM v`)
	got := make([]string, len(r.rows[0]))
	for i, v := range r.rows[0] {
		got[i] = string(v.Raw)
	}
	want := []string{"9223372036854775807", "0.1", "1.5", "\x00\xff", "2006-02-15T04:34:33Z", "1", "text"}
	if !slices.Equal(got, want) {
		t.Fatalf("values = %q, want %q", got, want)
	}
	kinds := []tile.ValueKind{tile.KindInt, tile.KindFloat, tile.KindFloat, tile.KindBytes, tile.KindTime, tile.KindBool, tile.KindText}
	for i, v := range r.rows[0] {
		if v.Kind != kinds[i] {
			t.Errorf("%s: kind %d, want %d", r.fields[i].Name, v.Kind, kinds[i])
		}
	}

	// An expression has no declared type, so its first value sets the field's kind.
	if r := exec(t, s, `SELECT 9223372036854775807 AS big, 0.5 AS f`); r.fields[0].Kind != tile.KindInt || r.fields[1].Kind != tile.KindFloat {
		t.Fatalf("expression fields = %+v", r.fields)
	}

	// Browsing reads a timestamp as stored, so a guard on it matches.
	b := c.(tile.Browser)
	ref := tile.ObjectRef{}.Child("schema", "main").Child("table", "v")
	l, err := b.Layout(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	req, err := b.BrowseRequest(ref, l, tile.BrowseQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if ts := exec(t, s, req.Text, req.Args...).rows[0][4]; string(ts.Raw) != "2006-02-15 04:34:33.000" {
		t.Fatalf("browsed timestamp = %q", ts.Raw)
	}
}

func TestExtensionsCannotLoad(t *testing.T) {
	s := newSession(t, openPath(t, Tile{}, fixtureDB(t)), tile.SessionOptions{})
	if _, err := run(s, `SELECT load_extension('x')`); err == nil {
		t.Fatal("load_extension ran")
	}
}

func TestSessionStateDoesNotLeak(t *testing.T) {
	c := openPath(t, Tile{}, fixtureDB(t))
	s, err := c.Session(context.Background(), tile.SessionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	exec(t, s, `CREATE TEMP TABLE leak (a)`)
	exec(t, s, `PRAGMA foreign_keys = 0`)
	s.Close()
	for range 3 {
		s := newSession(t, c, tile.SessionOptions{})
		if _, err := run(s, `SELECT * FROM leak`); err == nil {
			t.Fatal("a temporary table outlived its session")
		}
		if fk := exec(t, s, `PRAGMA foreign_keys`).rows[0][0]; string(fk.Raw) != "1" {
			t.Fatalf("foreign_keys = %s", fk.Raw)
		}
	}
}
