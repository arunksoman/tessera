package meta

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/arunksoman/tessera/tile"
)

type fakeTree struct {
	children, describes int
	fail                bool
}

func (f *fakeTree) Children(_ context.Context, parent tile.ObjectRef, _ tile.Page) ([]tile.Node, string, error) {
	f.children++
	if f.fail {
		return nil, "", errors.New("down")
	}
	if len(parent.Path) == 0 {
		return []tile.Node{{Ref: parent.Child("schema", "s"), Label: "s", HasChildren: true}}, "", nil
	}
	return []tile.Node{
		{Ref: parent.Child("table", "orders"), Label: "orders", Browsable: true},
		{Ref: parent.Child("table", "users"), Label: "users", Browsable: true},
	}, "", nil
}

func (f *fakeTree) Describe(_ context.Context, ref tile.ObjectRef) (tile.ObjectDetail, error) {
	f.describes++
	return tile.ObjectDetail{Sections: []tile.DetailSection{{Title: "Columns", Rows: [][]tile.Value{{tile.Text("id")}, {tile.Text(ref.Path[1].Name + "_col")}}}}}, nil
}

func TestCachesUntilTTLOrInvalidate(t *testing.T) {
	f := &fakeTree{}
	c := New(f, time.Minute)
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }
	cat := c.Catalog(context.Background())

	if got := cat.Tables(); len(got) != 2 || got[0] != (tile.ObjectName{Schema: "s", Name: "orders"}) {
		t.Fatalf("tables = %v", got)
	}
	loads := f.children
	cat.Tables()
	if f.children != loads {
		t.Fatal("second read should come from the cache")
	}
	if got := cat.Columns(tile.ObjectName{Name: "users"}); !slices.Equal(got, []string{"id", "users_col"}) {
		t.Fatalf("columns = %v", got)
	}
	cat.Columns(tile.ObjectName{Schema: "s", Name: "users"})
	if f.describes != 1 {
		t.Fatalf("describes = %d", f.describes)
	}
	if cat.Columns(tile.ObjectName{Name: "nope"}) != nil {
		t.Fatal("unknown table has no columns")
	}

	now = now.Add(2 * time.Minute)
	cat.Tables()
	if f.children == loads {
		t.Fatal("TTL did not expire")
	}
	loads = f.children
	c.Invalidate()
	cat.Tables()
	if f.children == loads {
		t.Fatal("Invalidate did not drop tables")
	}
}

func TestLoadFailureReadsAsEmptyAndIsNotCached(t *testing.T) {
	f := &fakeTree{fail: true}
	c := New(f, time.Minute)
	cat := c.Catalog(context.Background())
	if got := cat.Tables(); len(got) != 0 {
		t.Fatalf("tables = %v", got)
	}
	f.fail = false
	if got := cat.Tables(); len(got) != 2 {
		t.Fatalf("a failure must not be cached: %v", got)
	}
}
