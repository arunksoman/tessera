package engine

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/arunksoman/tessera/internal/store"
	"github.com/arunksoman/tessera/tile"
)

type fakeDesigner struct{}

func (fakeDesigner) Children(context.Context, tile.ObjectRef, tile.Page) ([]tile.Node, string, error) {
	return nil, "", nil
}
func (fakeDesigner) Describe(context.Context, tile.ObjectRef) (tile.ObjectDetail, error) {
	return tile.ObjectDetail{}, nil
}
func (fakeDesigner) DescribeStructure(context.Context, tile.ObjectRef) (tile.Structure, error) {
	return tile.Structure{Fields: []tile.FieldDef{{Name: "n", Type: "int"}}}, nil
}
func (fakeDesigner) CreateScript(context.Context, tile.ObjectRef) (string, error) {
	return "CREATE TABLE t (n int);", nil
}

// PlanStructure plans one statement per change, named by the change's Name.
func (fakeDesigner) PlanStructure(_ tile.ObjectRef, cs []tile.StructureChange) (tile.Plan, error) {
	p := tile.Plan{Begin: tile.Request{Text: "BEGIN"}, Commit: tile.Request{Text: "COMMIT"}, Rollback: tile.Request{Text: "ROLLBACK"}}
	for _, c := range cs {
		p.Steps = append(p.Steps, tile.PlanStep{Request: tile.Request{Text: c.Name}, Preview: c.Name, ExpectRows: -1})
	}
	return p, nil
}

// InsertPlan binds one value per record, so the fake session reports one record per value.
func (fakeConn) InsertPlan(_ tile.ObjectRef, fields []string, rows [][]tile.Value) (tile.Plan, error) {
	var args []tile.Value
	for _, r := range rows {
		args = append(args, r[0])
	}
	return tile.Plan{
		Begin: tile.Request{Text: "BEGIN"}, Commit: tile.Request{Text: "COMMIT"}, Rollback: tile.Request{Text: "ROLLBACK"},
		Steps: []tile.PlanStep{{Request: tile.Request{Text: "INSERT INTO t VALUES " + strconv.Itoa(len(rows)), Args: args}, ExpectRows: int64(len(rows))}},
	}, nil
}

func (fakeConn) InsertScript(tile.ObjectRef, []string, [][]tile.Value) (string, error) {
	return "", nil
}

func designSetup(t *testing.T, p store.Profile) fixture {
	fx := setup(t, p)
	fx.f.tree = fakeDesigner{}
	return fx
}

func TestStructureNeedsADesigner(t *testing.T) {
	fx := setup(t, store.Profile{})
	if _, err := fx.conn.Structure(context.Background(), fakeRef); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("structure: %v", err)
	}
	if _, err := fx.conn.PlanStructure(fakeRef, nil); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("plan: %v", err)
	}
	if _, err := fx.conn.CreateScript(context.Background(), fakeRef); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("script: %v", err)
	}
	fx = designSetup(t, store.Profile{})
	if s, err := fx.conn.Structure(context.Background(), fakeRef); err != nil || s.Fields[0].Name != "n" {
		t.Fatalf("%+v %v", s, err)
	}
	if s, err := fx.conn.CreateScript(context.Background(), fakeRef); err != nil || !strings.HasPrefix(s, "CREATE") {
		t.Fatalf("%q %v", s, err)
	}
}

