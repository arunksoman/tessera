// Command fakeplugin is a tiny tile served as a plugin, for tests. Its tile ID is its own
// file name, so one binary can be installed under any name.
package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/arunksoman/tessera/tile"
	"github.com/arunksoman/tessera/tile/plugin"
)

func main() {
	id := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
	if strings.HasPrefix(id, "liar") {
		id = "postgres"
	}
	plugin.Serve(fake{id: id})
}

type fake struct{ id string }

// Manifest needs tess 99.0.0 when the plugin's name starts with "future".
func (f fake) Manifest() tile.Manifest {
	m := tile.Manifest{ID: f.id, Name: "Fake " + f.id, APIVersion: tile.APIVersion, Version: "1.0.0",
		ConnForm: tile.Form{Fields: []tile.FormField{{Name: "host", Label: "Host", Type: "text"}}}}
	if strings.HasPrefix(f.id, "future") {
		m.MinTess = "99.0.0"
	}
	return m
}

// ConfigWarnings reports what a plugin could learn from its environment.
func (f fake) ConfigWarnings(tile.ConnConfig) []string {
	return []string{"env:" + os.Getenv("TESSERA_PLUGIN_TEST_SECRET")}
}

func (f fake) Open(ctx context.Context, _ tile.ConnConfig, env tile.Env) (tile.Conn, error) {
	pw, err := env.Secret(ctx, "password")
	if err != nil {
		return nil, err
	}
	if string(pw) != "pw" {
		return nil, errors.New("wrong password")
	}
	return conn{}, nil
}

type conn struct{}

func (conn) Capabilities() tile.Capabilities { return tile.Capabilities{} }
func (conn) Ping(context.Context) error      { return nil }
func (conn) Close() error                    { return nil }
func (conn) Tree() tile.ObjectTree           { return nil }
func (conn) Language() tile.Language         { return lang{} }
func (conn) Session(context.Context, tile.SessionOptions) (tile.Session, error) {
	return session{}, nil
}

type lang struct{}

func (lang) ID() string                 { return "fake" }
func (lang) Split(s string) []tile.Span { return []tile.Span{{Start: 0, End: len(s)}} }
func (lang) Redact(s string) string     { return s }
func (lang) Classify(string) tile.Classification {
	return tile.Classification{Verb: "ROWS", ReadOnly: true}
}

type session struct{}

func (session) Close() error { return nil }
func (session) Execute(_ context.Context, req tile.Request) (tile.ResultStream, error) {
	n, err := strconv.Atoi(strings.TrimPrefix(req.Text, "rows "))
	if err != nil {
		return nil, err
	}
	return &stream{n: n}, nil
}

type stream struct{ n, step int }

func (s *stream) Close() error { return nil }
func (s *stream) Next(context.Context) (tile.Frame, error) {
	s.step++
	switch s.step {
	case 1:
		return tile.Frame{Schema: &tile.ResultSchema{Fields: []tile.Field{{Name: "n", Kind: tile.KindInt}}}}, nil
	case 2:
		b := &tile.Batch{Rows: s.n}
		for i := 1; i <= s.n; i++ {
			b.Cells = append(b.Cells, tile.Value{Kind: tile.KindInt, Raw: []byte(strconv.Itoa(i))})
		}
		return tile.Frame{Batch: b}, nil
	case 3:
		return tile.Frame{Done: &tile.ResultSummary{}}, nil
	}
	return tile.Frame{}, io.EOF
}
