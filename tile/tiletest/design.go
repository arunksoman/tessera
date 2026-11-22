package tiletest

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/arunksoman/tessera/tile"
)

// Design supplies what the structure tests need.
type Design struct {
	// Create makes tt_design (id integer primary key, name text up to 50 characters that may
	// be null) and tt_design_ref (id integer primary key) in Schema, dropping them on cleanup.
	Create func(t *testing.T, s tile.Session)
	// IntType is a native integer type for new fields.
	IntType string
	// TriggerAction is a trigger action that upper-cases name before each row is inserted into
	// tt_design. Create also makes anything it relies on, such as a function.
	TriggerAction string
}

func designer(t *testing.T, c tile.Conn) tile.Designer {
	t.Helper()
	d, ok := c.Tree().(tile.Designer)
	if !ok {
		t.Fatal("tile has no Designer facet")
	}
	return d
}

// runPlan applies a plan as the engine does, checking row counts the plan asks for.
func runPlan(t *testing.T, s tile.Session, p tile.Plan) {
	t.Helper()
	if p.Atomicity == tile.AtomicAll && p.Begin.Text == "" {
		t.Fatalf("an atomic plan has no Begin: %+v", p)
	}
	ctx := context.Background()
	run := func(req tile.Request, expect int64) {
		t.Helper()
		if req.Text == "" {
			return
		}
		c, err := collect(ctx, s, req)
		if err != nil {
			t.Fatalf("%s: %v", req.Text, err)
		}
		if expect >= 0 && (c.done == nil || c.done.RowsAffected != expect) {
			t.Fatalf("%s: summary %+v, want %d rows", req.Text, c.done, expect)
		}
	}
	run(p.Begin, -1)
	for _, st := range p.Steps {
		run(st.Request, st.ExpectRows)
	}
	run(p.Commit, -1)
}

