// Command tess-kv is a Tessera plugin for a tiny key-value store kept in a JSON file.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/arunksoman/tessera/tile"
	"github.com/arunksoman/tessera/tile/plugin"
)

var version = "dev" // set with -ldflags "-X main.version=0.1.0"

func main() { plugin.Serve(Tile{}) }

// Tile is the plugin's entry point.
type Tile struct{}

func (Tile) Manifest() tile.Manifest {
	return tile.Manifest{
		ID: "kv", Name: "Key-value file", APIVersion: tile.APIVersion,
		Version: version, MinTess: "0.5.0", Stability: "alpha",
		Style: tile.Style{Color: "porphyry", Glyph: "K"},
		ConnForm: tile.Form{Fields: []tile.FormField{
			{Name: "path", Label: "Store file", Type: "file", Required: true},
		}},
	}
}

func (Tile) Open(ctx context.Context, cfg tile.ConnConfig, env tile.Env) (tile.Conn, error) {
	path := cfg.Values["path"]
	if path == "" {
		return nil, errors.New("kv: no store file")
	}
	c := &conn{path: path, data: map[string]string{}}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist): // a new store
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(b, &c.data); err != nil {
			return nil, fmt.Errorf("kv: %s is not a store: %w", path, err)
		}
	}
	return c, nil
}

// conn is one open store. Every session shares it.
type conn struct {
	path string
	mu   sync.Mutex
	data map[string]string
}

func (c *conn) Capabilities() tile.Capabilities { return tile.Capabilities{} }
func (c *conn) Ping(context.Context) error      { return nil }
func (c *conn) Close() error                    { return nil }
func (c *conn) Tree() tile.ObjectTree           { return tree{c} }
func (c *conn) Language() tile.Language         { return language{} }

func (c *conn) Session(_ context.Context, opt tile.SessionOptions) (tile.Session, error) {
	return &session{c: c, readOnly: opt.ReadOnly}, nil
}

func (c *conn) save() error {
	b, err := json.MarshalIndent(c.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.path, b, 0o600)
}

