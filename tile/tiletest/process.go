package tiletest

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/arunksoman/tessera/tile"
	"github.com/arunksoman/tessera/tile/plugin"
)

const childEnv = "TESSERA_TILETEST_PLUGIN"

// ServeIfChild serves t as a plugin and exits when this test binary was started by
// OutOfProcess. Call it first in TestMain.
func ServeIfChild(t tile.Tile) {
	if os.Getenv(childEnv) == "1" {
		plugin.Serve(t)
		os.Exit(0)
	}
}

// OutOfProcess starts this test binary again as a plugin and returns a tile that talks to it
// over the plugin protocol, so the suite can check a tile behaves the same across the
// process boundary.
func OutOfProcess(t *testing.T) tile.Tile {
	t.Helper()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), childEnv+"=1")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	inR.Close()
	outW.Close()
	c := plugin.NewClient(outR, inW)
	t.Cleanup(func() {
		inW.Close()
		cmd.Wait()
		outR.Close()
	})
	h, err := c.Handshake(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	return remoteTile{c, h.Manifest}
}

type remoteTile struct {
	c *plugin.Client
	m tile.Manifest
}

func (r remoteTile) Manifest() tile.Manifest { return r.m }

func (r remoteTile) Open(ctx context.Context, cfg tile.ConnConfig, env tile.Env) (tile.Conn, error) {
	return r.c.Open(ctx, cfg, env)
}
