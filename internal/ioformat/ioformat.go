// Package ioformat writes result streams as CSV, JSON, or terminal tables.
// Every writer streams, so an export never holds a whole result in memory.
package ioformat

import (
	"bytes"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/arunksoman/tessera/tile"
)

// Writer receives one result. Cells' Raw bytes are valid only during the Row call.
type Writer interface {
	Begin(fields []tile.Field) error
	Row(cells []tile.Value) error
	End() error
}

// CSV

type csvWriter struct {
	w          *csv.Writer
	neutralize bool
	rec        []string
}

// NewCSV writes CSV. With neutralize, text cells that a spreadsheet would run as a formula
// get a leading quote. Database content is untrusted, so exports should keep this on.
func NewCSV(w io.Writer, neutralize bool) Writer {
	return &csvWriter{w: csv.NewWriter(w), neutralize: neutralize}
}

func (c *csvWriter) safe(s string) string {
	if c.neutralize && s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

func (c *csvWriter) Begin(fields []tile.Field) error {
	c.rec = make([]string, len(fields))
	for i, f := range fields {
		c.rec[i] = c.safe(f.Name)
	}
	return c.w.Write(c.rec)
}

func (c *csvWriter) Row(cells []tile.Value) error {
	for i, v := range cells {
		switch v.Kind {
		case tile.KindNull:
			c.rec[i] = ""
		case tile.KindInt, tile.KindDecimal, tile.KindFloat, tile.KindBool, tile.KindTime:
			c.rec[i] = string(v.Raw) // numbers like -5 are not formulas
		case tile.KindBytes:
			c.rec[i] = base64.StdEncoding.EncodeToString(v.Raw)
		default:
			c.rec[i] = c.safe(string(v.Raw))
		}
	}
	return c.w.Write(c.rec)
}

func (c *csvWriter) End() error {
	c.w.Flush()
	return c.w.Error()
}

// JSON

type jsonWriter struct {
	w     io.Writer
	names [][]byte
	rows  int
	buf   bytes.Buffer
}

// NewJSON writes a JSON array of objects. Integers and decimals stay exact JSON numbers.
func NewJSON(w io.Writer) Writer { return &jsonWriter{w: w} }

func (j *jsonWriter) Begin(fields []tile.Field) error {
	j.names = make([][]byte, len(fields))
	for i, f := range fields {
		n, err := json.Marshal(f.Name)
		if err != nil {
			return err
		}
		j.names[i] = n
	}
	_, err := io.WriteString(j.w, "[")
	return err
}

func (j *jsonWriter) Row(cells []tile.Value) error {
	j.buf.Reset()
	if j.rows > 0 {
		j.buf.WriteByte(',')
	}
	j.buf.WriteString("\n{")
	j.rows++
	for i, v := range cells {
		if i > 0 {
			j.buf.WriteByte(',')
		}
		j.buf.Write(j.names[i])
		j.buf.WriteByte(':')
		j.buf.Write(jsonValue(v))
	}
	j.buf.WriteByte('}')
	_, err := j.w.Write(j.buf.Bytes())
	return err
}

func (j *jsonWriter) End() error {
	end := "]\n"
	if j.rows > 0 {
		end = "\n]\n"
	}
	_, err := io.WriteString(j.w, end)
	return err
}

func jsonValue(v tile.Value) []byte {
	switch v.Kind {
	case tile.KindNull:
		return []byte("null")
	case tile.KindInt, tile.KindDecimal, tile.KindFloat, tile.KindJSON:
		// NaN, Infinity, and invalid JSON fall through to a string.
		if json.Valid(v.Raw) {
			return v.Raw
		}
	case tile.KindBool:
		switch string(v.Raw) { // PostgreSQL text format says t/f, MySQL says 1/0
		case "t", "true", "1":
			return []byte("true")
		case "f", "false", "0":
			return []byte("false")
		}
	case tile.KindBytes:
		out, _ := json.Marshal(base64.StdEncoding.EncodeToString(v.Raw))
		return out
	}
	out, _ := json.Marshal(string(v.Raw))
	return out
}

// Table

const (
	sizingRows = 1000 // column widths come from this many rows, then the table streams
	maxWidth   = 60
)

type tableWriter struct {
	w       io.Writer
	fields  []tile.Field
	widths  []int
	held    [][]string
	flushed bool
	rows    int64
}

// NewTable writes an aligned text table, safe for a terminal.
func NewTable(w io.Writer) Writer { return &tableWriter{w: w} }

func (t *tableWriter) Begin(fields []tile.Field) error {
	t.fields = fields
	t.widths = make([]int, len(fields))
	for i, f := range fields {
		t.widths[i] = utf8.RuneCountInString(display(f.Name))
	}
	return nil
}

// display makes text safe to print. Database content could otherwise send terminal
// escape sequences, retitle the window, or fake extra rows.
func display(s string) string {
	s = strings.ToValidUTF8(s, "�")
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			if r == '\n' || r == '\r' || r == '\t' {
				return ' '
			}
			return '�'
		}
		return r
	}, s)
}

