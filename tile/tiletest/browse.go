package tiletest

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/arunksoman/tessera/tile"
)

const browseRows = 25

// browseTable creates tt_browse: id 1..25, grp cycling 0, 1, NULL, and a label.
func browseTable(t *testing.T, s tile.Session) tile.ObjectRef {
	t.Helper()
	exec := func(text string) { mustCollect(t, s, text) }
	exec(`DROP TABLE IF EXISTS tt_browse`)
	exec(`CREATE TABLE tt_browse (id BIGINT PRIMARY KEY, grp INT, label VARCHAR(50))`)
	t.Cleanup(func() { exec(`DROP TABLE IF EXISTS tt_browse`) })
	var vals []string
	for i := 1; i <= browseRows; i++ {
		grp := strconv.Itoa(i % 3)
		if i%3 == 2 {
			grp = "NULL"
		}
		vals = append(vals, fmt.Sprintf("(%d, %s, 'item %d')", i, grp, i))
	}
	exec(`INSERT INTO tt_browse (id, grp, label) VALUES ` + strings.Join(vals, ", "))
	return tile.ObjectRef{}
}

func browser(t *testing.T, c tile.Conn) tile.Browser {
	t.Helper()
	b, ok := c.(tile.Browser)
	if !ok {
		t.Fatal("the tile has no Browser facet")
	}
	return b
}

