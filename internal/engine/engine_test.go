package engine

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/arunksoman/tessera/internal/lang/sqlish"
	"github.com/arunksoman/tessera/internal/secrets"
	"github.com/arunksoman/tessera/internal/store"
	"github.com/arunksoman/tessera/tile"
)

// fakeTile records what reaches the "database".
type fakeTile struct {
	executed     []string
	readOnly     []bool
	secret       string
	dialled      bool
	failOn       string
	closedStream int
}

func (f *fakeTile) Manifest() tile.Manifest { return tile.Manifest{ID: "fake", Name: "Fake"} }

func (f *fakeTile) Open(ctx context.Context, _ tile.ConnConfig, env tile.Env) (tile.Conn, error) {
	pw, err := env.Secret(ctx, "password")
	if err != nil {
		return nil, err
	}
	f.secret = string(pw)
	return fakeConn{f}, nil
}

type fakeConn struct{ f *fakeTile }

func (fakeConn) Capabilities() tile.Capabilities { return tile.Capabilities{} }
func (fakeConn) Ping(context.Context) error      { return nil }
func (fakeConn) Close() error                    { return nil }
func (fakeConn) Tree() tile.ObjectTree           { return nil }
func (fakeConn) Language() tile.Language         { return sqlish.Postgres.Language() }
func (c fakeConn) Session(_ context.Context, opt tile.SessionOptions) (tile.Session, error) {
	c.f.readOnly = append(c.f.readOnly, opt.ReadOnly)
	return fakeSession{c.f}, nil
}

type fakeSession struct{ f *fakeTile }

func (fakeSession) Close() error { return nil }
func (s fakeSession) Execute(_ context.Context, req tile.Request) (tile.ResultStream, error) {
	s.f.executed = append(s.f.executed, req.Text)
	if s.f.failOn != "" && strings.Contains(req.Text, s.f.failOn) {
		return nil, errors.New("boom")
	}
	return &fakeStream{f: s.f}, nil
}

type fakeStream struct {
	f *fakeTile
	i int
}

func (s *fakeStream) Close() error { s.f.closedStream++; return nil }
func (s *fakeStream) Next(context.Context) (tile.Frame, error) {
	s.i++
	switch s.i {
	case 1:
		return tile.Frame{Schema: &tile.ResultSchema{Fields: []tile.Field{{Name: "n"}}}}, nil
	case 2:
		return tile.Frame{Batch: &tile.Batch{Rows: 2, Cells: []tile.Value{tile.Text("1"), tile.Text("2")}}}, nil
	case 3:
		return tile.Frame{Done: &tile.ResultSummary{}}, nil
	}
	return tile.Frame{}, io.EOF
}

type fixture struct {
	e    *Engine
	f    *fakeTile
	s    *store.Store
	conn *Conn
}

func setup(t *testing.T, p store.Profile) fixture {
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
	p.Tile = "fake"
	if p.Name == "" {
		p.Name = "dev"
	}
	if err := st.SaveProfile(p); err != nil {
		t.Fatal(err)
	}
	if err := vault.Set(SecretName(p.Name, "password"), []byte("hunter2")); err != nil {
		t.Fatal(err)
	}
	f := &fakeTile{}
	e := New(st, vault, "tester", f)
	c, err := e.Open(context.Background(), p.Name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return fixture{e, f, st, c}
}

func run(fx fixture, text string, confirm Confirm) error {
	return fx.conn.Run(context.Background(), text, RunOptions{Confirm: confirm}, func(int, tile.Frame) error { return nil })
}

func always(bool2 bool) Confirm { return func(Prompt) bool { return bool2 } }

func TestSecretsReachTheTile(t *testing.T) {
	fx := setup(t, store.Profile{})
	if fx.f.secret != "hunter2" {
		t.Fatalf("secret = %q", fx.f.secret)
	}
}

func TestGate(t *testing.T) {
	cases := []struct {
		name     string
		profile  store.Profile
		text     string
		confirm  Confirm
		wantErr  error
		executed int
	}{
		{"read runs", store.Profile{}, "SELECT 1", nil, nil, 1},
		{"write runs in development", store.Profile{}, "UPDATE t SET a=1 WHERE b=2", nil, nil, 1},
		{"drop needs confirm", store.Profile{}, "DROP TABLE t", nil, ErrNeedsConfirm, 0},
		{"drop declined", store.Profile{}, "DROP TABLE t", always(false), ErrDeclined, 0},
		{"drop confirmed", store.Profile{}, "DROP TABLE t", always(true), nil, 1},
		{"unbounded delete needs confirm", store.Profile{}, "DELETE FROM t", nil, ErrNeedsConfirm, 0},
		{"production write needs confirm", store.Profile{Env: "production"}, "UPDATE t SET a=1 WHERE b=2", nil, ErrNeedsConfirm, 0},
		{"production read runs", store.Profile{Env: "production"}, "SELECT 1", nil, nil, 1},
		{"read-only refuses write", store.Profile{ReadOnly: true}, "UPDATE t SET a=1 WHERE b=2", always(true), ErrReadOnly, 0},
		{"read-only refuses drop even confirmed", store.Profile{ReadOnly: true}, "DROP TABLE t", always(true), ErrReadOnly, 0},
		{"read-only allows select", store.Profile{ReadOnly: true}, "SELECT 1", nil, nil, 1},
		{"script is gated before anything runs", store.Profile{}, "SELECT 1; INSERT INTO t VALUES (1); DROP TABLE t", nil, ErrNeedsConfirm, 0},
		{"read-only script is gated first", store.Profile{ReadOnly: true}, "SELECT 1; DELETE FROM t WHERE a=1", nil, ErrReadOnly, 0},
		{"each statement runs alone", store.Profile{}, "SELECT 1; SELECT 2", nil, nil, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fx := setup(t, c.profile)
			err := run(fx, c.text, c.confirm)
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("err = %v, want %v", err, c.wantErr)
			}
			if len(fx.f.executed) != c.executed {
				t.Fatalf("executed %q, want %d statements", fx.f.executed, c.executed)
			}
		})
	}
}

