package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/arunksoman/tessera/tile"
)

// Cell is one value on the wire: null, a string, or {"b64": ...} for bytes that are not
// UTF-8. Values stay text, so integers and decimals never pass through a JavaScript number.
type Cell struct {
	Null  bool
	Text  string
	Bytes []byte // set when the value is not valid UTF-8
}

func encodeCell(v tile.Value) Cell {
	switch {
	case v.Kind == tile.KindNull:
		return Cell{Null: true}
	case utf8.Valid(v.Raw):
		return Cell{Text: string(v.Raw)}
	}
	return Cell{Bytes: append([]byte(nil), v.Raw...)}
}

func (c Cell) MarshalJSON() ([]byte, error) {
	switch {
	case c.Null:
		return []byte("null"), nil
	case c.Bytes != nil:
		return json.Marshal(map[string]string{"b64": base64.StdEncoding.EncodeToString(c.Bytes)})
	}
	return json.Marshal(c.Text)
}

func (c *Cell) UnmarshalJSON(b []byte) error {
	*c = Cell{}
	if string(b) == "null" {
		c.Null = true
		return nil
	}
	if len(b) > 0 && b[0] == '{' {
		var o struct {
			B64 *string `json:"b64"`
		}
		if err := json.Unmarshal(b, &o); err != nil || o.B64 == nil {
			return fmt.Errorf("a value is a string, null, or {\"b64\": ...}")
		}
		raw, err := base64.StdEncoding.DecodeString(*o.B64)
		if err != nil {
			return fmt.Errorf("bad base64 value: %w", err)
		}
		c.Bytes = raw
		if c.Bytes == nil {
			c.Bytes = []byte{}
		}
		return nil
	}
	return json.Unmarshal(b, &c.Text)
}

// value turns a cell back into a tile value of the field's kind. A layout may leave kinds
// unset, and the zero kind means NULL, so a value of unknown kind is sent as text.
func (c Cell) value(kind tile.ValueKind) tile.Value {
	if kind == tile.KindNull {
		kind = tile.KindText
	}
	switch {
	case c.Null:
		return tile.Value{Kind: tile.KindNull}
	case c.Bytes != nil:
		return tile.Value{Kind: kind, Raw: c.Bytes}
	}
	return tile.Value{Kind: kind, Raw: []byte(c.Text)}
}

var kindNames = map[tile.ValueKind]string{
	tile.KindNull: "null", tile.KindBool: "bool", tile.KindInt: "int", tile.KindDecimal: "decimal",
	tile.KindFloat: "float", tile.KindText: "text", tile.KindBytes: "bytes", tile.KindTime: "time",
	tile.KindJSON: "json", tile.KindOther: "other",
}

type fieldJSON struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
	Kind string `json:"kind"`
}

func encodeFields(fs []tile.Field) []fieldJSON {
	out := make([]fieldJSON, len(fs))
	for i, f := range fs {
		out[i] = fieldJSON{Name: f.Name, Type: f.NativeType, Kind: kindNames[f.Kind]}
	}
	return out
}

// encodeRows copies a batch, since its bytes are valid only until the next frame.
func encodeRows(b *tile.Batch) [][]Cell {
	if b.Rows == 0 {
		return [][]Cell{}
	}
	width := len(b.Cells) / b.Rows
	rows := make([][]Cell, b.Rows)
	for r := range rows {
		row := make([]Cell, width)
		for c := range row {
			row[c] = encodeCell(b.Cells[r*width+c])
		}
		rows[r] = row
	}
	return rows
}

type segmentJSON struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type refJSON []segmentJSON

func (r refJSON) ref() tile.ObjectRef {
	out := tile.ObjectRef{Path: make([]tile.Segment, len(r))}
	for i, s := range r {
		out.Path[i] = tile.Segment{Kind: s.Kind, Name: s.Name}
	}
	return out
}

func encodeRef(r tile.ObjectRef) refJSON {
	out := make(refJSON, len(r.Path))
	for i, s := range r.Path {
		out[i] = segmentJSON{s.Kind, s.Name}
	}
	return out
}