func (c *conn) keys() []string {
	keys := make([]string, 0, len(c.data))
	for k := range c.data {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// session runs one command at a time.
type session struct {
	c        *conn
	readOnly bool
}

func (s *session) Close() error { return nil }

func (s *session) Execute(ctx context.Context, req tile.Request) (tile.ResultStream, error) {
	words := strings.Fields(req.Text)
	if len(words) == 0 {
		return nil, errors.New("kv: empty command")
	}
	verb := strings.ToUpper(words[0])
	if s.readOnly && (language{}).Classify(req.Text).Risk != tile.RiskNone {
		return nil, errors.New("kv: read-only session")
	}
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	switch {
	case verb == "GET" && len(words) == 2:
		v, ok := s.c.data[words[1]]
		if !ok {
			return rows(nil), nil
		}
		return rows([][2]string{{words[1], v}}), nil
	case verb == "KEYS" && len(words) == 1:
		var out [][2]string
		for _, k := range s.c.keys() {
			out = append(out, [2]string{k, s.c.data[k]})
		}
		return rows(out), nil
	case verb == "SET" && len(words) >= 3:
		s.c.data[words[1]] = strings.Join(words[2:], " ")
		return affected(1), s.c.save()
	case verb == "DEL" && len(words) == 2:
		n := len(s.c.data)
		delete(s.c.data, words[1])
		return affected(n - len(s.c.data)), s.c.save()
	case verb == "CLEAR" && len(words) == 1:
		n := len(s.c.data)
		clear(s.c.data)
		return affected(n), s.c.save()
	}
	return nil, fmt.Errorf("kv: cannot parse %q: use GET k, SET k v, DEL k, KEYS, or CLEAR", req.Text)
}

// stream hands out frames in order: Schema, Batch, Done. It holds only copies, so its
// values stay valid after the store changes.
type stream struct{ frames []tile.Frame }

func (s *stream) Close() error { return nil }

func (s *stream) Next(context.Context) (tile.Frame, error) {
	if len(s.frames) == 0 {
		return tile.Frame{}, io.EOF
	}
	f := s.frames[0]
	s.frames = s.frames[1:]
	return f, nil
}

func rows(pairs [][2]string) *stream {
	schema := &tile.ResultSchema{Shape: tile.ShapeKeyValue, Fields: []tile.Field{
		{Name: "key", Kind: tile.KindText}, {Name: "value", Kind: tile.KindText},
	}}
	batch := &tile.Batch{Rows: len(pairs)}
	for _, p := range pairs {
		batch.Cells = append(batch.Cells, tile.Text(p[0]), tile.Text(p[1]))
	}
	return &stream{frames: []tile.Frame{{Schema: schema}, {Batch: batch}, {Done: &tile.ResultSummary{}}}}
}

func affected(n int) *stream {
	return &stream{frames: []tile.Frame{{Done: &tile.ResultSummary{RowsAffected: int64(n), HasAffected: true}}}}
}

// language splits a script into one command per line and classifies each command.
type language struct{}

func (language) ID() string { return "kv" }

func (language) Split(text string) []tile.Span {
	var spans []tile.Span
	start := 0
	for i := 0; i <= len(text); i++ {
		if i == len(text) || text[i] == '\n' {
			if strings.TrimSpace(text[start:i]) != "" {
				spans = append(spans, tile.Span{Start: start, End: i})
			}
			start = i + 1
		}
	}
	return spans
}

// Classify is default-deny: anything it does not know is a write.
func (language) Classify(stmt string) tile.Classification {
	verb := ""
	if words := strings.Fields(stmt); len(words) > 0 {
		verb = strings.ToUpper(words[0])
	}
	switch verb {
	case "GET", "KEYS":
		return tile.Classification{Verb: verb, ReadOnly: true}
	case "CLEAR":
		return tile.Classification{Verb: verb, Risk: tile.RiskDestructive, Reason: "CLEAR removes every key"}
	}
	return tile.Classification{Verb: verb, Risk: tile.RiskWrite, Reason: verb + " changes data"}
}

// Redact has nothing to hide: values are data, not credentials.
func (language) Redact(stmt string) string { return stmt }

// tree lists every key at the root.
type tree struct{ c *conn }

func (t tree) Children(_ context.Context, parent tile.ObjectRef, p tile.Page) ([]tile.Node, string, error) {
	if len(parent.Path) > 0 {
		return nil, "", nil
	}
	t.c.mu.Lock()
	keys := t.c.keys()
	t.c.mu.Unlock()
	start, _ := strconv.Atoi(p.Cursor)
	start = min(start, len(keys))
	end := min(start+max(p.Limit, 1), len(keys))
	var nodes []tile.Node
	for _, k := range keys[start:end] {
		nodes = append(nodes, tile.Node{Ref: parent.Child("key", k), Kind: "key", Label: k})
	}
	next := ""
	if end < len(keys) {
		next = strconv.Itoa(end)
	}
	return nodes, next, nil
}

func (t tree) Describe(_ context.Context, ref tile.ObjectRef) (tile.ObjectDetail, error) {
	if len(ref.Path) != 1 {
		return tile.ObjectDetail{}, errors.New("kv: no such object")
	}
	t.c.mu.Lock()
	v, ok := t.c.data[ref.Path[0].Name]
	t.c.mu.Unlock()
	if !ok {
		return tile.ObjectDetail{}, errors.New("kv: no such key")
	}
	return tile.ObjectDetail{Sections: []tile.DetailSection{{
		Title:  "Value",
		Fields: []tile.Field{{Name: "value", Kind: tile.KindText}},
		Rows:   [][]tile.Value{{tile.Text(v)}},
	}}}, nil
}