func TestPromptExplainsTheRisk(t *testing.T) {
	fx := setup(t, store.Profile{Env: "production"})
	var got []Prompt
	err := run(fx, "SELECT 1; DROP TABLE t", func(p Prompt) bool { got = append(got, p); return true })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Statement != "DROP TABLE t" || got[0].Env != "production" || got[0].Class.Risk != tile.RiskDestructive {
		t.Fatalf("prompts = %+v", got)
	}
}

func TestReadOnlyProfileUsesReadOnlySession(t *testing.T) {
	fx := setup(t, store.Profile{ReadOnly: true})
	if err := run(fx, "SELECT 1", nil); err != nil {
		t.Fatal(err)
	}
	if len(fx.f.readOnly) != 1 || !fx.f.readOnly[0] {
		t.Fatalf("sessions = %v", fx.f.readOnly)
	}
}

func TestHistoryIsRedacted(t *testing.T) {
	fx := setup(t, store.Profile{})
	if err := run(fx, "CREATE ROLE a PASSWORD 'hunter2'", nil); err != nil {
		t.Fatal(err)
	}
	h, err := fx.s.History("dev", 10)
	if err != nil || len(h) != 1 {
		t.Fatalf("%+v %v", h, err)
	}
	if strings.Contains(h[0].Statement, "hunter2") {
		t.Fatalf("secret in history: %s", h[0].Statement)
	}
}

func TestAuditCoversWritesOnly(t *testing.T) {
	fx := setup(t, store.Profile{})
	if err := run(fx, "SELECT 1; UPDATE t SET a=1 WHERE b=2", nil); err != nil {
		t.Fatal(err)
	}
	if n, err := fx.s.VerifyAudit(); err != nil || n != 1 {
		t.Fatalf("audit entries = %d, %v", n, err)
	}
}

func TestFailureIsRecordedAndStopsTheScript(t *testing.T) {
	fx := setup(t, store.Profile{})
	fx.f.failOn = "UPDATE"
	err := run(fx, "SELECT 1; UPDATE t SET a=1 WHERE b=2; SELECT 3", nil)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	if len(fx.f.executed) != 2 {
		t.Fatalf("executed = %q", fx.f.executed)
	}
	h, _ := fx.s.History("dev", 10)
	if len(h) != 2 || h[0].Error == "" || h[1].Error != "" {
		t.Fatalf("history = %+v", h)
	}
	if n, _ := fx.s.VerifyAudit(); n != 1 {
		t.Fatalf("a failed write must still be audited: %d", n)
	}
}

func TestSinkErrorStopsAndClosesStream(t *testing.T) {
	fx := setup(t, store.Profile{})
	stop := errors.New("stop")
	err := fx.conn.Run(context.Background(), "SELECT 1", RunOptions{}, func(int, tile.Frame) error { return stop })
	if !errors.Is(err, stop) {
		t.Fatalf("err = %v", err)
	}
	if fx.f.closedStream != 1 {
		t.Fatalf("stream closed %d times", fx.f.closedStream)
	}
}

func TestUnknownProfileAndTile(t *testing.T) {
	fx := setup(t, store.Profile{})
	if _, err := fx.e.Open(context.Background(), "nope"); err == nil {
		t.Fatal("want an error for an unknown profile")
	}
	if err := fx.s.SaveProfile(store.Profile{Name: "x", Tile: "mystery"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.e.Open(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "mystery") {
		t.Fatalf("err = %v", err)
	}
}
