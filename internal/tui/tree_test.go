package tui

import (
	"strings"
	"testing"

	"github.com/arunksoman/tessera/tile"
)

func TestTreeRowShowsKindAndDetail(t *testing.T) {
	cases := []struct {
		node tile.Node
		want string
	}{
		{tile.Node{Kind: "table", Label: "items"}, "items"},
		{tile.Node{Kind: "view", Label: "report"}, "report (view)"},
		{tile.Node{Kind: "function", Label: "total", HasChildren: true}, "▸ total (function)"},
		{tile.Node{Kind: "procedure", Label: "reset", HasChildren: true}, "▸ reset (procedure)"},
		{tile.Node{Kind: "parameter", Label: "n", Detail: "integer"}, "n integer"},
	}
	for _, c := range cases {
		got := strings.TrimSpace(treeRow{n: &treeNode{node: c.node}}.label())
		if got != c.want {
			t.Errorf("%+v: got %q, want %q", c.node, got, c.want)
		}
	}
	// Detail comes from the database, so control characters never reach the terminal.
	got := treeRow{n: &treeNode{node: tile.Node{Kind: "parameter", Label: "x", Detail: "int\x1b[31m"}}}.label()
	if strings.ContainsRune(got, 0x1b) {
		t.Errorf("escape reached the terminal: %q", got)
	}
}