func layoutOf(t *testing.T, c tile.Conn, ref tile.ObjectRef) tile.Layout {
	t.Helper()
	l, err := browser(t, c).Layout(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// browseAll reads every page, and fails if paging repeats itself forever.
func browseAll(t *testing.T, c tile.Conn, s tile.Session, ref tile.ObjectRef, l tile.Layout, q tile.BrowseQuery) [][]tile.Value {
	t.Helper()
	q.Limit = 7
	var out [][]tile.Value
	for range browseRows + 2 {
		req, err := browser(t, c).BrowseRequest(ref, l, q)
		if err != nil {
			t.Fatal(err)
		}
		page := mustCollect(t, s, req.Text, req.Args...)
		out = append(out, page.rows...)
		if len(page.rows) < q.Limit {
			return out
		}
		q.After = page.rows[len(page.rows)-1]
		q.Offset = int64(len(out))
	}
	t.Fatalf("paging did not end: %d rows so far", len(out))
	return nil
}

func column(rows [][]tile.Value, i int) []string {
	out := make([]string, len(rows))
	for r, row := range rows {
		out[r] = "NULL"
		if row[i].Kind != tile.KindNull {
			out[r] = string(row[i].Raw)
		}
	}
	return out
}

func testBrowseLayout(t *testing.T, h Harness) {
	c := h.Open(t)
	l := layoutOf(t, c, h.Schema.Child("table", "orders"))
	var names []string
	for _, f := range l.Fields {
		names = append(names, f.Name)
	}
	if strings.Join(names, ",") != "id,customer_id,total,placed_at" {
		t.Fatalf("fields = %v", names)
	}
	if !slices.Equal(l.Key, []string{"id"}) {
		t.Fatalf("key = %v, note = %q", l.Key, l.Note)
	}
	if _, err := browser(t, c).Layout(context.Background(), h.Schema.Child("table", "no_such_table")); err == nil {
		t.Fatal("want an error for a missing table")
	}
}

func testBrowsePaging(t *testing.T, h Harness) {
	c := h.Open(t)
	s := session(t, c, tile.SessionOptions{})
	browseTable(t, s)
	ref := h.Schema.Child("table", "tt_browse")
	l := layoutOf(t, c, ref)

	ascending := func(rows [][]tile.Value) {
		t.Helper()
		ids := column(rows, 0)
		if len(ids) != browseRows {
			t.Fatalf("got %d rows: %v", len(ids), ids)
		}
		seen := map[string]bool{}
		for _, id := range ids {
			if seen[id] {
				t.Fatalf("row %s repeated: %v", id, ids)
			}
			seen[id] = true
		}
	}
	rows := browseAll(t, c, s, ref, l, tile.BrowseQuery{})
	ascending(rows)
	if column(rows, 0)[0] != "1" || column(rows, 0)[24] != "25" {
		t.Fatalf("not in key order: %v", column(rows, 0))
	}

	for _, desc := range []bool{false, true} {
		rows = browseAll(t, c, s, ref, l, tile.BrowseQuery{Sort: []tile.SortKey{{Field: "grp", Desc: desc}}})
		ascending(rows)
		grps := column(rows, 1)
		// Sorted, with NULLs last in both directions and on both dialects.
		first := "0"
		if desc {
			first = "1"
		}
		if grps[0] != first || grps[len(grps)-1] != "NULL" {
			t.Fatalf("desc=%v grps = %v", desc, grps)
		}
		for i := 1; i < len(grps); i++ {
			if grps[i-1] == "NULL" && grps[i] != "NULL" {
				t.Fatalf("NULL before a value: %v", grps)
			}
		}
	}
}

func testBrowseFilter(t *testing.T, h Harness) {
	c := h.Open(t)
	s := session(t, c, tile.SessionOptions{})
	browseTable(t, s)
	ref := h.Schema.Child("table", "tt_browse")
	l := layoutOf(t, c, ref)
	count := func(conds ...tile.Condition) int {
		return len(browseAll(t, c, s, ref, l, tile.BrowseQuery{Filter: conds}))
	}
	cases := []struct {
		name string
		c    tile.Condition
		want int
	}{
		{"eq", tile.Condition{Field: "grp", Op: tile.OpEq, Value: tile.Text("1")}, 9},
		{"ne", tile.Condition{Field: "grp", Op: tile.OpNe, Value: tile.Text("1")}, 8},
		{"gt", tile.Condition{Field: "id", Op: tile.OpGt, Value: tile.Text("20")}, 5},
		{"le", tile.Condition{Field: "id", Op: tile.OpLe, Value: tile.Text("3")}, 3},
		{"null", tile.Condition{Field: "grp", Op: tile.OpNull}, 8},
		{"not null", tile.Condition{Field: "grp", Op: tile.OpNotNull}, 17},
		{"contains ignores case", tile.Condition{Field: "label", Op: tile.OpContains, Value: tile.Text("ITEM 2")}, 7},
		{"contains escapes wildcards", tile.Condition{Field: "label", Op: tile.OpContains, Value: tile.Text("%")}, 0},
		{"value cannot inject", tile.Condition{Field: "label", Op: tile.OpEq, Value: tile.Text(`x' OR '1'='1`)}, 0},
	}
	for _, tc := range cases {
		if got := count(tc.c); got != tc.want {
			t.Errorf("%s: got %d rows, want %d", tc.name, got, tc.want)
		}
	}
}

func testBrowseKeyless(t *testing.T, h Harness) {
	c := h.Open(t)
	s := session(t, c, tile.SessionOptions{})
	mustCollect(t, s, `DROP TABLE IF EXISTS tt_keyless`)
	mustCollect(t, s, `CREATE TABLE tt_keyless (id BIGINT, label VARCHAR(50))`)
	t.Cleanup(func() { mustCollect(t, s, `DROP TABLE IF EXISTS tt_keyless`) })
	var vals []string
	for i := 1; i <= 10; i++ {
		vals = append(vals, fmt.Sprintf("(%d, 'x')", i))
	}
	mustCollect(t, s, `INSERT INTO tt_keyless (id, label) VALUES `+strings.Join(vals, ", "))

	ref := h.Schema.Child("table", "tt_keyless")
	l := layoutOf(t, c, ref)
	if len(l.Key) != 0 || l.Note == "" {
		t.Fatalf("key = %v, note = %q: a keyless table must explain why it is read-only", l.Key, l.Note)
	}
	if rows := browseAll(t, c, s, ref, l, tile.BrowseQuery{}); len(rows) != 10 {
		t.Fatalf("rows = %d", len(rows))
	}
	if _, err := c.(tile.Editor).Plan(ref, l, tile.ChangeSet{Changes: []tile.Change{{
		Set: map[string]tile.Value{"label": tile.Text("y")}, Old: map[string]tile.Value{"label": tile.Text("x")},
	}}}); err == nil {
		t.Fatal("a keyless table must not be editable")
	}
}

func testBrowseHostile(t *testing.T, h Harness) {
	c := h.Open(t)
	s := session(t, c, tile.SessionOptions{})
	name := h.HostileTable(t, s)
	ref := h.Schema.Child("table", name)
	if rows := browseAll(t, c, s, ref, layoutOf(t, c, ref), tile.BrowseQuery{}); len(rows) != 0 {
		t.Fatalf("rows = %d", len(rows))
	}
	if len(mustCollect(t, s, `SELECT id FROM orders`).rows) == 0 {
		t.Fatal("orders was damaged")
	}
}

func planFor(t *testing.T, c tile.Conn, ref tile.ObjectRef, l tile.Layout, changes ...tile.Change) tile.Plan {
	t.Helper()
	e, ok := c.(tile.Editor)
	if !ok {
		t.Fatal("the tile has no Editor facet")
	}
	p, err := e.Plan(ref, l, tile.ChangeSet{Changes: changes})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func labelOf(t *testing.T, s tile.Session, id string) string {
	t.Helper()
	rows := mustCollect(t, s, `SELECT label FROM tt_browse WHERE id = `+id).rows
	return column(rows, 0)[0]
}

func testPlanPurity(t *testing.T, h Harness) {
	c := h.Open(t)
	s := session(t, c, tile.SessionOptions{})
	browseTable(t, s)
	ref := h.Schema.Child("table", "tt_browse")
	p := planFor(t, c, ref, layoutOf(t, c, ref), tile.Change{
		Key: []tile.Value{tile.Text("1")},
		Old: map[string]tile.Value{"label": tile.Text("item 1")},
		Set: map[string]tile.Value{"label": tile.Text("planned")},
	})
	if len(p.Steps) != 1 || p.Steps[0].Preview == "" || p.Steps[0].ExpectRows != 1 {
		t.Fatalf("plan = %+v", p)
	}
	if got := labelOf(t, s, "1"); got != "item 1" {
		t.Fatalf("planning changed the data: %q", got)
	}
}

func testPlanGuards(t *testing.T, h Harness) {
	c := h.Open(t)
	s := session(t, c, tile.SessionOptions{})
	browseTable(t, s)
	ref := h.Schema.Child("table", "tt_browse")
	l := layoutOf(t, c, ref)
	apply := func(change tile.Change) int64 {
		t.Helper()
		st := planFor(t, c, ref, l, change).Steps[0]
		res := mustCollect(t, s, st.Request.Text, st.Request.Args...)
		if res.done == nil || !res.done.HasAffected {
			t.Fatalf("done = %+v", res.done)
		}
		return res.done.RowsAffected
	}
	change := func(id, field string, old, set tile.Value) tile.Change {
		return tile.Change{
			Key: []tile.Value{tile.Text(id)},
			Old: map[string]tile.Value{field: old},
			Set: map[string]tile.Value{field: set},
		}
	}
	null := tile.Value{Kind: tile.KindNull}

	if n := apply(change("1", "label", tile.Text("item 1"), tile.Text("it's new"))); n != 1 {
		t.Fatalf("guard matched %d rows", n)
	}
	if got := labelOf(t, s, "1"); got != "it's new" {
		t.Fatalf("label = %q", got)
	}
	// Someone else changed the record: the old value no longer matches.
	if n := apply(change("1", "label", tile.Text("item 1"), tile.Text("overwrite"))); n != 0 {
		t.Fatalf("a stale guard matched %d rows", n)
	}
	if got := labelOf(t, s, "1"); got != "it's new" {
		t.Fatalf("a stale update overwrote the record: %q", got)
	}
	// Setting the same value is not a conflict, which needs clientFoundRows on MySQL.
	if n := apply(change("1", "label", tile.Text("it's new"), tile.Text("it's new"))); n != 1 {
		t.Fatalf("same-value update matched %d rows", n)
	}
	// NULL guards and NULL values.
	if n := apply(change("2", "grp", null, tile.Text("9"))); n != 1 {
		t.Fatalf("NULL guard matched %d rows", n)
	}
	if n := apply(change("2", "grp", tile.Text("9"), null)); n != 1 {
		t.Fatalf("set NULL matched %d rows", n)
	}
	if got := column(mustCollect(t, s, `SELECT grp FROM tt_browse WHERE id = 2`).rows, 0)[0]; got != "NULL" {
		t.Fatalf("grp = %q", got)
	}
}

func testExplain(t *testing.T, h Harness) {
	c := h.Open(t)
	s := session(t, c, tile.SessionOptions{ReadOnly: true})
	e, ok := c.(tile.Explainer)
	if !ok {
		t.Fatal("the tile has no Explainer facet")
	}
	const stmt = `SELECT id FROM orders WHERE id = 1`
	if res := mustCollect(t, s, e.ExplainRequest(stmt, tile.ExplainText).Text); len(res.rows) == 0 {
		t.Fatal("no text plan")
	}
	res := mustCollect(t, s, e.ExplainRequest(stmt, tile.ExplainJSON).Text)
	if len(res.rows) != 1 || !json.Valid(res.rows[0][0].Raw) {
		t.Fatalf("plan is not JSON: %+v", res.rows)
	}
	// Planning a write must not run it, even on a writable session.
	rw := session(t, c, tile.SessionOptions{})
	browseTable(t, rw)
	mustCollect(t, rw, e.ExplainRequest(`DELETE FROM tt_browse WHERE id = 1`, tile.ExplainText).Text)
	if got := labelOf(t, rw, "1"); got != "item 1" {
		t.Fatalf("explain ran the statement: %q", got)
	}
}

func testSessionPing(t *testing.T, h Harness) {
	s := session(t, h.Open(t), tile.SessionOptions{})
	p, ok := s.(tile.Pinger)
	if !ok {
		t.Fatal("the session has no Pinger facet")
	}
	if err := p.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Ping(context.Background()); err == nil {
		t.Fatal("a closed session must not answer a ping")
	}
}
