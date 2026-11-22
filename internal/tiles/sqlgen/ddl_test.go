package sqlgen

import (
	"strings"
	"testing"

	"github.com/arunksoman/tessera/tile"
)

var my = Dialect{
	Quote:  func(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" },
	Mark:   func(int) string { return "?" },
	Text:   func(c string) string { return "CAST(" + c + " AS CHAR)" },
	Like:   "LIKE",
	Begin:  "START TRANSACTION",
	Family: FamilyMySQL,
}

func steps(t *testing.T, d Dialect, ref tile.ObjectRef, changes ...tile.StructureChange) []string {
	t.Helper()
	p, err := d.PlanStructure(ref, changes)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range p.Steps {
		if s.ExpectRows >= 0 {
			t.Fatalf("a structure step must not check row counts: %+v", s)
		}
		if s.Preview != s.Request.Text || len(s.Request.Args) > 0 {
			t.Fatalf("a structure step has no bound values: %+v", s)
		}
		out = append(out, s.Request.Text)
	}
	return out
}

func TestPlanStructure(t *testing.T) {
	ref := tile.ObjectRef{}.Child("schema", "s").Child("table", "t")
	view := tile.ObjectRef{}.Child("schema", "s").Child("view", "v")
	target := tile.ObjectRef{}.Child("schema", "s").Child("table", "u")
	name := &tile.FieldDef{Name: "name", Type: "varchar(50)", Nullable: true}
	cases := []struct {
		name   string
		d      Dialect
		ref    tile.ObjectRef
		change tile.StructureChange
		want   []string
	}{
		{"pg rename", pg, ref, tile.StructureChange{Op: tile.OpRename, Name: `n"x`}, []string{`ALTER TABLE "s"."t" RENAME TO "n""x"`}},
		{"my rename", my, ref, tile.StructureChange{Op: tile.OpRename, Name: "n"}, []string{"RENAME TABLE `s`.`t` TO `s`.`n`"}},
		{"truncate", pg, ref, tile.StructureChange{Op: tile.OpTruncate}, []string{`TRUNCATE TABLE "s"."t"`}},
		{"drop table", my, ref, tile.StructureChange{Op: tile.OpDrop}, []string{"DROP TABLE `s`.`t`"}},
		{"drop view", pg, view, tile.StructureChange{Op: tile.OpDrop}, []string{`DROP VIEW "s"."v"`}},
		{"pg add field", pg, ref, tile.StructureChange{Op: tile.OpFieldAdd, Field: &tile.FieldDef{Name: "qty", Type: "integer", Default: "0", Comment: "it's"}},
			[]string{`ALTER TABLE "s"."t" ADD COLUMN "qty" integer NOT NULL DEFAULT 0`, `COMMENT ON COLUMN "s"."t"."qty" IS 'it''s'`}},
		{"my add field", my, ref, tile.StructureChange{Op: tile.OpFieldAdd, Field: &tile.FieldDef{Name: "qty", Type: "int", Nullable: true, Default: "0", Comment: `a\b`}},
			[]string{"ALTER TABLE `s`.`t` ADD COLUMN `qty` int NULL DEFAULT 0 COMMENT 'a\\\\b'"}},
		{"my add field keeps collation and extra", my, ref, tile.StructureChange{Op: tile.OpFieldAdd,
			Field: &tile.FieldDef{Name: "id", Type: "int unsigned", Extra: "auto_increment", Collation: "x"}},
			[]string{"ALTER TABLE `s`.`t` ADD COLUMN `id` int unsigned NOT NULL auto_increment"}},
		{"my generated field", my, ref, tile.StructureChange{Op: tile.OpFieldAdd,
			Field: &tile.FieldDef{Name: "g", Type: "int", Nullable: true, Extra: "GENERATED ALWAYS AS (`a` + 1) VIRTUAL"}},
			[]string{"ALTER TABLE `s`.`t` ADD COLUMN `g` int GENERATED ALWAYS AS (`a` + 1) VIRTUAL NULL"}},
		{"pg alter field states only what changed", pg, ref, tile.StructureChange{Op: tile.OpFieldAlter, From: name,
			Field: &tile.FieldDef{Name: "label", Type: "text", Default: "'x'", Comment: "c"}},
			[]string{
				`ALTER TABLE "s"."t" RENAME COLUMN "name" TO "label"`,
				`ALTER TABLE "s"."t" ALTER COLUMN "label" TYPE text`,
				`ALTER TABLE "s"."t" ALTER COLUMN "label" SET DEFAULT 'x'`,
				`ALTER TABLE "s"."t" ALTER COLUMN "label" SET NOT NULL`,
				`COMMENT ON COLUMN "s"."t"."label" IS 'c'`,
			}},
		{"pg alter field back", pg, ref, tile.StructureChange{Op: tile.OpFieldAlter, Field: name,
			From: &tile.FieldDef{Name: "name", Type: "varchar(50)", Default: "'x'", Comment: "c"}},
			[]string{
				`ALTER TABLE "s"."t" ALTER COLUMN "name" DROP DEFAULT`,
				`ALTER TABLE "s"."t" ALTER COLUMN "name" DROP NOT NULL`,
				`COMMENT ON COLUMN "s"."t"."name" IS NULL`,
			}},
		{"pg alter unchanged", pg, ref, tile.StructureChange{Op: tile.OpFieldAlter, From: name, Field: name}, nil},
		{"my alter restates the field", my, ref, tile.StructureChange{Op: tile.OpFieldAlter, From: name,
			Field: &tile.FieldDef{Name: "label", Type: "varchar(80)", Nullable: true, Collation: "utf8mb4_bin"}},
			[]string{"ALTER TABLE `s`.`t` CHANGE COLUMN `name` `label` varchar(80) COLLATE `utf8mb4_bin` NULL"}},
		{"drop field", pg, ref, tile.StructureChange{Op: tile.OpFieldDrop, Name: "qty"}, []string{`ALTER TABLE "s"."t" DROP COLUMN "qty"`}},
		{"pg index", pg, ref, tile.StructureChange{Op: tile.OpIndexAdd, Index: &tile.IndexDef{Name: "i", Fields: []string{"a", "(lower(b))"}, Unique: true, Method: "btree"}},
			[]string{`CREATE UNIQUE INDEX "i" ON "s"."t" USING btree ("a", (lower(b)))`}},
		{"pg unnamed index", pg, ref, tile.StructureChange{Op: tile.OpIndexAdd, Index: &tile.IndexDef{Fields: []string{"a"}}},
			[]string{`CREATE INDEX ON "s"."t" ("a")`}},
		{"pg primary key", pg, ref, tile.StructureChange{Op: tile.OpIndexAdd, Index: &tile.IndexDef{Name: "t_pkey", Fields: []string{"id"}, Primary: true}},
			[]string{`ALTER TABLE "s"."t" ADD CONSTRAINT "t_pkey" PRIMARY KEY ("id")`}},
		{"pg unique constraint", pg, ref, tile.StructureChange{Op: tile.OpIndexAdd, Index: &tile.IndexDef{Name: "u", Fields: []string{"a"}, Unique: true, Constraint: true}},
			[]string{`ALTER TABLE "s"."t" ADD CONSTRAINT "u" UNIQUE ("a")`}},
		{"my index", my, ref, tile.StructureChange{Op: tile.OpIndexAdd, Index: &tile.IndexDef{Name: "i", Fields: []string{"a", "b"}, Unique: true}},
			[]string{"ALTER TABLE `s`.`t` ADD UNIQUE INDEX `i` (`a`, `b`)"}},
		{"my fulltext", my, ref, tile.StructureChange{Op: tile.OpIndexAdd, Index: &tile.IndexDef{Name: "f", Fields: []string{"a"}, Method: "FULLTEXT"}},
			[]string{"ALTER TABLE `s`.`t` ADD FULLTEXT INDEX `f` (`a`)"}},
		{"my primary key", my, ref, tile.StructureChange{Op: tile.OpIndexAdd, Index: &tile.IndexDef{Name: "PRIMARY", Fields: []string{"id"}, Primary: true}},
			[]string{"ALTER TABLE `s`.`t` ADD PRIMARY KEY (`id`)"}},
		{"pg drop index", pg, ref, tile.StructureChange{Op: tile.OpIndexDrop, Index: &tile.IndexDef{Name: "i"}}, []string{`DROP INDEX "s"."i"`}},
		{"pg drop constraint index", pg, ref, tile.StructureChange{Op: tile.OpIndexDrop, Index: &tile.IndexDef{Name: "t_pkey", Primary: true}},
			[]string{`ALTER TABLE "s"."t" DROP CONSTRAINT "t_pkey"`}},
		{"my drop index", my, ref, tile.StructureChange{Op: tile.OpIndexDrop, Index: &tile.IndexDef{Name: "i"}}, []string{"ALTER TABLE `s`.`t` DROP INDEX `i`"}},
		{"my drop primary", my, ref, tile.StructureChange{Op: tile.OpIndexDrop, Index: &tile.IndexDef{Name: "PRIMARY", Primary: true}},
			[]string{"ALTER TABLE `s`.`t` DROP PRIMARY KEY"}},
		{"pg relation", pg, ref, tile.StructureChange{Op: tile.OpRelationAdd, Relation: &tile.Relation{
			ForeignKey: tile.ForeignKey{Name: "fk", Fields: []string{"u_id"}, Target: target, TargetFields: []string{"id"}}, OnDelete: "cascade"}},
			[]string{`ALTER TABLE "s"."t" ADD CONSTRAINT "fk" FOREIGN KEY ("u_id") REFERENCES "s"."u" ("id") ON DELETE CASCADE`}},
		{"my relation", my, ref, tile.StructureChange{Op: tile.OpRelationAdd, Relation: &tile.Relation{
			ForeignKey: tile.ForeignKey{Fields: []string{"a", "b"}, Target: target, TargetFields: []string{"x", "y"}}, OnUpdate: "SET NULL", OnDelete: "RESTRICT"}},
			[]string{"ALTER TABLE `s`.`t` ADD FOREIGN KEY (`a`, `b`) REFERENCES `s`.`u` (`x`, `y`) ON UPDATE SET NULL ON DELETE RESTRICT"}},
		{"pg drop relation", pg, ref, tile.StructureChange{Op: tile.OpRelationDrop, Name: "fk"}, []string{`ALTER TABLE "s"."t" DROP CONSTRAINT "fk"`}},
		{"my drop relation", my, ref, tile.StructureChange{Op: tile.OpRelationDrop, Name: "fk"}, []string{"ALTER TABLE `s`.`t` DROP FOREIGN KEY `fk`"}},
		{"pg trigger", pg, ref, tile.StructureChange{Op: tile.OpTriggerAdd, Trigger: &tile.TriggerDef{
			Name: "trg", Timing: "before", Events: []string{"insert", "UPDATE"}, ForEach: "row", Condition: "NEW.a > 0", Action: "EXECUTE FUNCTION f()"}},
			[]string{`CREATE TRIGGER "trg" BEFORE INSERT OR UPDATE ON "s"."t" FOR EACH ROW WHEN (NEW.a > 0) EXECUTE FUNCTION f()`}},
		{"my trigger body", my, ref, tile.StructureChange{Op: tile.OpTriggerAdd, Trigger: &tile.TriggerDef{
			Name: "trg", Timing: "AFTER", Events: []string{"DELETE"}, ForEach: "ROW", Action: "BEGIN SET @a = 1; SET @b = 2; END"}},
			[]string{"CREATE TRIGGER `s`.`trg` AFTER DELETE ON `s`.`t` FOR EACH ROW BEGIN SET @a = 1; SET @b = 2; END"}},
		{"pg drop trigger", pg, ref, tile.StructureChange{Op: tile.OpTriggerDrop, Name: "trg"}, []string{`DROP TRIGGER "trg" ON "s"."t"`}},
		{"my drop trigger", my, ref, tile.StructureChange{Op: tile.OpTriggerDrop, Name: "trg"}, []string{"DROP TRIGGER `s`.`trg`"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := steps(t, c.d, c.ref, c.change)
			if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
				t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(c.want, "\n"))
			}
		})
	}
}

