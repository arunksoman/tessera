package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/arunksoman/tessera/tile"
	"github.com/arunksoman/tessera/tile/tiletest"
)

// TestMain lets tiletest.OutOfProcess start this test binary again as the plugin.
func TestMain(m *testing.M) {
	tiletest.ServeIfChild(Tile{})
	os.Exit(m.Run())
}

func TestSetGet(t *testing.T) {
	for name, tl := range map[string]tile.Tile{"in process": Tile{}, "out of process": tiletest.OutOfProcess(t)} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			cfg := tile.ConnConfig{Values: map[string]string{"path": filepath.Join(t.TempDir(), "store.json")}}
			c, err := tl.Open(ctx, cfg, tile.Env{})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			s, err := c.Session(ctx, tile.SessionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			run(t, s, "SET greeting hello world")
			got := run(t, s, "GET greeting")
			if len(got) != 2 || got[1] != "hello world" {
				t.Fatalf("GET greeting = %q", got)
			}
			if r := c.Language().Classify("CLEAR"); r.Risk != tile.RiskDestructive {
				t.Fatalf("CLEAR classified as %+v", r)
			}
			if spans := c.Language().Split("GET a\n\nKEYS\n"); len(spans) != 2 {
				t.Fatalf("Split = %v", spans)
			}
		})
	}
}

// run executes one command and returns its cells as text, checking the frame order.
func run(t *testing.T, s tile.Session, text string) []string {
	t.Helper()
	st, err := s.Execute(context.Background(), tile.Request{Text: text})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var cells []string
	for {
		f, err := st.Next(context.Background())
		if err == io.EOF {
			return cells
		}
		if err != nil {
			t.Fatal(err)
		}
		if f.Batch != nil {
			for _, v := range f.Batch.Cells {
				cells = append(cells, string(v.Raw)) // copy: Raw is only valid until the next Next
			}
		}
	}
}