func describe(t *testing.T, d tile.Designer, ref tile.ObjectRef) tile.Structure {
	t.Helper()
	s, err := d.DescribeStructure(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func fieldNamed(s tile.Structure, name string) *tile.FieldDef {
	for i := range s.Fields {
		if s.Fields[i].Name == name {
			return &s.Fields[i]
		}
	}
	return nil
}

func count(t *testing.T, s tile.Session, table string) string {
	t.Helper()
	return column(mustCollect(t, s, "SELECT count(*) FROM "+table).rows, 0)[0]
}

func testDesign(t *testing.T, h Harness) {
	if h.Design == nil {
		t.Skip("no structure tests")
	}
	c := h.Open(t)
	s := session(t, c, tile.SessionOptions{})
	h.Design.Create(t, s)
	d := designer(t, c)
	ref := h.Schema.Child("table", "tt_design")
	apply := func(changes ...tile.StructureChange) {
		t.Helper()
		p, err := d.PlanStructure(ref, changes)
		if err != nil {
			t.Fatal(err)
		}
		for _, st := range p.Steps {
			if st.ExpectRows >= 0 {
				t.Fatalf("a structure step checks a row count: %+v", st)
			}
		}
		runPlan(t, s, p)
	}

	st := describe(t, d, ref)
	id, name := fieldNamed(st, "id"), fieldNamed(st, "name")
	if id == nil || !id.Primary || id.Nullable || id.Type == "" || name == nil || !name.Nullable || name.Primary {
		t.Fatalf("fields = %+v", st.Fields)
	}
	if len(st.Indexes) == 0 || !st.Indexes[0].Primary || !slices.Equal(st.Indexes[0].Fields, []string{"id"}) {
		t.Fatalf("indexes = %+v", st.Indexes)
	}

	hostile := "we\"ird`; DROP TABLE orders;--'x"
	apply(
		tile.StructureChange{Op: tile.OpFieldAdd, Field: &tile.FieldDef{Name: "qty", Type: h.Design.IntType, Default: "0", Comment: "how many's"}},
		tile.StructureChange{Op: tile.OpFieldAdd, Field: &tile.FieldDef{Name: "ref_id", Type: h.Design.IntType, Nullable: true}},
		tile.StructureChange{Op: tile.OpFieldAdd, Field: &tile.FieldDef{Name: hostile, Type: h.Design.IntType, Nullable: true}},
		tile.StructureChange{Op: tile.OpIndexAdd, Index: &tile.IndexDef{Name: "tt_design_name_idx", Fields: []string{"name"}, Unique: true}},
		tile.StructureChange{Op: tile.OpRelationAdd, Relation: &tile.Relation{ForeignKey: tile.ForeignKey{
			Name: "tt_design_ref_fk", Fields: []string{"ref_id"}, Target: h.Schema.Child("table", "tt_design_ref"), TargetFields: []string{"id"},
		}, OnDelete: "CASCADE"}},
		tile.StructureChange{Op: tile.OpTriggerAdd, Trigger: &tile.TriggerDef{
			Name: "tt_design_trg", Timing: "BEFORE", Events: []string{"INSERT"}, ForEach: "ROW", Action: h.Design.TriggerAction,
		}},
	)
	st = describe(t, d, ref)
	qty := fieldNamed(st, "qty")
	if qty == nil || qty.Nullable || !strings.Contains(qty.Default, "0") || qty.Comment != "how many's" {
		t.Fatalf("qty = %+v", qty)
	}
	if fieldNamed(st, hostile) == nil {
		t.Fatalf("hostile field missing from %+v", st.Fields)
	}
	ix := slices.IndexFunc(st.Indexes, func(i tile.IndexDef) bool { return i.Name == "tt_design_name_idx" })
	if ix < 0 || !st.Indexes[ix].Unique || !slices.Equal(st.Indexes[ix].Fields, []string{"name"}) || st.Indexes[ix].Definition == "" {
		t.Fatalf("indexes = %+v", st.Indexes)
	}
	if len(st.Relations) != 1 {
		t.Fatalf("relations = %+v", st.Relations)
	}
	rel := st.Relations[0]
	if rel.Name != "tt_design_ref_fk" || !slices.Equal(rel.Fields, []string{"ref_id"}) || !slices.Equal(rel.TargetFields, []string{"id"}) ||
		fmt.Sprint(rel.Target.Path) != fmt.Sprint(h.Schema.Child("table", "tt_design_ref").Path) || rel.OnDelete != "CASCADE" {
		t.Fatalf("relation = %+v", rel)
	}
	if len(st.Triggers) != 1 {
		t.Fatalf("triggers = %+v", st.Triggers)
	}
	tr := st.Triggers[0]
	if tr.Name != "tt_design_trg" || tr.Timing != "BEFORE" || !slices.Equal(tr.Events, []string{"INSERT"}) || tr.ForEach != "ROW" ||
		tr.Action == "" || tr.Definition == "" {
		t.Fatalf("trigger = %+v", tr)
	}

	// Loading runs through the trigger, which upper-cases names.
	l, ok := c.(tile.Loader)
	if !ok {
		t.Fatal("tile has no Loader facet")
	}
	fields := []string{"id", "name", "qty"}
	p, err := l.InsertPlan(ref, fields, [][]tile.Value{
		{tile.Text("1"), tile.Text("ab"), tile.Text("5")},
		{tile.Text("2"), {Kind: tile.KindNull}, tile.Text("6")},
	})
	if err != nil {
		t.Fatal(err)
	}
	runPlan(t, s, p)
	rows := mustCollect(t, s, "SELECT name, qty FROM tt_design ORDER BY id").rows
	if len(rows) != 2 || string(rows[0][0].Raw) != "AB" || rows[1][0].Kind != tile.KindNull || string(rows[1][1].Raw) != "6" {
		t.Fatalf("loaded %v", rows)
	}
	script, err := l.InsertScript(ref, fields, [][]tile.Value{{{Kind: tile.KindInt, Raw: []byte("3")}, tile.Text("it's"), {Kind: tile.KindNull}}})
	if err == nil {
		_, err = collect(context.Background(), s, tile.Request{Text: strings.TrimRight(script, ";\n")})
	}
	if err == nil {
		// A NULL into NOT NULL qty must fail, so the script carries NULL as NULL, not as text.
		t.Fatal("a NULL loaded into a NOT NULL field")
	}
	script, err = l.InsertScript(ref, []string{"id", "name"}, [][]tile.Value{{{Kind: tile.KindInt, Raw: []byte("3")}, tile.Text(`it's a\b`)}})
	if err != nil {
		t.Fatal(err)
	}
	mustCollect(t, s, strings.TrimRight(script, ";\n"))
	if got := column(mustCollect(t, s, "SELECT name FROM tt_design WHERE id = 3").rows, 0)[0]; got != `IT'S A\B` {
		t.Fatalf("script loaded %q", got)
	}

	from := *fieldNamed(st, "name")
	to := from
	to.Name, to.Default, to.Comment = "label", "'none'", "renamed"
	apply(tile.StructureChange{Op: tile.OpFieldAlter, From: &from, Field: &to})
	st = describe(t, d, ref)
	label := fieldNamed(st, "label")
	if fieldNamed(st, "name") != nil || label == nil || !strings.Contains(label.Default, "none") || label.Comment != "renamed" || label.Type != from.Type {
		t.Fatalf("after alter: %+v", st.Fields)
	}

	apply(
		tile.StructureChange{Op: tile.OpTriggerDrop, Name: "tt_design_trg"},
		tile.StructureChange{Op: tile.OpRelationDrop, Name: "tt_design_ref_fk"},
		tile.StructureChange{Op: tile.OpIndexDrop, Index: &st.Indexes[slices.IndexFunc(st.Indexes, func(i tile.IndexDef) bool { return i.Name == "tt_design_name_idx" })]},
		tile.StructureChange{Op: tile.OpFieldDrop, Name: "qty"},
		tile.StructureChange{Op: tile.OpFieldDrop, Name: hostile},
	)
	st = describe(t, d, ref)
	if fieldNamed(st, "qty") != nil || len(st.Relations) != 0 || len(st.Triggers) != 0 ||
		slices.ContainsFunc(st.Indexes, func(i tile.IndexDef) bool { return i.Name == "tt_design_name_idx" }) {
		t.Fatalf("after drops: %+v", st)
	}
}

func testCreateScript(t *testing.T, h Harness) {
	if h.Design == nil {
		t.Skip("no structure tests")
	}
	c := h.Open(t)
	s := session(t, c, tile.SessionOptions{})
	h.Design.Create(t, s)
	d := designer(t, c)
	ref := h.Schema.Child("table", "tt_design")
	runPlan(t, s, mustPlan(t, d, ref,
		tile.StructureChange{Op: tile.OpFieldAdd, Field: &tile.FieldDef{Name: "qty", Type: h.Design.IntType, Default: "7", Comment: "count"}},
		tile.StructureChange{Op: tile.OpIndexAdd, Index: &tile.IndexDef{Name: "tt_design_qty_idx", Fields: []string{"qty"}}},
		tile.StructureChange{Op: tile.OpTriggerAdd, Trigger: &tile.TriggerDef{
			Name: "tt_design_trg", Timing: "BEFORE", Events: []string{"INSERT"}, ForEach: "ROW", Action: h.Design.TriggerAction,
		}},
	))
	before := describe(t, d, ref)
	script, err := d.CreateScript(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "tt_design") {
		t.Fatalf("script = %s", script)
	}
	runPlan(t, s, mustPlan(t, d, ref, tile.StructureChange{Op: tile.OpDrop}))
	lang := c.Language()
	for _, sp := range lang.Split(script) {
		mustCollect(t, s, script[sp.Start:sp.End])
	}
	after := describe(t, d, ref)
	strip := func(st tile.Structure) string {
		for i := range st.Indexes {
			st.Indexes[i].Definition = ""
		}
		for i := range st.Triggers {
			st.Triggers[i].Definition = ""
		}
		return fmt.Sprintf("%+v", st)
	}
	if strip(before) != strip(after) {
		t.Fatalf("script did not recreate the table:\nbefore %s\nafter  %s\nscript %s", strip(before), strip(after), script)
	}
}

func mustPlan(t *testing.T, d tile.Designer, ref tile.ObjectRef, changes ...tile.StructureChange) tile.Plan {
	t.Helper()
	p, err := d.PlanStructure(ref, changes)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func testObjectActions(t *testing.T, h Harness) {
	if h.Design == nil {
		t.Skip("no structure tests")
	}
	c := h.Open(t)
	s := session(t, c, tile.SessionOptions{})
	h.Design.Create(t, s)
	d := designer(t, c)
	ref := h.Schema.Child("table", "tt_design")
	mustCollect(t, s, "INSERT INTO tt_design (id, name) VALUES (1, 'a'), (2, 'b')")
	runPlan(t, s, mustPlan(t, d, ref, tile.StructureChange{Op: tile.OpTruncate}))
	if n := count(t, s, "tt_design"); n != "0" {
		t.Fatalf("after truncate: %s rows", n)
	}
	listed := func(name string) bool {
		return slices.ContainsFunc(allChildren(t, c.Tree(), h.Schema, 100), func(n tile.Node) bool { return n.Label == name })
	}
	runPlan(t, s, mustPlan(t, d, ref, tile.StructureChange{Op: tile.OpRename, Name: "tt_design_renamed"}))
	if listed("tt_design") || !listed("tt_design_renamed") {
		t.Fatal("rename did not show in the tree")
	}
	renamed := h.Schema.Child("table", "tt_design_renamed")
	runPlan(t, s, mustPlan(t, d, renamed, tile.StructureChange{Op: tile.OpDrop}))
	if listed("tt_design_renamed") {
		t.Fatal("drop did not remove the table")
	}
	if _, err := d.PlanStructure(h.Schema.Child("view", "v"), []tile.StructureChange{{Op: tile.OpTruncate}}); err == nil {
		t.Fatal("planned a truncate of a view")
	}
}
