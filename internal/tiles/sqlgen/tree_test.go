package sqlgen

import (
	"slices"
	"strings"
	"testing"

	"github.com/arunksoman/tessera/tile"
)

// listStage serves sorted names as one stage, recording the keys it was asked to start after.
func listStage(kind string, names []string, asked *[]string) Stage {
	return func(after string, n int) ([]tile.Node, error) {
		*asked = append(*asked, kind+">"+after)
		var out []tile.Node
		for _, name := range names {
			if name > after && len(out) < n {
				out = append(out, tile.Node{Ref: tile.ObjectRef{}.Child(kind, name), Kind: kind, Label: name})
			}
		}
		return out, nil
	}
}

func TestPageStages(t *testing.T) {
	var asked []string
	stages := []Stage{
		listStage("table", []string{"a", "b", "c"}, &asked),
		listStage("view", nil, &asked),
		listStage("function", []string{"a(int)", "z()"}, &asked),
	}
	for _, limit := range []int{1, 2, 3, 4, 5, 6, 100} {
		var got []string
		cursor := ""
		for range 20 {
			nodes, next, err := PageStages(limit, cursor, stages...)
			if err != nil {
				t.Fatal(err)
			}
			if len(nodes) > limit {
				t.Fatalf("limit %d: %d nodes", limit, len(nodes))
			}
			for _, n := range nodes {
				got = append(got, n.Kind+" "+n.Label)
			}
			if next == "" {
				break
			}
			cursor = next
		}
		want := "table a,table b,table c,function a(int),function z()"
		if strings.Join(got, ",") != want {
			t.Errorf("limit %d: got %v", limit, got)
		}
	}

	// A cursor in a later stage does not ask earlier stages again.
	asked = nil
	_, next, _ := PageStages(1, "", stages...)
	asked = nil
	if _, _, err := PageStages(10, "2:a(int)", stages...); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(asked, []string{"function>a(int)"}) {
		t.Errorf("asked %v", asked)
	}
	if next != "0:a" {
		t.Errorf("next = %q", next)
	}

	// A cursor from before stages, a bare name, continues the first stage.
	nodes, _, _ := PageStages(10, "a", stages...)
	if len(nodes) != 4 || nodes[0].Label != "b" {
		t.Errorf("bare cursor: %+v", nodes)
	}
}

func TestPageSlice(t *testing.T) {
	nodes := make([]tile.Node, 5)
	for i := range nodes {
		nodes[i].Label = string(rune('a' + i))
	}
	var got []string
	cursor := ""
	for range 10 {
		page, next, _ := PageSlice(nodes, tile.Page{Cursor: cursor, Limit: 2})
		for _, n := range page {
			got = append(got, n.Label)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if strings.Join(got, "") != "abcde" {
		t.Fatalf("got %v", got)
	}
	if page, next, _ := PageSlice(nodes, tile.Page{Cursor: "99", Limit: 2}); len(page) != 0 || next != "" {
		t.Fatalf("past the end: %v %q", page, next)
	}
}

func TestAddForeign(t *testing.T) {
	var fks []tile.ForeignKey
	fks = AddForeign(fks, "a_fk", "x", "s", "t", "id")
	fks = AddForeign(fks, "b_fk", "y1", "s2", "u", "k1")
	fks = AddForeign(fks, "b_fk", "y2", "s2", "u", "k2")
	if len(fks) != 2 || !slices.Equal(fks[1].Fields, []string{"y1", "y2"}) || !slices.Equal(fks[1].TargetFields, []string{"k1", "k2"}) {
		t.Fatalf("fks = %+v", fks)
	}
	if p := fks[1].Target.Path; len(p) != 2 || p[0] != (tile.Segment{Kind: "schema", Name: "s2"}) || p[1] != (tile.Segment{Kind: "table", Name: "u"}) {
		t.Fatalf("target = %+v", p)
	}
}