// A structure change reports whatever row count the store gives, such as rows copied by an
// ALTER, so its steps are not checked against one.
func TestApplyStructureSkipsRowCounts(t *testing.T) {
	fx := designSetup(t, store.Profile{})
	five := int64(5)
	fx.f.affected = &five
	p, err := fx.conn.PlanStructure(fakeRef, []tile.StructureChange{{Op: tile.OpFieldAdd, Name: "ALTER TABLE t ADD COLUMN a int"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := fx.conn.Apply(context.Background(), p, RunOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(fx.f.executed, "; "); got != "BEGIN; ALTER TABLE t ADD COLUMN a int; COMMIT" {
		t.Fatalf("executed %s", got)
	}
}

func TestApplyStructureIsGated(t *testing.T) {
	fx := designSetup(t, store.Profile{})
	p, err := fx.conn.PlanStructure(fakeRef, []tile.StructureChange{{Op: tile.OpDrop, Name: "DROP TABLE t"}})
	if err != nil {
		t.Fatal(err)
	}
	if c := fx.conn.Classify(p.Plan.Steps[0].Request.Text); c.Risk != tile.RiskDestructive {
		t.Fatalf("classified %+v", c)
	}
	if err := fx.conn.Apply(context.Background(), p, RunOptions{}); !errors.Is(err, ErrNeedsConfirm) {
		t.Fatalf("err = %v", err)
	}
	if len(fx.f.executed) != 0 {
		t.Fatalf("executed %q", fx.f.executed)
	}
	if err := fx.conn.Apply(context.Background(), p, RunOptions{Confirm: always(true)}); err != nil {
		t.Fatal(err)
	}
}

func TestPlanStructureRefusesAnEmptyPlan(t *testing.T) {
	fx := designSetup(t, store.Profile{})
	if _, err := fx.conn.PlanStructure(fakeRef, nil); err == nil {
		t.Fatal("an empty plan was accepted")
	}
}

func records(n int) func() ([]tile.Value, error) {
	i := 0
	return func() ([]tile.Value, error) {
		if i == n {
			return nil, io.EOF
		}
		i++
		return []tile.Value{tile.Text(strconv.Itoa(i))}, nil
	}
}

func TestLoadBatchesInOneTransaction(t *testing.T) {
	fx := setup(t, store.Profile{})
	n, err := fx.conn.Load(context.Background(), fakeRef, []string{"n"}, records(1201), RunOptions{})
	if err != nil || n != 1201 {
		t.Fatalf("n = %d, %v", n, err)
	}
	want := "BEGIN; INSERT INTO t VALUES 500; INSERT INTO t VALUES 500; INSERT INTO t VALUES 201; COMMIT"
	if got := strings.Join(fx.f.executed, "; "); got != want {
		t.Fatalf("executed %s", got)
	}
	if fx.f.readOnly[len(fx.f.readOnly)-1] {
		t.Fatal("loaded on a read-only session")
	}
	if n, err := fx.s.VerifyAudit(); err != nil || n != 3 {
		t.Fatalf("audit = %d, %v", n, err)
	}
}

func TestLoadNothing(t *testing.T) {
	fx := setup(t, store.Profile{})
	if n, err := fx.conn.Load(context.Background(), fakeRef, []string{"n"}, records(0), RunOptions{}); err != nil || n != 0 {
		t.Fatalf("n = %d, %v", n, err)
	}
	if len(fx.f.executed) != 0 {
		t.Fatalf("executed %q", fx.f.executed)
	}
}

func TestLoadFailureRollsBackEverything(t *testing.T) {
	fx := setup(t, store.Profile{})
	i := 0
	next := func() ([]tile.Value, error) {
		i++
		if i == 700 {
			return nil, errors.New("bad CSV")
		}
		return []tile.Value{tile.Text("x")}, nil
	}
	n, err := fx.conn.Load(context.Background(), fakeRef, []string{"n"}, next, RunOptions{})
	if err == nil || n != 0 {
		t.Fatalf("n = %d, %v", n, err)
	}
	if got := strings.Join(fx.f.executed, "; "); got != "BEGIN; INSERT INTO t VALUES 500; ROLLBACK" {
		t.Fatalf("executed %s", got)
	}
	if fx.f.closedSession != 1 {
		t.Fatalf("closed %d sessions", fx.f.closedSession)
	}
}

func TestLoadIsGated(t *testing.T) {
	fx := setup(t, store.Profile{ReadOnly: true})
	if _, err := fx.conn.Load(context.Background(), fakeRef, []string{"n"}, records(3), RunOptions{Confirm: always(true)}); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("read-only: %v", err)
	}
	fx = setup(t, store.Profile{Env: EnvProduction})
	if _, err := fx.conn.Load(context.Background(), fakeRef, []string{"n"}, records(3), RunOptions{}); !errors.Is(err, ErrNeedsConfirm) {
		t.Fatalf("production: %v", err)
	}
	if len(fx.f.executed) != 0 {
		t.Fatalf("executed %q", fx.f.executed)
	}
	if n, err := fx.conn.Load(context.Background(), fakeRef, []string{"n"}, records(3), RunOptions{Confirm: always(true)}); err != nil || n != 3 {
		t.Fatalf("confirmed: %d, %v", n, err)
	}
}

func TestLoadRefusesARaggedRecord(t *testing.T) {
	fx := setup(t, store.Profile{})
	if _, err := fx.conn.Load(context.Background(), fakeRef, []string{"n", "m"}, records(2), RunOptions{}); err == nil {
		t.Fatal("a record with too few values was accepted")
	}
}

func TestExportStreamsOneReadOnlyRequest(t *testing.T) {
	fx := setup(t, store.Profile{})
	var schemas, rows int
	err := fx.conn.Export(context.Background(), fakeRef, func(f tile.Frame) error {
		if f.Schema != nil {
			schemas++
		}
		if f.Batch != nil {
			rows += f.Batch.Rows
		}
		if f.Done != nil {
			t.Fatal("export passes no summary")
		}
		return nil
	})
	if err != nil || schemas != 1 || rows != 2 {
		t.Fatalf("schemas %d rows %d err %v", schemas, rows, err)
	}
	if len(fx.f.executed) != 1 || !fx.f.readOnly[len(fx.f.readOnly)-1] {
		t.Fatalf("executed %q read-only %v", fx.f.executed, fx.f.readOnly)
	}
}
