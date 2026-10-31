package ioformat

import (
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"

	"github.com/arunksoman/tessera/tile"
)

var fields = []tile.Field{
	{Name: "id", Kind: tile.KindInt},
	{Name: "total", Kind: tile.KindDecimal},
	{Name: "name", Kind: tile.KindText},
	{Name: "ok", Kind: tile.KindBool},
}

func cells(id, total string, name *string, ok string) []tile.Value {
	row := []tile.Value{
		{Kind: tile.KindInt, Raw: []byte(id)},
		{Kind: tile.KindDecimal, Raw: []byte(total)},
		{Kind: tile.KindNull},
		{Kind: tile.KindBool, Raw: []byte(ok)},
	}
	if name != nil {
		row[2] = tile.Text(*name)
	}
	return row
}

func write(t *testing.T, w Writer, fields []tile.Field, rows ...[]tile.Value) {
	t.Helper()
	if err := w.Begin(fields); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if err := w.Row(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.End(); err != nil {
		t.Fatal(err)
	}
}

func ptr(s string) *string { return &s }

func TestCSV(t *testing.T) {
	var b strings.Builder
	write(t, NewCSV(&b, true), fields, cells("9007199254740993", "12345678901234.5678", ptr("a,b\"c"), "t"), cells("-5", "-1.5", nil, "f"))
	recs, err := csv.NewReader(strings.NewReader(b.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(recs[0], ",") != "id,total,name,ok" {
		t.Fatalf("header = %q", recs[0])
	}
	if recs[1][0] != "9007199254740993" || recs[1][1] != "12345678901234.5678" || recs[1][2] != `a,b"c` {
		t.Fatalf("row = %q", recs[1])
	}
	if recs[2][0] != "-5" || recs[2][1] != "-1.5" || recs[2][2] != "" {
		t.Fatalf("negative numbers must not be neutralized: %q", recs[2])
	}
}

func TestCSVNeutralizesFormulas(t *testing.T) {
	for _, in := range []string{"=1+1", "+cmd", "-cmd", "@SUM(A1)", "\tx", "\rx"} {
		var b strings.Builder
		write(t, NewCSV(&b, true), []tile.Field{{Name: "a", Kind: tile.KindText}}, []tile.Value{tile.Text(in)})
		recs, err := csv.NewReader(strings.NewReader(b.String())).ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(recs[1][0], "'") {
			t.Errorf("%q not neutralized: %q", in, recs[1][0])
		}
	}
	// A hostile column name is a cell too.
	var b strings.Builder
	write(t, NewCSV(&b, true), []tile.Field{{Name: "=evil()", Kind: tile.KindText}})
	if !strings.HasPrefix(b.String(), "'=evil()") {
		t.Errorf("header not neutralized: %q", b.String())
	}
	b.Reset()
	write(t, NewCSV(&b, false), []tile.Field{{Name: "a", Kind: tile.KindText}}, []tile.Value{tile.Text("=1+1")})
	if !strings.Contains(b.String(), "=1+1") || strings.Contains(b.String(), "'=") {
		t.Errorf("raw mode changed the value: %q", b.String())
	}
}

func TestJSONIsValidAndLossless(t *testing.T) {
	var b strings.Builder
	write(t, NewJSON(&b), fields, cells("9007199254740993", "12345678901234.5678", ptr("q\"x\n"), "t"), cells("1", "NaN", nil, "0"))
	dec := json.NewDecoder(strings.NewReader(b.String()))
	dec.UseNumber()
	var out []map[string]any
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("%v in %s", err, b.String())
	}
	if out[0]["id"].(json.Number).String() != "9007199254740993" {
		t.Fatalf("int changed: %v", out[0]["id"])
	}
	if out[0]["total"].(json.Number).String() != "12345678901234.5678" {
		t.Fatalf("decimal changed: %v", out[0]["total"])
	}
	if out[0]["name"] != "q\"x\n" || out[0]["ok"] != true {
		t.Fatalf("%+v", out[0])
	}
	if out[1]["name"] != nil || out[1]["ok"] != false || out[1]["total"] != "NaN" {
		t.Fatalf("%+v", out[1])
	}
}

func TestJSONEmptyAndSpecialKinds(t *testing.T) {
	var b strings.Builder
	write(t, NewJSON(&b), fields)
	if strings.TrimSpace(b.String()) != "[]" {
		t.Fatalf("empty = %q", b.String())
	}
	b.Reset()
	write(t, NewJSON(&b), []tile.Field{{Name: "j", Kind: tile.KindJSON}, {Name: "bad", Kind: tile.KindJSON}, {Name: "bin", Kind: tile.KindBytes}},
		[]tile.Value{{Kind: tile.KindJSON, Raw: []byte(`{"a":1}`)}, {Kind: tile.KindJSON, Raw: []byte(`{oops`)}, {Kind: tile.KindBytes, Raw: []byte{0xff, 0x00}}})
	var out []map[string]any
	if err := json.Unmarshal([]byte(b.String()), &out); err != nil {
		t.Fatalf("%v in %s", err, b.String())
	}
	if out[0]["j"].(map[string]any)["a"] != float64(1) || out[0]["bad"] != "{oops" || out[0]["bin"] != "/wA=" {
		t.Fatalf("%+v", out[0])
	}
}

func TestTable(t *testing.T) {
	var b strings.Builder
	write(t, NewTable(&b), []tile.Field{{Name: "id", Kind: tile.KindInt}, {Name: "name", Kind: tile.KindText}},
		[]tile.Value{{Kind: tile.KindInt, Raw: []byte("1")}, tile.Text("Ada")},
		[]tile.Value{{Kind: tile.KindInt, Raw: []byte("22")}, {Kind: tile.KindNull}})
	want := " id | name\n----+-----\n  1 | Ada\n 22 | NULL\n(2 rows)\n"
	if b.String() != want {
		t.Fatalf("got:\n%s\nwant:\n%s", b.String(), want)
	}
}

func TestTableStripsTerminalEscapes(t *testing.T) {
	var b strings.Builder
	write(t, NewTable(&b), []tile.Field{{Name: "a\x1b[31m", Kind: tile.KindText}},
		[]tile.Value{tile.Text("x\x1b]0;pwned\x07\ny\tz\x9b31m")})
	out := b.String()
	for _, bad := range []string{"\x1b", "\x07", "\x9b"} {
		if strings.Contains(out, bad) {
			t.Fatalf("control character %q reached the terminal: %q", bad, out)
		}
	}
	if lines := strings.Count(out, "\n"); lines != 4 {
		t.Fatalf("a newline in a value must not add a row: %q", out)
	}
}

func TestTableShowsBinarySize(t *testing.T) {
	var b strings.Builder
	write(t, NewTable(&b), []tile.Field{{Name: "b", Kind: tile.KindBytes}}, []tile.Value{{Kind: tile.KindBytes, Raw: []byte{0xff, 0, 1}}})
	if !strings.Contains(b.String(), "<3 bytes>") {
		t.Fatalf("%q", b.String())
	}
}

func TestTableStreamsPastItsSizingWindow(t *testing.T) {
	var b strings.Builder
	w := NewTable(&b)
	if err := w.Begin([]tile.Field{{Name: "n", Kind: tile.KindText}}); err != nil {
		t.Fatal(err)
	}
	for range sizingRows + 5 {
		if err := w.Row([]tile.Value{tile.Text("x")}); err != nil {
			t.Fatal(err)
		}
	}
	if b.Len() == 0 {
		t.Fatal("nothing written after the sizing window, so the table buffers everything")
	}
	if err := w.End(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "(1005 rows)") {
		t.Fatalf("footer: %q", b.String()[len(b.String())-30:])
	}
}

func TestRowCopiesSoRawCanBeReused(t *testing.T) {
	var b strings.Builder
	w := NewTable(&b)
	w.Begin([]tile.Field{{Name: "n", Kind: tile.KindText}})
	raw := []byte("first")
	w.Row([]tile.Value{{Kind: tile.KindText, Raw: raw}})
	copy(raw, "XXXXX")
	w.End()
	if !strings.Contains(b.String(), "first") {
		t.Fatalf("buffered row aliased driver memory: %q", b.String())
	}
}
