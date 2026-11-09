package server

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/arunksoman/tessera/internal/engine"
	"github.com/arunksoman/tessera/internal/lang/sqlish"
	"github.com/arunksoman/tessera/internal/secrets"
	"github.com/arunksoman/tessera/internal/store"
	"github.com/arunksoman/tessera/internal/tiles/sqlgen"
	"github.com/arunksoman/tessera/tile"
)

// fakeDB is an in-memory table (id, name) behind the real engine.
type fakeDB struct {
	mu       sync.Mutex
	rows     [][]string
	executed []string
	password string
	conflict bool
	stopped  bool
	failOpen bool
}

func newFakeDB(n int) *fakeDB {
	db := &fakeDB{}
	for i := 1; i <= n; i++ {
		db.rows = append(db.rows, []string{strconv.Itoa(i), "name" + strconv.Itoa(i)})
	}
	return db
}

func (db *fakeDB) ran(substr string) bool {
	db.mu.Lock()
	defer db.mu.Unlock()
	for _, s := range db.executed {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

type fakeTile struct{ db *fakeDB }

func (f fakeTile) Manifest() tile.Manifest {
	return tile.Manifest{ID: "fake", Name: "Fake", Style: tile.Style{Color: "lapis", Glyph: "F"}, ConnForm: tile.Form{Fields: []tile.FormField{
		{Name: "host", Label: "Host", Type: "text", Required: true, Default: "localhost"},
		{Name: "port", Label: "Port", Type: "int", Default: "1"},
		{Name: "password", Label: "Password", Type: "secret"},
		{Name: "mode", Label: "Mode", Type: "enum", Options: []string{"safe", "weak"}},
		{Name: "ca", Label: "CA certificate", Type: "file"},
	}}}
}

func (f fakeTile) ConfigWarnings(cfg tile.ConnConfig) []string {
	if cfg.Values["mode"] == "weak" {
		return []string{"weak mode"}
	}
	return nil
}

func (f fakeTile) Open(ctx context.Context, _ tile.ConnConfig, env tile.Env) (tile.Conn, error) {
	pw, err := env.Secret(ctx, "password")
	if err != nil {
		return nil, err
	}
	f.db.mu.Lock()
	defer f.db.mu.Unlock()
	f.db.password = string(pw)
	if f.db.failOpen {
		return nil, context.DeadlineExceeded
	}
	return fakeConn{f.db}, nil
}

type fakeConn struct{ db *fakeDB }

var fakeDialect = sqlgen.Dialect{
	Quote: func(s string) string { return `"` + s + `"` },
	Mark:  func(i int) string { return "$" + strconv.Itoa(i) },
	Text:  func(c string) string { return c },
	Like:  "LIKE",
	Begin: "BEGIN",
}

func (fakeConn) Capabilities() tile.Capabilities {
	return tile.Capabilities{tile.CapTxDDL: {Supported: true}}
}
func (fakeConn) Ping(context.Context) error { return nil }
func (fakeConn) Close() error               { return nil }
func (fakeConn) Tree() tile.ObjectTree      { return fakeTree{} }
func (fakeConn) Language() tile.Language    { return sqlish.Postgres.Language() }
func (c fakeConn) Session(context.Context, tile.SessionOptions) (tile.Session, error) {
	return fakeSession{c.db}, nil
}

func (fakeConn) Layout(_ context.Context, ref tile.ObjectRef) (tile.Layout, error) {
	l := tile.Layout{Fields: []tile.Field{{Name: "id", Kind: tile.KindInt}, {Name: "name", Kind: tile.KindText}}, Estimate: 1000}
	if ref.Path[len(ref.Path)-1].Name == "items" {
		l.Key = []string{"id"}
	} else {
		l.Note = "no primary key"
	}
	return l, nil
}

func (fakeConn) BrowseRequest(ref tile.ObjectRef, l tile.Layout, q tile.BrowseQuery) (tile.Request, error) {
	return fakeDialect.Select(ref, l, q)
}

func (fakeConn) Plan(ref tile.ObjectRef, l tile.Layout, cs tile.ChangeSet) (tile.Plan, error) {
	return fakeDialect.Plan(ref, l, cs)
}

type fakeTree struct{}

func (fakeTree) Children(_ context.Context, parent tile.ObjectRef, p tile.Page) ([]tile.Node, string, error) {
	if len(parent.Path) == 0 {
		return []tile.Node{{Ref: parent.Child("schema", "app"), Kind: "schema", Label: "app", HasChildren: true}}, "", nil
	}
	if p.Cursor == "" {
		return []tile.Node{{Ref: parent.Child("table", "items"), Kind: "table", Label: "items", Browsable: true}}, "page2", nil
	}
	return []tile.Node{{Ref: parent.Child("view", "report"), Kind: "view", Label: "report", Browsable: true}}, "", nil
}

func (fakeTree) Describe(context.Context, tile.ObjectRef) (tile.ObjectDetail, error) {
	return tile.ObjectDetail{}, nil
}

type fakeSession struct{ db *fakeDB }

func (fakeSession) Close() error { return nil }

func (s fakeSession) Execute(ctx context.Context, req tile.Request) (tile.ResultStream, error) {
	s.db.mu.Lock()
	defer s.db.mu.Unlock()
	s.db.executed = append(s.db.executed, req.Text)
	st := &fakeStream{ctx: ctx, db: s.db}
	switch {
	case strings.Contains(req.Text, "SLEEP"):
		st.block = true
	case strings.HasPrefix(req.Text, "UPDATE"):
		st.write = true
		if !s.db.conflict {
			st.affected = 1
			for _, r := range s.db.rows {
				if r[0] == string(req.Args[1].Raw) {
					r[1] = string(req.Args[0].Raw)
				}
			}
		}
	case strings.HasPrefix(req.Text, "BEGIN"), strings.HasPrefix(req.Text, "COMMIT"),
		strings.HasPrefix(req.Text, "ROLLBACK"), strings.HasPrefix(req.Text, "DROP"), strings.HasPrefix(req.Text, "DELETE"):
		st.control = true
	case strings.HasPrefix(req.Text, "BYTES"):
		st.fields = []tile.Field{{Name: "b", Kind: tile.KindBytes}}
		st.cells = []tile.Value{{Kind: tile.KindBytes, Raw: []byte{0xff, 0x00}}, {Kind: tile.KindNull}}
		st.width = 1
	default:
		st.fields = []tile.Field{{Name: "id", Kind: tile.KindInt}, {Name: "name", Kind: tile.KindText}}
		st.width = 2
		start := 0
		if strings.Contains(req.Text, ">") && len(req.Args) > 0 {
			start, _ = strconv.Atoi(string(req.Args[len(req.Args)-1].Raw))
		}
		limit := len(s.db.rows)
		if i := strings.Index(req.Text, "LIMIT "); i >= 0 {
			limit, _ = strconv.Atoi(strings.Fields(req.Text[i+6:])[0])
		}
		for i := start; i < len(s.db.rows) && i < start+limit; i++ {
			st.cells = append(st.cells, tile.Value{Kind: tile.KindInt, Raw: []byte(s.db.rows[i][0])}, tile.Text(s.db.rows[i][1]))
		}
	}
	return st, nil
}

type fakeStream struct {
	ctx      context.Context
	db       *fakeDB
	fields   []tile.Field
	cells    []tile.Value
	width    int
	block    bool
	write    bool
	control  bool
	affected int64
	step     int
	sent     int
}

func (s *fakeStream) Close() error { return nil }

// batchRows keeps batches small, so caps and streaming are exercised.
const batchRows = 100

func (s *fakeStream) Next(context.Context) (tile.Frame, error) {
	if s.block {
		<-s.ctx.Done()
		s.db.mu.Lock()
		s.db.stopped = true
		s.db.mu.Unlock()
		return tile.Frame{}, s.ctx.Err()
	}
	s.step++
	if s.write || s.control {
		if s.step > 1 {
			return tile.Frame{}, io.EOF
		}
		return tile.Frame{Done: &tile.ResultSummary{HasAffected: s.write, RowsAffected: s.affected}}, nil
	}
	if s.step == 1 {
		return tile.Frame{Schema: &tile.ResultSchema{Fields: s.fields}}, nil
	}
	total := len(s.cells) / s.width
	if s.sent < total {
		n := min(batchRows, total-s.sent)
		b := &tile.Batch{Rows: n, Cells: s.cells[s.sent*s.width : (s.sent+n)*s.width]}
		s.sent += n
		return tile.Frame{Batch: b}, nil
	}
	if s.sent == total {
		s.sent++
		return tile.Frame{Done: &tile.ResultSummary{}}, nil
	}
	return tile.Frame{}, io.EOF
}

type fixture struct {
	t       *testing.T
	db      *fakeDB
	e       *engine.Engine
	st      *store.Store
	s       *Server
	cookies []*http.Cookie
}

func newFixture(t *testing.T, profiles ...store.Profile) *fixture {
	return newFixtureAt(t, Config{}, profiles...)
}

func newFixtureAt(t *testing.T, cfg Config, profiles ...store.Profile) *fixture {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	vault, err := secrets.OpenVault(dir, make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range profiles {
		if p.Tile == "" {
			p.Tile = "fake"
		}
		if err := st.SaveProfile(p); err != nil {
			t.Fatal(err)
		}
	}
	db := newFakeDB(250)
	e := engine.New(st, vault, "tester", fakeTile{db})
	cfg.Engine = e
	s := newServer(t, cfg)
	t.Cleanup(s.Close)
	return &fixture{t: t, db: db, e: e, st: st, s: s}
}
