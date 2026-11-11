package plugin

import (
	"encoding/json"
	"testing"

	"github.com/arunksoman/tessera/tile"
)

// TestWireNames pins the JSON names plugins see. A failure here means a protocol change:
// bump tile.APIVersion or the protocol revision rather than updating the golden text.
func TestWireNames(t *testing.T) {
	cases := []struct {
		v    any
		want string
	}{
		{tile.Frame{Schema: &tile.ResultSchema{Shape: tile.ShapeKeyValue, Fields: []tile.Field{{Name: "k", NativeType: "string", Kind: tile.KindText}}}},
			`{"schema":{"shape":3,"fields":[{"name":"k","native_type":"string","kind":5}]}}`},
		{tile.Frame{Batch: &tile.Batch{Rows: 1, Cells: []tile.Value{{Kind: tile.KindBytes, Raw: []byte{0xff}}}}},
			`{"batch":{"rows":1,"cells":[{"kind":6,"raw":"/w=="}]}}`},
		{tile.Frame{Done: &tile.ResultSummary{RowsAffected: 2, HasAffected: true, Tag: "OK", Cursor: "17"}},
			`{"done":{"rows_affected":2,"has_affected":true,"tag":"OK","cursor":"17"}}`},
		{tile.Classification{Verb: "MULTI", ReadOnly: true, Tx: tile.TxBegin},
			`{"verb":"MULTI","risk":0,"read_only":true,"tx":1}`},
		{tile.BrowseQuery{Cursor: "c", Limit: 10, Filter: []tile.Condition{{Field: "a", Op: tile.OpEq, Value: tile.Text("x")}}},
			`{"filter":[{"field":"a","op":"=","value":{"kind":5,"raw":"eA=="}}],"cursor":"c","limit":10}`},
		{tile.Layout{Fields: []tile.Field{}, Key: []string{"k"}, Estimate: -1, Paging: tile.PagingCursor, Shape: tile.ShapeKeyValue},
			`{"fields":[],"key":["k"],"estimate":-1,"paging":1,"shape":3}`},
		{tile.Manifest{ID: "redis", Name: "Redis", Version: "0.1.0", MinTess: "0.4.0"},
			`{"id":"redis","name":"Redis","api_version":0,"style":{},"conn_form":{"fields":null},"version":"0.1.0","min_tess":"0.4.0"}`},
		{tile.ObjectRef{}.Child("db", "0"), `{"path":[{"kind":"db","name":"0"}]}`},
	}
	for _, c := range cases {
		b, err := json.Marshal(c.v)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != c.want {
			t.Errorf("%T:\n got %s\nwant %s", c.v, b, c.want)
		}
	}
}
