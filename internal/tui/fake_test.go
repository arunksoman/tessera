package tui

import (
	"context"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/arunksoman/tessera/internal/engine"
	"github.com/arunksoman/tessera/internal/lang/sqlish"
	"github.com/arunksoman/tessera/internal/secrets"
	"github.com/arunksoman/tessera/internal/store"
	"github.com/arunksoman/tessera/internal/tiles/sqlgen"
	"github.com/arunksoman/tessera/tile"
)

// fakeDB is an in-memory table behind the real engine, so tests drive the whole front-end
// without a server. Integration tests repeat the flows against real ones.
type fakeDB struct {
	mu       sync.Mutex
	rows     [][]string // id, name
	executed []string
	opens    int
	reject   string // Open fails while the password equals this
	conflict bool
	password string
	stopped  bool
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
	f.db.opens++
	f.db.password = string(pw)
	if f.db.reject != "" && string(pw) == f.db.reject {
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
func (c fakeConn) Tree() tile.ObjectTree    { return fakeTree{} }
func (fakeConn) Language() tile.Language    { return sqlish.Postgres.Language() }
func (c fakeConn) Session(context.Context, tile.SessionOptions) (tile.Session, error) {
	return fakeSession{c.db}, nil
}

func (fakeConn) Layout(_ context.Context, ref tile.ObjectRef) (tile.Layout, error) {
	l := tile.Layout{Fields: []tile.Field{{Name: "id"}, {Name: "name"}}, Estimate: 1000}
	if ref.Path[1].Name == "items" {
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

func (fakeConn) ExplainRequest(stmt string, _ tile.ExplainFormat) tile.Request {
	return tile.Request{Text: "EXPLAIN " + stmt}
}

type fakeTree struct{}

func (fakeTree) Children(_ context.Context, parent tile.ObjectRef, _ tile.Page) ([]tile.Node, string, error) {
	if len(parent.Path) == 0 {
		return []tile.Node{{Ref: parent.Child("schema", "app"), Kind: "schema", Label: "app", HasChildren: true}}, "", nil
	}
	return []tile.Node{
		{Ref: parent.Child("table", "items"), Kind: "table", Label: "items", Browsable: true},
		{Ref: parent.Child("view", "report"), Kind: "view", Label: "report", Browsable: true},
	}, "", nil
}

func (fakeTree) Describe(context.Context, tile.ObjectRef) (tile.ObjectDetail, error) {
	return tile.ObjectDetail{Sections: []tile.DetailSection{{
		Title:  "Columns",
		Fields: []tile.Field{{Name: "column"}},
		Rows:   [][]tile.Value{{tile.Text("id")}, {tile.Text("name")}},
	}}}, nil
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
		if s.db.conflict {
			st.affected = 0
		} else {
			st.affected = 1
			for _, r := range s.db.rows {
				if r[0] == string(req.Args[1].Raw) {
					r[1] = string(req.Args[0].Raw)
				}
			}
		}
	case strings.HasPrefix(req.Text, "BEGIN"), strings.HasPrefix(req.Text, "COMMIT"), strings.HasPrefix(req.Text, "ROLLBACK"), strings.HasPrefix(req.Text, "DROP"):
		st.control = true
	default:
		st.fields = []tile.Field{{Name: "id", Kind: tile.KindInt}, {Name: "name", Kind: tile.KindText}}
		start := 0
		limit := 500
		if strings.Contains(req.Text, ">") && len(req.Args) > 0 {
			start, _ = strconv.Atoi(string(req.Args[len(req.Args)-1].Raw))
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
	block    bool
	write    bool
	control  bool
	affected int64
	step     int
}

func (s *fakeStream) Close() error { return nil }

func (s *fakeStream) Next(context.Context) (tile.Frame, error) {
	if s.block {
		<-s.ctx.Done()
		s.db.mu.Lock()
		s.db.stopped = true
		s.db.mu.Unlock()
		return tile.Frame{}, s.ctx.Err()
	}
	s.step++
	switch {
	case s.write || s.control:
		if s.step > 1 {
			return tile.Frame{}, io.EOF
		}
		return tile.Frame{Done: &tile.ResultSummary{HasAffected: s.write, RowsAffected: s.affected}}, nil
	case s.step == 1:
		return tile.Frame{Schema: &tile.ResultSchema{Fields: s.fields}}, nil
	case s.step == 2 && len(s.cells) > 0:
		return tile.Frame{Batch: &tile.Batch{Rows: len(s.cells) / 2, Cells: s.cells}}, nil
	case s.step <= 3:
		s.step = 3
		return tile.Frame{Done: &tile.ResultSummary{}}, nil
	}
	return tile.Frame{}, io.EOF
}

// app runs a real Bubble Tea program around the model, with no terminal attached.
type app struct {
	t    *testing.T
	m    *Model
	p    *tea.Program
	view atomic.Value
	st   *store.Store
	done chan struct{}
}

// probe stores each frame after each update, so a test can read it from another goroutine.
type probe struct{ a *app }

func (p probe) Init() tea.Cmd { return p.a.m.Init() }

func (p probe) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	if f, ok := msg.(func(*Model) tea.Cmd); ok {
		cmd = f(p.a.m) // lets a test act on the model inside the event loop
	} else {
		_, cmd = p.a.m.Update(msg)
	}
	p.a.view.Store(p.a.m.frame())
	return p, cmd
}

func (p probe) View() tea.View { return p.a.m.View() }

type setup struct {
	profiles []store.Profile
	start    string
	password string
	stopped  bool
	width    int
	height   int
}

func start(t *testing.T, db *fakeDB, s setup) *app {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	vault, err := secrets.OpenVault(dir, make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range s.profiles {
		if err := st.SaveProfile(p); err != nil {
			t.Fatal(err)
		}
	}
	return startWith(t, engine.New(st, vault, "tester", fakeTile{db}), st, s)
}

func startWith(t *testing.T, e *engine.Engine, st *store.Store, s setup) *app {
	t.Helper()
	if s.width == 0 {
		s.width, s.height = 120, 40
	}
	a := &app{t: t, m: New(e, s.start), st: st, done: make(chan struct{})}
	a.view.Store("")
	a.p = tea.NewProgram(probe{a}, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithWindowSize(s.width, s.height))
	go func() {
		defer close(a.done)
		a.p.Run()
	}()
	t.Cleanup(func() {
		a.p.Quit()
		<-a.done
		a.m.Close()
		st.Close()
	})
	return a
}

var sgr = regexp.MustCompile(`\x1b\[[0-9;:]*m`)

func (a *app) screen() string { return sgr.ReplaceAllString(a.view.Load().(string), "") }

// see waits until the screen holds every text.
func (a *app) see(texts ...string) {
	a.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s := a.screen()
		missing := ""
		for _, want := range texts {
			if !strings.Contains(s, want) {
				missing = want
				break
			}
		}
		if missing == "" {
			return
		}
		if time.Now().After(deadline) {
			a.t.Fatalf("screen never showed %q:\n%s", missing, s)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (a *app) gone(text string) {
	a.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for strings.Contains(a.screen(), text) {
		if time.Now().After(deadline) {
			a.t.Fatalf("screen still shows %q:\n%s", text, a.screen())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

var specialKeys = map[string]rune{
	"enter": tea.KeyEnter, "esc": tea.KeyEscape, "tab": tea.KeyTab, "up": tea.KeyUp, "down": tea.KeyDown,
	"left": tea.KeyLeft, "right": tea.KeyRight, "pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown,
	"home": tea.KeyHome, "end": tea.KeyEnd, "space": tea.KeySpace, "backspace": tea.KeyBackspace,
	"f1": tea.KeyF1, "f5": tea.KeyF5, "f6": tea.KeyF6, "f7": tea.KeyF7, "f8": tea.KeyF8, "f9": tea.KeyF9,
}

// keys sends key names like "ctrl+k", "alt+t", "enter", or a single character.
func (a *app) keys(names ...string) {
	for _, name := range names {
		var k tea.KeyPressMsg
		rest := name
		for {
			if r, ok := strings.CutPrefix(rest, "ctrl+"); ok && r != "" {
				k.Mod |= tea.ModCtrl
				rest = r
			} else if r, ok := strings.CutPrefix(rest, "alt+"); ok && r != "" {
				k.Mod |= tea.ModAlt
				rest = r
			} else if r, ok := strings.CutPrefix(rest, "shift+"); ok && r != "" {
				k.Mod |= tea.ModShift
				rest = r
			} else {
				break
			}
		}
		if code, ok := specialKeys[rest]; ok {
			k.Code = code
		} else {
			k.Code = []rune(rest)[0]
			if k.Mod == 0 {
				k.Text = rest
			}
		}
		a.p.Send(k)
	}
}

// typeText sends each character as a key press.
func (a *app) typeText(s string) {
	for _, r := range s {
		a.p.Send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

var fakeProfile = store.Profile{Name: "dev", Tile: "fake"}
