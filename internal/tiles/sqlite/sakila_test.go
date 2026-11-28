package sqlite

import (
	"context"
	"slices"
	"testing"

	"github.com/arunksoman/tessera/tile"
)

// TestSakila loads the vendored Sakila port through the tile's own splitter, which must keep
// its trigger bodies whole, then reads it back.
func TestSakila(t *testing.T) {
	if testing.Short() {
		t.Skip("loads 46,000 statements")
	}
	const dir = "../../../test/fixtures/sakila/sqlite/"
	c := openPath(t, Tile{}, fixtureDB(t, dir+"sqlite-sakila-schema.sql", dir+"sqlite-sakila-insert-data.sql"))
	s := newSession(t, c, tile.SessionOptions{ReadOnly: true})
	for table, want := range map[string]string{"actor": "200", "film": "1000", "rental": "16044", "payment": "16049", "film_list": "5462"} {
		if got := string(exec(t, s, `SELECT count(*) FROM `+table).rows[0][0].Raw); got != want {
			t.Errorf("%s has %s rows, want %s", table, got, want)
		}
	}
	main := tile.ObjectRef{}.Child("schema", "main")
	var views int
	for _, n := range allNodes(t, c.Tree(), main) {
		if n.Kind == "view" {
			views++
		}
	}
	if views != 5 { // the port leaves actor_info commented out
		t.Errorf("%d views in the tree", views)
	}

	ctx := context.Background()
	k, err := c.Tree().(tile.KeyDescriber).DescribeKeys(ctx, main.Child("table", "film_actor"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(k.Primary, []string{"actor_id", "film_id"}) || len(k.Foreign) != 2 {
		t.Fatalf("film_actor keys = %+v", k)
	}
	st, err := c.Tree().(tile.Designer).DescribeStructure(ctx, main.Child("table", "actor"))
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Triggers) != 2 || st.Triggers[0].Timing != "AFTER" || !slices.Equal(st.Triggers[0].Events, []string{"INSERT"}) {
		t.Fatalf("actor triggers = %+v", st.Triggers)
	}

	// Pages of a view, which has no key, by offset.
	b := c.(tile.Browser)
	l, err := b.Layout(ctx, main.Child("view", "film_list"))
	if err != nil {
		t.Fatal(err)
	}
	req, err := b.BrowseRequest(main.Child("view", "film_list"), l, tile.BrowseQuery{Offset: 5000, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(exec(t, s, req.Text, req.Args...).rows); n != 462 {
		t.Fatalf("last page has %d rows", n)
	}
}

func allNodes(t *testing.T, tr tile.ObjectTree, parent tile.ObjectRef) []tile.Node {
	t.Helper()
	var out []tile.Node
	cursor := ""
	for {
		nodes, next, err := tr.Children(context.Background(), parent, tile.Page{Cursor: cursor, Limit: 7})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, nodes...)
		if next == "" {
			return out
		}
		cursor = next
	}
}