func TestPlanStructureAtomicity(t *testing.T) {
	ref := tile.ObjectRef{}.Child("schema", "s").Child("table", "t")
	ch := tile.StructureChange{Op: tile.OpFieldDrop, Name: "a"}
	p, err := pg.PlanStructure(ref, []tile.StructureChange{ch, ch})
	if err != nil || p.Atomicity != tile.AtomicAll || p.Begin.Text != "BEGIN" || p.Commit.Text != "COMMIT" || len(p.Steps) != 2 {
		t.Fatalf("pg: %+v %v", p, err)
	}
	// MySQL commits DDL implicitly, so its plan says each step stands alone.
	p, err = my.PlanStructure(ref, []tile.StructureChange{ch})
	if err != nil || p.Atomicity != tile.AtomicPerItem || p.Begin.Text != "" || p.Commit.Text != "" {
		t.Fatalf("mysql: %+v %v", p, err)
	}
}

func TestPlanStructureRefuses(t *testing.T) {
	ref := tile.ObjectRef{}.Child("schema", "s").Child("table", "t")
	view := tile.ObjectRef{}.Child("schema", "s").Child("view", "v")
	field := func(typ, def string) *tile.FieldDef { return &tile.FieldDef{Name: "a", Type: typ, Default: def} }
	trig := func(tr tile.TriggerDef) *tile.TriggerDef {
		if tr.Name == "" {
			tr.Name = "t"
		}
		if tr.Action == "" {
			tr.Action = "EXECUTE FUNCTION f()"
		}
		return &tr
	}
	cases := []struct {
		name   string
		d      Dialect
		ref    tile.ObjectRef
		change tile.StructureChange
	}{
		{"no changes", pg, ref, tile.StructureChange{}},
		{"truncate a view", pg, view, tile.StructureChange{Op: tile.OpTruncate}},
		{"empty rename", pg, ref, tile.StructureChange{Op: tile.OpRename}},
		{"empty type", pg, ref, tile.StructureChange{Op: tile.OpFieldAdd, Field: field("", "")}},
		{"semicolon in type", pg, ref, tile.StructureChange{Op: tile.OpFieldAdd, Field: field("int; DROP TABLE x", "")}},
		{"comment in default", my, ref, tile.StructureChange{Op: tile.OpFieldAdd, Field: field("int", "1 -- x")}},
		{"hash comment in mysql", my, ref, tile.StructureChange{Op: tile.OpFieldAdd, Field: field("int", "1 # x")}},
		{"block comment", pg, ref, tile.StructureChange{Op: tile.OpFieldAdd, Field: field("int", "1 /* x */")}},
		{"unbalanced parenthesis", pg, ref, tile.StructureChange{Op: tile.OpFieldAdd, Field: field("int", "(1))")}},
		{"unclosed quote", pg, ref, tile.StructureChange{Op: tile.OpFieldAdd, Field: field("text", "'a")}},
		{"mysql escaped quote does not close", my, ref, tile.StructureChange{Op: tile.OpFieldAdd, Field: field("text", `'a\'`)}},
		{"alter without from", pg, ref, tile.StructureChange{Op: tile.OpFieldAlter, Field: field("int", "")}},
		{"pg identity change", pg, ref, tile.StructureChange{Op: tile.OpFieldAlter, From: field("int", ""),
			Field: &tile.FieldDef{Name: "a", Type: "int", Extra: "GENERATED ALWAYS AS IDENTITY"}}},
		{"index without fields", pg, ref, tile.StructureChange{Op: tile.OpIndexAdd, Index: &tile.IndexDef{Name: "i"}}},
		{"bad pg method", pg, ref, tile.StructureChange{Op: tile.OpIndexAdd, Index: &tile.IndexDef{Fields: []string{"a"}, Method: "btree; x"}}},
		{"bad mysql method", my, ref, tile.StructureChange{Op: tile.OpIndexAdd, Index: &tile.IndexDef{Fields: []string{"a"}, Method: "gin"}}},
		{"relation width", pg, ref, tile.StructureChange{Op: tile.OpRelationAdd, Relation: &tile.Relation{
			ForeignKey: tile.ForeignKey{Fields: []string{"a"}, Target: ref, TargetFields: []string{"x", "y"}}}}},
		{"relation action", pg, ref, tile.StructureChange{Op: tile.OpRelationAdd, Relation: &tile.Relation{
			ForeignKey: tile.ForeignKey{Fields: []string{"a"}, Target: ref, TargetFields: []string{"x"}}, OnDelete: "DROP"}}},
		{"trigger timing", pg, ref, tile.StructureChange{Op: tile.OpTriggerAdd, Trigger: trig(tile.TriggerDef{Timing: "WHENEVER", Events: []string{"INSERT"}})}},
		{"trigger event", pg, ref, tile.StructureChange{Op: tile.OpTriggerAdd, Trigger: trig(tile.TriggerDef{Timing: "AFTER", Events: []string{"SELECT"}})}},
		{"pg trigger action semicolon", pg, ref, tile.StructureChange{Op: tile.OpTriggerAdd, Trigger: trig(tile.TriggerDef{Timing: "AFTER", Events: []string{"INSERT"},
			Action: "EXECUTE FUNCTION f(); DROP TABLE x"})}},
		{"mysql trigger events", my, ref, tile.StructureChange{Op: tile.OpTriggerAdd, Trigger: trig(tile.TriggerDef{Timing: "AFTER", Events: []string{"INSERT", "UPDATE"}, Action: "SET @a = 1"})}},
		{"mysql trigger condition", my, ref, tile.StructureChange{Op: tile.OpTriggerAdd, Trigger: trig(tile.TriggerDef{Timing: "AFTER", Events: []string{"INSERT"}, Condition: "1", Action: "SET @a = 1"})}},
		{"unknown op", pg, ref, tile.StructureChange{Op: "vacuum"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var changes []tile.StructureChange
			if c.change.Op != "" {
				changes = append(changes, c.change)
			}
			if p, err := c.d.PlanStructure(c.ref, changes); err == nil {
				t.Fatalf("planned %+v", p.Steps)
			}
		})
	}
}

