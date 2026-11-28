package sqlite

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/arunksoman/tessera/tile"
)

// The conformance suite's structure tests need comments and added relations, which SQLite
// lacks, so the SQLite subset is checked here.

func applyPlan(t *testing.T, s tile.Session, p tile.Plan) {
	t.Helper()
	if p.Atomicity != tile.AtomicAll || p.Begin.Text == "" {
		t.Fatalf("plan is not atomic: %+v", p)
	}
	exec(t, s, p.Begin.Text)
	for _, st := range p.Steps {
		r := exec(t, s, st.Request.Text, st.Request.Args...)
		if st.ExpectRows >= 0 && (r.done == nil || r.done.RowsAffected != st.ExpectRows) {
			t.Fatalf("%s: summary %+v, want %d rows", st.Request.Text, r.done, st.ExpectRows)
		}
	}
	exec(t, s, p.Commit.Text)
}

func TestDesign(t *testing.T) {
	ctx := context.Background()
	c := openPath(t, Tile{}, fixtureDB(t))
	s := newSession(t, c, tile.SessionOptions{})
	exec(t, s, `CREATE TABLE tt_design (id int PRIMARY KEY, name varchar(50))`)
	d := c.Tree().(tile.Designer)
	ref := tile.ObjectRef{}.Child("schema", "main").Child("table", "tt_design")
	plan := func(changes ...tile.StructureChange) (tile.Plan, error) { return d.PlanStructure(ref, changes) }
	apply := func(changes ...tile.StructureChange) {
		t.Helper()
		p, err := plan(changes...)
		if err != nil {
			t.Fatal(err)
		}
		applyPlan(t, s, p)
	}
	describe := func() tile.Structure {
		t.Helper()
		st, err := d.DescribeStructure(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}

	st := describe()
	if len(st.Fields) != 2 || !st.Fields[0].Primary || st.Fields[0].Nullable || !st.Fields[1].Nullable {
		t.Fatalf("fields = %+v", st.Fields)
	}
	if len(st.Indexes) != 1 || !st.Indexes[0].Primary || !slices.Equal(st.Indexes[0].Fields, []string{"id"}) {
		t.Fatalf("indexes = %+v", st.Indexes)
	}

	hostile := "we\"ird`; DROP TABLE orders;--'x"
	apply(
		tile.StructureChange{Op: tile.OpFieldAdd, Field: &tile.FieldDef{Name: "qty", Type: "integer", Default: "0"}},
		tile.StructureChange{Op: tile.OpFieldAdd, Field: &tile.FieldDef{Name: hostile, Type: "integer", Nullable: true}},
		tile.StructureChange{Op: tile.OpIndexAdd, Index: &tile.IndexDef{Name: "tt_design_name_idx", Fields: []string{"name"}, Unique: true}},
		tile.StructureChange{Op: tile.OpTriggerAdd, Trigger: &tile.TriggerDef{
			Name: "tt_design_trg", Timing: "AFTER", Events: []string{"INSERT"}, ForEach: "ROW",
			Action: "UPDATE tt_design SET name = upper(name) WHERE id = NEW.id; UPDATE tt_design SET qty = qty + 1 WHERE id = NEW.id;",
		}},
	)
	st = describe()
	if f := st.Fields[2]; f.Name != "qty" || f.Nullable || f.Default != "0" || st.Fields[3].Name != hostile {
		t.Fatalf("fields = %+v", st.Fields)
	}
	ix := slices.IndexFunc(st.Indexes, func(i tile.IndexDef) bool { return i.Name == "tt_design_name_idx" })
	if ix < 0 || !st.Indexes[ix].Unique || st.Indexes[ix].Constraint || !strings.Contains(st.Indexes[ix].Definition, "CREATE UNIQUE INDEX") {
		t.Fatalf("indexes = %+v", st.Indexes)
	}
	if len(st.Triggers) != 1 || st.Triggers[0].Timing != "AFTER" || !slices.Equal(st.Triggers[0].Events, []string{"INSERT"}) ||
		!strings.Contains(st.Triggers[0].Action, "qty + 1") {
		t.Fatalf("triggers = %+v", st.Triggers)
	}

	// The trigger's own changes do not count against the plan's expected rows.
	p, err := c.(tile.Loader).InsertPlan(ref, []string{"id", "name"}, [][]tile.Value{{tile.Text("1"), tile.Text("ab")}, {tile.Text("2"), {Kind: tile.KindNull}}})
	if err != nil {
		t.Fatal(err)
	}
	applyPlan(t, s, p)
	if r := exec(t, s, `SELECT name, qty FROM tt_design ORDER BY id`).rows; string(r[0][0].Raw) != "AB" || string(r[0][1].Raw) != "1" {
		t.Fatalf("loaded %v", r)
	}

	from := st.Fields[1]
	to := from
	to.Name = "label"
	apply(tile.StructureChange{Op: tile.OpFieldAlter, From: &from, Field: &to})
	if describe().Fields[1].Name != "label" {
		t.Fatal("rename did not apply")
	}
	to.Type = "text"
	refused := []tile.StructureChange{
		{Op: tile.OpFieldAlter, From: &from, Field: &to},
		{Op: tile.OpFieldAdd, Field: &tile.FieldDef{Name: "c", Type: "int", Comment: "x"}},
		{Op: tile.OpRelationAdd, Relation: &tile.Relation{ForeignKey: tile.ForeignKey{Name: "r", Fields: []string{"qty"},
			Target: tile.ObjectRef{}.Child("schema", "main").Child("table", "customers"), TargetFields: []string{"id"}}}},
		{Op: tile.OpIndexDrop, Index: &st.Indexes[0]},
	}
	for _, ch := range refused {
		if _, err := plan(ch); err == nil {
			t.Errorf("planned %s, which needs a rebuild", ch.Op)
		}
	}

	// The script recreates the table as it was.
	before := describe()
	script, err := d.CreateScript(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	apply(tile.StructureChange{Op: tile.OpDrop})
	for _, sp := range c.Language().Split(script) {
		exec(t, s, script[sp.Start:sp.End])
	}
	if after := describe(); len(after.Fields) != len(before.Fields) || len(after.Indexes) != len(before.Indexes) || len(after.Triggers) != 1 {
		t.Fatalf("script did not recreate the table:\n%s", script)
	}

	apply(
		tile.StructureChange{Op: tile.OpTriggerDrop, Name: "tt_design_trg"},
		tile.StructureChange{Op: tile.OpIndexDrop, Index: &before.Indexes[slices.IndexFunc(before.Indexes, func(i tile.IndexDef) bool { return i.Name == "tt_design_name_idx" })]},
		tile.StructureChange{Op: tile.OpFieldDrop, Name: hostile},
		tile.StructureChange{Op: tile.OpTruncate},
		tile.StructureChange{Op: tile.OpRename, Name: "tt_renamed"},
	)
	renamed := tile.ObjectRef{}.Child("schema", "main").Child("table", "tt_renamed")
	st, err = d.DescribeStructure(ctx, renamed)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Fields) != 3 || len(st.Triggers) != 0 || len(st.Indexes) != 1 {
		t.Fatalf("after drops: %+v", st)
	}
	if n := string(exec(t, s, `SELECT count(*) FROM tt_renamed`).rows[0][0].Raw); n != "0" {
		t.Fatalf("truncate left %s rows", n)
	}
	p, err = d.PlanStructure(renamed, []tile.StructureChange{{Op: tile.OpDrop}})
	if err != nil {
		t.Fatal(err)
	}
	applyPlan(t, s, p)
}