func cellText(v tile.Value) string {
	switch v.Kind {
	case tile.KindNull:
		return "NULL"
	case tile.KindBytes:
		return fmt.Sprintf("<%d bytes>", len(v.Raw))
	}
	return display(string(v.Raw))
}

func (t *tableWriter) Row(cells []tile.Value) error {
	t.rows++
	row := make([]string, len(cells))
	for i, v := range cells {
		row[i] = cellText(v)
	}
	if t.flushed {
		return t.line(row)
	}
	for i, s := range row {
		t.widths[i] = max(t.widths[i], min(utf8.RuneCountInString(s), maxWidth))
	}
	t.held = append(t.held, row)
	if len(t.held) >= sizingRows {
		return t.flush()
	}
	return nil
}

func (t *tableWriter) flush() error {
	t.flushed = true
	names := make([]string, len(t.fields))
	for i, f := range t.fields {
		names[i] = display(f.Name)
	}
	if err := t.line(names); err != nil {
		return err
	}
	parts := make([]string, len(t.widths))
	for i, w := range t.widths {
		parts[i] = strings.Repeat("-", w+2)
	}
	if _, err := fmt.Fprintln(t.w, trimRule(strings.Join(parts, "+"))); err != nil {
		return err
	}
	for _, row := range t.held {
		if err := t.line(row); err != nil {
			return err
		}
	}
	t.held = nil
	return nil
}

// trimRule drops the rule's trailing padding, which matches the rows' unpadded last column.
func trimRule(s string) string { return strings.TrimSuffix(s, "-") }

func isNumeric(f tile.Field) bool {
	return f.Kind == tile.KindInt || f.Kind == tile.KindDecimal || f.Kind == tile.KindFloat
}

func (t *tableWriter) line(row []string) error {
	var b strings.Builder
	for i, s := range row {
		if i > 0 {
			b.WriteString(" | ")
		} else {
			b.WriteByte(' ')
		}
		w := t.widths[i]
		runes := utf8.RuneCountInString(s)
		if runes > w {
			r := []rune(s)
			s, runes = string(r[:max(w-1, 0)])+"…", w
		}
		pad := strings.Repeat(" ", w-runes)
		switch {
		case isNumeric(t.fields[i]):
			b.WriteString(pad + s)
		case i == len(row)-1:
			b.WriteString(s)
		default:
			b.WriteString(s + pad)
		}
	}
	_, err := fmt.Fprintln(t.w, b.String())
	return err
}

func (t *tableWriter) End() error {
	if !t.flushed {
		if err := t.flush(); err != nil {
			return err
		}
	}
	noun := "rows"
	if t.rows == 1 {
		noun = "row"
	}
	_, err := fmt.Fprintf(t.w, "(%d %s)\n", t.rows, noun)
	return err
}

// CellText renders a value as text that is safe to show in a terminal.
func CellText(v tile.Value) string { return cellText(v) }

// Display makes database or server text safe to show in a terminal.
func Display(s string) string { return display(s) }
