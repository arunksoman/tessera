package sqlgen

import (
	"strconv"
	"strings"
	"testing"

	"github.com/arunksoman/tessera/tile"
)

var pg = Dialect{
	Quote: func(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` },
	Mark:  func(i int) string { return "$" + strconv.Itoa(i) },
	Text:  func(c string) string { return c + "::text" },
	Like:  "ILIKE",
	Begin: "BEGIN",
}

var layout = tile.Layout{
	Fields: []tile.Field{{Name: "id"}, {Name: "grp"}, {Name: "label"}},
	Key:    []string{"id"},
}

var table = tile.ObjectRef{}.Child("schema", "s").Child("table", "t")

func row(id, grp, label string) []tile.Value {
	null := func(s string) tile.Value {
		if s == "" {
			return tile.Value{Kind: tile.KindNull}
		}
		return tile.Text(s)
	}
	return []tile.Value{null(id), null(grp), null(label)}
}

func TestSelect(t *testing.T) {
	const cols = `SELECT "id", "grp", "label" FROM "s"."t"`
	cases := []struct {
		name string
		l    tile.Layout
		q    tile.BrowseQuery
		text string
		args []string
	}{
		{"first page orders by key", layout, tile.BrowseQuery{Limit: 10},
			cols + ` ORDER BY "id" ASC LIMIT 10`, nil},
		{"key only keyset uses plain comparison", layout, tile.BrowseQuery{Limit: 10, After: row("5", "", "x")},
			cols + ` WHERE ("id" > $1) ORDER BY "id" ASC LIMIT 10`, []string{"5"}},
		{"sort puts nulls last and adds key tiebreak", layout, tile.BrowseQuery{Limit: 10, Sort: []tile.SortKey{{Field: "grp", Desc: true}}},
			cols + ` ORDER BY ("grp" IS NULL) ASC, "grp" DESC, "id" ASC LIMIT 10`, nil},
		{"keyset on a nullable sort value", layout, tile.BrowseQuery{Limit: 10, Sort: []tile.SortKey{{Field: "grp"}}, After: row("5", "2", "x")},
			cols + ` WHERE (("grp" > $1 OR "grp" IS NULL) OR ("grp" = $2 AND ("id" > $3))) ORDER BY ("grp" IS NULL) ASC, "grp" ASC, "id" ASC LIMIT 10`,
			[]string{"2", "2", "5"}},
		{"keyset after a null sort value stays among nulls", layout, tile.BrowseQuery{Limit: 10, Sort: []tile.SortKey{{Field: "grp"}}, After: row("5", "", "x")},
			cols + ` WHERE ("grp" IS NULL AND ("id" > $1)) ORDER BY ("grp" IS NULL) ASC, "grp" ASC, "id" ASC LIMIT 10`,
			[]string{"5"}},
		{"descending key", layout, tile.BrowseQuery{Limit: 10, Sort: []tile.SortKey{{Field: "id", Desc: true}}, After: row("5", "", "x")},
			cols + ` WHERE ("id" < $1) ORDER BY "id" DESC LIMIT 10`, []string{"5"}},
		{"no key uses offset", tile.Layout{Fields: layout.Fields}, tile.BrowseQuery{Limit: 10, Offset: 30, After: row("5", "", "x")},
			cols + ` LIMIT 10 OFFSET 30`, nil},
		{"filters come before keyset", layout, tile.BrowseQuery{Limit: 10, After: row("5", "", "x"), Filter: []tile.Condition{
			{Field: "grp", Op: tile.OpGe, Value: tile.Text("1")},
			{Field: "label", Op: tile.OpContains, Value: tile.Text(`5%_\`)},
			{Field: "grp", Op: tile.OpNotNull},
		}},
			cols + ` WHERE "grp" >= $1 AND "label"::text ILIKE $2 AND "grp" IS NOT NULL AND ("id" > $3) ORDER BY "id" ASC LIMIT 10`,
			[]string{"1", `%5\%\_\\%`, "5"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, err := pg.Select(table, c.l, c.q)
			if err != nil {
				t.Fatal(err)
			}
			if req.Text != c.text {
				t.Fatalf("got  %s\nwant %s", req.Text, c.text)
			}
			if len(req.Args) != len(c.args) {
				t.Fatalf("args = %v, want %v", req.Args, c.args)
			}
			for i, a := range c.args {
				if string(req.Args[i].Raw) != a {
					t.Fatalf("arg %d = %q, want %q", i, req.Args[i].Raw, a)
				}
			}
		})
	}
}

func TestSelectRefusesUnknownFields(t *testing.T) {
	for name, q := range map[string]tile.BrowseQuery{
		"sort":   {Sort: []tile.SortKey{{Field: "nope"}}},
		"filter": {Filter: []tile.Condition{{Field: "nope", Op: tile.OpEq}}},
		"op":     {Filter: []tile.Condition{{Field: "id", Op: "; DROP"}}},
	} {
		if _, err := pg.Select(table, layout, q); err == nil {
			t.Fatalf("%s: want an error", name)
		}
	}
}

func TestSelectQuotesHostileNames(t *testing.T) {
	ref := tile.ObjectRef{}.Child("schema", `a"b`).Child("table", `c"; DROP TABLE x;--`)
	req, err := pg.Select(ref, tile.Layout{Fields: []tile.Field{{Name: `d"e`}}}, tile.BrowseQuery{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if want := `SELECT "d""e" FROM "a""b"."c""; DROP TABLE x;--" LIMIT 1`; req.Text != want {
		t.Fatalf("got %s", req.Text)
	}
}

