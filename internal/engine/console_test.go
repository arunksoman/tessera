package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/arunksoman/tessera/internal/store"
	"github.com/arunksoman/tessera/tile"
)

var fakeRef = tile.ObjectRef{}.Child("schema", "s").Child("table", "t")

func (fakeConn) Layout(context.Context, tile.ObjectRef) (tile.Layout, error) {
	return tile.Layout{Fields: []tile.Field{{Name: "n"}}, Key: []string{"n"}}, nil
}

func (fakeConn) BrowseRequest(_ tile.ObjectRef, _ tile.Layout, q tile.BrowseQuery) (tile.Request, error) {
	return tile.Request{Text: "SELECT page"}, nil
}

func (fakeConn) Plan(_ tile.ObjectRef, _ tile.Layout, cs tile.ChangeSet) (tile.Plan, error) {
	p := tile.Plan{Begin: tile.Request{Text: "BEGIN"}, Commit: tile.Request{Text: "COMMIT"}, Rollback: tile.Request{Text: "ROLLBACK"}}
	for range cs.Changes {
		p.Steps = append(p.Steps, tile.PlanStep{Request: tile.Request{Text: "UPDATE t SET a = 1 WHERE b = 2"}, Preview: "preview", ExpectRows: 1})
	}
	return p, nil
}

func (fakeConn) ExplainRequest(stmt string, _ tile.ExplainFormat) tile.Request {
	return tile.Request{Text: "EXPLAIN " + stmt}
}

func discard(int, tile.Frame) error { return nil }

func TestConsolePinsOneSession(t *testing.T) {
	fx := setup(t, store.Profile{})
	k := fx.conn.NewConsole()
	for range 3 {
		if err := k.Run(context.Background(), "SELECT 1", RunOptions{}, discard); err != nil {
			t.Fatal(err)
		}
	}
	if len(fx.f.readOnly) != 1 {
		t.Fatalf("opened %d sessions, want one", len(fx.f.readOnly))
	}
	if fx.f.closedSession != 0 {
		t.Fatal("session closed while the console is open")
	}
	k.Close()
	if fx.f.closedSession != 1 {
		t.Fatalf("closed %d sessions", fx.f.closedSession)
	}
}

func TestConsoleTracksTransactions(t *testing.T) {
	fx := setup(t, store.Profile{})
	k := fx.conn.NewConsole()
	defer k.Close()
	steps := []struct {
		text string
		inTx bool
	}{
		{"SELECT 1", false},
		{"BEGIN", true},
		{"UPDATE t SET a=1 WHERE b=2", true},
		{"SAVEPOINT s", true},
		{"ROLLBACK TO s", true},
		{"COMMIT", false},
		{"START TRANSACTION", true},
		{"rollback", false},
	}
	for _, s := range steps {
		if err := k.Run(context.Background(), s.text, RunOptions{}, discard); err != nil {
			t.Fatal(err)
		}
		if k.InTx() != s.inTx {
			t.Fatalf("after %q InTx = %v", s.text, k.InTx())
		}
	}
}

func TestConsoleImplicitCommitOnDDLWithoutTransactionalDDL(t *testing.T) {
	fx := setup(t, store.Profile{})
	k := fx.conn.NewConsole()
	defer k.Close()
	if err := k.Run(context.Background(), "BEGIN; CREATE TABLE x (a int)", RunOptions{}, discard); err != nil {
		t.Fatal(err)
	}
	if k.InTx() {
		t.Fatal("a server without transactional DDL commits on CREATE")
	}
}

func TestConsoleIsGated(t *testing.T) {
	fx := setup(t, store.Profile{ReadOnly: true})
	k := fx.conn.NewConsole()
	defer k.Close()
	if err := k.Run(context.Background(), "DELETE FROM t WHERE a=1", RunOptions{Confirm: always(true)}, discard); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("err = %v", err)
	}
	if len(fx.f.executed) != 0 {
		t.Fatalf("executed %q", fx.f.executed)
	}
}

func TestConsoleReopensDeadSession(t *testing.T) {
	fx := setup(t, store.Profile{})
	k := fx.conn.NewConsole()
	defer k.Close()
	run := func(text string) error { return k.Run(context.Background(), text, RunOptions{}, discard) }
	if err := run("SELECT 1"); err != nil {
		t.Fatal(err)
	}
	fx.f.pingErr = errors.New("gone")
	if err := run("SELECT 2"); err != nil {
		t.Fatalf("outside a transaction a dead session is replaced: %v", err)
	}
	if len(fx.f.readOnly) != 2 {
		t.Fatalf("sessions = %d", len(fx.f.readOnly))
	}

	fx.f.pingErr = nil
	if err := run("BEGIN"); err != nil {
		t.Fatal(err)
	}
	fx.f.pingErr = errors.New("gone")
	err := run("UPDATE t SET a=1 WHERE b=2")
	if !errors.Is(err, ErrSessionLost) {
		t.Fatalf("losing a transaction must be reported: %v", err)
	}
	if k.InTx() {
		t.Fatal("the transaction is gone")
	}
}