func TestInsertPlan(t *testing.T) {
	ref := tile.ObjectRef{}.Child("schema", "s").Child("table", "t")
	rows := [][]tile.Value{{tile.Text("1"), tile.Text("a'b")}, {tile.Text("2"), {Kind: tile.KindNull}}}
	p, err := pg.InsertPlan(ref, []string{"id", "name"}, rows)
	if err != nil {
		t.Fatal(err)
	}
	st := p.Steps[0]
	if st.Request.Text != `INSERT INTO "s"."t" ("id", "name") VALUES ($1, $2), ($3, $4)` || len(st.Request.Args) != 4 || st.ExpectRows != 2 {
		t.Fatalf("%+v", st)
	}
	if st.Preview != `INSERT INTO "s"."t" ("id", "name") VALUES ('1', 'a''b'), ('2', NULL)` {
		t.Fatalf("preview %s", st.Preview)
	}
	if p.Begin.Text != "BEGIN" || p.Commit.Text != "COMMIT" || p.Rollback.Text != "ROLLBACK" {
		t.Fatalf("%+v", p)
	}
	p, err = my.InsertPlan(ref, []string{"id", "name"}, rows[:1])
	if err != nil || p.Steps[0].Request.Text != "INSERT INTO `s`.`t` (`id`, `name`) VALUES (?, ?)" || p.Begin.Text != "START TRANSACTION" {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := pg.InsertPlan(ref, []string{"id"}, rows); err == nil {
		t.Fatal("a row wider than the fields was accepted")
	}
	if _, err := pg.InsertPlan(ref, []string{"id"}, nil); err == nil {
		t.Fatal("an empty insert was accepted")
	}
}

func TestInsertScript(t *testing.T) {
	ref := tile.ObjectRef{}.Child("schema", "s").Child("table", "t")
	fields := []string{"i", "d", "f", "b", "t", "x", "j"}
	row := []tile.Value{
		{Kind: tile.KindInt, Raw: []byte("9007199254740993")},
		{Kind: tile.KindDecimal, Raw: []byte("-1.50")},
		{Kind: tile.KindFloat, Raw: []byte("NaN")},
		{Kind: tile.KindBool, Raw: []byte("t")},
		tile.Text(`it's a\b`),
		{Kind: tile.KindBytes, Raw: []byte{0xde, 0xad}},
		{Kind: tile.KindNull},
	}
	got, err := pg.InsertScript(ref, fields, [][]tile.Value{row, row})
	if err != nil {
		t.Fatal(err)
	}
	line := `(9007199254740993, -1.50, 'NaN', TRUE, 'it''s a\b', '\xdead', NULL)`
	want := `INSERT INTO "s"."t" ("i", "d", "f", "b", "t", "x", "j") VALUES` + "\n" + line + ",\n" + line + ";\n"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	got, _ = my.InsertScript(ref, fields, [][]tile.Value{row})
	if !strings.Contains(got, `'it''s a\\b', X'dead', NULL)`) {
		t.Fatalf("mysql: %s", got)
	}
	if got, _ := pg.InsertScript(ref, fields, nil); got != "" {
		t.Fatalf("no rows: %q", got)
	}
}