func TestPlanGuardsEveryUpdate(t *testing.T) {
	plan, err := pg.Plan(table, layout, tile.ChangeSet{Changes: []tile.Change{{
		Key: []tile.Value{tile.Text("7")},
		Old: map[string]tile.Value{"label": tile.Text("old"), "grp": {Kind: tile.KindNull}},
		Set: map[string]tile.Value{"label": tile.Text("it's"), "grp": tile.Text("3")},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Begin.Text != "BEGIN" || plan.Commit.Text != "COMMIT" || plan.Rollback.Text != "ROLLBACK" || plan.Atomicity != tile.AtomicAll {
		t.Fatalf("plan = %+v", plan)
	}
	st := plan.Steps[0]
	want := `UPDATE "s"."t" SET "grp" = $1, "label" = $2 WHERE "id" = $3 AND "grp" IS NULL AND "label"::text = $4`
	if st.Request.Text != want {
		t.Fatalf("got  %s\nwant %s", st.Request.Text, want)
	}
	var args []string
	for _, a := range st.Request.Args {
		args = append(args, string(a.Raw))
	}
	if strings.Join(args, "|") != "3|it's|7|old" || st.ExpectRows != 1 {
		t.Fatalf("args = %v, expect = %d", args, st.ExpectRows)
	}
	wantPreview := `UPDATE "s"."t" SET "grp" = '3', "label" = 'it''s' WHERE "id" = '7' AND "grp" IS NULL AND "label"::text = 'old'`
	if st.Preview != wantPreview {
		t.Fatalf("preview = %s", st.Preview)
	}
}

func TestPlanRefuses(t *testing.T) {
	key := []tile.Value{tile.Text("7")}
	set := map[string]tile.Value{"label": tile.Text("x")}
	if _, err := pg.Plan(table, tile.Layout{Fields: layout.Fields, Note: "no key"}, tile.ChangeSet{Changes: []tile.Change{{Key: key, Set: set}}}); err == nil || !strings.Contains(err.Error(), "no key") {
		t.Fatalf("keyless: %v", err)
	}
	if _, err := pg.Plan(table, layout, tile.ChangeSet{Changes: []tile.Change{{Key: key, Set: set}}}); err == nil {
		t.Fatal("an update with no old value has no guard")
	}
	unknown := map[string]tile.Value{"nope": tile.Text("y")}
	if _, err := pg.Plan(table, layout, tile.ChangeSet{Changes: []tile.Change{{Key: key, Set: unknown, Old: unknown}}}); err == nil {
		t.Fatal("unknown field")
	}
}