func TestErrStopEndsOnlyThatStatement(t *testing.T) {
	fx := setup(t, store.Profile{})
	err := fx.conn.Run(context.Background(), "SELECT 1; SELECT 2", RunOptions{}, func(_ int, f tile.Frame) error {
		if f.Batch != nil {
			return ErrStop
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(fx.f.executed) != 2 || fx.f.closedStream != 2 {
		t.Fatalf("executed = %q, closed = %d", fx.f.executed, fx.f.closedStream)
	}
	h, _ := fx.s.History("dev", 10)
	if len(h) != 2 || h[0].Error != "" {
		t.Fatalf("a deliberate stop is not an error: %+v", h)
	}
}

func TestBrowseUsesReadOnlySessionAndNoHistory(t *testing.T) {
	fx := setup(t, store.Profile{})
	l, err := fx.conn.Layout(context.Background(), fakeRef)
	if err != nil {
		t.Fatal(err)
	}
	rows := 0
	err = fx.conn.Browse(context.Background(), fakeRef, l, tile.BrowseQuery{Limit: 2}, func(f tile.Frame) error {
		if f.Batch != nil {
			rows += f.Batch.Rows
		}
		return nil
	})
	if err != nil || rows != 2 {
		t.Fatalf("rows = %d, err = %v", rows, err)
	}
	if len(fx.f.readOnly) != 1 || !fx.f.readOnly[0] || fx.f.closedSession != 1 {
		t.Fatalf("sessions = %v, closed = %d", fx.f.readOnly, fx.f.closedSession)
	}
	if h, _ := fx.s.History("dev", 10); len(h) != 0 {
		t.Fatalf("history = %+v", h)
	}
}

func change() tile.ChangeSet {
	return tile.ChangeSet{Changes: []tile.Change{{Key: []tile.Value{tile.Text("1")}}}}
}

func plan(t *testing.T, fx fixture) *Pending {
	t.Helper()
	p, err := fx.conn.PlanChanges(fakeRef, tile.Layout{Key: []string{"n"}}, change())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestApplyCommitsAndAudits(t *testing.T) {
	fx := setup(t, store.Profile{})
	one := int64(1)
	fx.f.affected = &one
	if err := fx.conn.Apply(context.Background(), plan(t, fx), RunOptions{}); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(fx.f.executed, "; ")
	if got != "BEGIN; UPDATE t SET a = 1 WHERE b = 2; COMMIT" {
		t.Fatalf("executed %s", got)
	}
	if n, err := fx.s.VerifyAudit(); err != nil || n != 1 {
		t.Fatalf("audit = %d, %v", n, err)
	}
}

func TestApplyConflictRollsBack(t *testing.T) {
	fx := setup(t, store.Profile{})
	zero := int64(0)
	fx.f.affected = &zero
	err := fx.conn.Apply(context.Background(), plan(t, fx), RunOptions{})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v", err)
	}
	if last := fx.f.executed[len(fx.f.executed)-1]; last != "ROLLBACK" {
		t.Fatalf("executed = %q", fx.f.executed)
	}
}

func TestApplyStepFailureRollsBack(t *testing.T) {
	fx := setup(t, store.Profile{})
	fx.f.failOn = "UPDATE"
	if err := fx.conn.Apply(context.Background(), plan(t, fx), RunOptions{}); err == nil {
		t.Fatal("want an error")
	}
	if last := fx.f.executed[len(fx.f.executed)-1]; last != "ROLLBACK" {
		t.Fatalf("executed = %q", fx.f.executed)
	}
}

func TestApplyRefusesAChangedPlan(t *testing.T) {
	fx := setup(t, store.Profile{})
	p := plan(t, fx)
	p.Plan.Steps[0].Request.Text = "DROP TABLE t"
	if err := fx.conn.Apply(context.Background(), p, RunOptions{}); !errors.Is(err, ErrPlanChanged) {
		t.Fatalf("err = %v", err)
	}
	if len(fx.f.executed) != 0 {
		t.Fatalf("executed = %q", fx.f.executed)
	}
}

func TestApplyPassesTheGate(t *testing.T) {
	fx := setup(t, store.Profile{ReadOnly: true})
	if err := fx.conn.Apply(context.Background(), plan(t, fx), RunOptions{Confirm: always(true)}); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("read-only: %v", err)
	}
	fx = setup(t, store.Profile{Env: EnvProduction})
	if err := fx.conn.Apply(context.Background(), plan(t, fx), RunOptions{}); !errors.Is(err, ErrNeedsConfirm) {
		t.Fatalf("production: %v", err)
	}
	if len(fx.f.executed) != 0 {
		t.Fatalf("executed = %q", fx.f.executed)
	}
}

func TestExplainUsesReadOnlySession(t *testing.T) {
	fx := setup(t, store.Profile{})
	if err := fx.conn.Explain(context.Background(), "DELETE FROM t", tile.ExplainText, func(tile.Frame) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if fx.f.executed[0] != "EXPLAIN DELETE FROM t" || !fx.f.readOnly[0] {
		t.Fatalf("executed = %q, sessions = %v", fx.f.executed, fx.f.readOnly)
	}
	if err := fx.conn.Explain(context.Background(), "SELECT 1; SELECT 2", tile.ExplainText, func(tile.Frame) error { return nil }); err == nil {
		t.Fatal("explain takes one statement")
	}
}

func TestStatementAt(t *testing.T) {
	fx := setup(t, store.Profile{})
	const text = "SELECT 1;\nSELECT ';' ;\n\n  SELECT 3"
	cases := []struct {
		offset int
		want   string
	}{
		{0, "SELECT 1"},
		{8, "SELECT 1"},
		{9, "SELECT 1"},
		{12, "SELECT ';'"},
		{22, "SELECT ';'"},
		{len(text), "SELECT 3"},
	}
	for _, c := range cases {
		if got := fx.conn.StatementAt(text, c.offset); got != c.want {
			t.Errorf("offset %d: %q, want %q", c.offset, got, c.want)
		}
	}
	if got := fx.conn.StatementAt("  ", 1); got != "" {
		t.Errorf("blank text: %q", got)
	}
}

func TestNoHistoryProfileStillAudits(t *testing.T) {
	fx := setup(t, store.Profile{NoHistory: true})
	if err := run(fx, "SELECT 1; UPDATE t SET a=1 WHERE b=2", nil); err != nil {
		t.Fatal(err)
	}
	if h, _ := fx.s.History("dev", 10); len(h) != 0 {
		t.Fatalf("history = %+v", h)
	}
	if n, _ := fx.s.VerifyAudit(); n != 1 {
		t.Fatalf("audit = %d", n)
	}
}

func TestOpenPrunesHistoryToTheRetentionSetting(t *testing.T) {
	fx := setup(t, store.Profile{})
	if err := fx.s.SaveSettings(store.Settings{HistoryKeep: 2}); err != nil {
		t.Fatal(err)
	}
	if err := run(fx, "SELECT 1; SELECT 2; SELECT 3", nil); err != nil {
		t.Fatal(err)
	}
	c, err := fx.e.Open(context.Background(), "dev")
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if h, _ := fx.s.History("dev", 10); len(h) != 2 {
		t.Fatalf("history = %d entries", len(h))
	}
}

func TestManagerReusesCapsAndClosesIdle(t *testing.T) {
	fx := setup(t, store.Profile{})
	for _, name := range []string{"a", "b"} {
		if err := fx.s.SaveProfile(store.Profile{Name: name, Tile: "fake"}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Unix(1000, 0)
	m := NewManager(fx.e, 2, time.Minute)
	m.now = func() time.Time { return now }
	ctx := context.Background()

	a1, releaseA1, err := m.Acquire(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	opens := fx.f.opens
	a2, releaseA2, err := m.Acquire(ctx, "a")
	if err != nil || a1 != a2 || fx.f.opens != opens {
		t.Fatalf("a connection must be shared: %v, opens %d", err, fx.f.opens)
	}
	_, releaseB, err := m.Acquire(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Acquire(ctx, "dev"); !errors.Is(err, ErrTooManyConns) {
		t.Fatalf("every slot is in use: %v", err)
	}

	// A released connection is evicted to make room, oldest first.
	releaseB()
	now = now.Add(time.Second)
	_, releaseDev, err := m.Acquire(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if fx.f.closedConn != 1 {
		t.Fatalf("closed = %d", fx.f.closedConn)
	}

	releaseA1()
	releaseA2()
	releaseDev()
	m.CloseIdle()
	if fx.f.closedConn != 1 {
		t.Fatal("not idle long enough")
	}
	now = now.Add(2 * time.Minute)
	m.CloseIdle()
	if fx.f.closedConn != 3 {
		t.Fatalf("closed = %d", fx.f.closedConn)
	}
	m.Close()
}

func TestWarningsComeFromTheTile(t *testing.T) {
	fx := setup(t, store.Profile{})
	got := fx.e.Warnings(store.Profile{Tile: "fake", Values: map[string]string{"weak": "1"}})
	if len(got) != 1 || got[0] != "weak" {
		t.Fatalf("warnings = %v", got)
	}
	if got := fx.e.Warnings(store.Profile{Tile: "fake"}); len(got) != 0 {
		t.Fatalf("warnings = %v", got)
	}
}

func (f *fakeTile) ConfigWarnings(cfg tile.ConnConfig) []string {
	if cfg.Values["weak"] != "" {
		return []string{"weak"}
	}
	return nil
}
