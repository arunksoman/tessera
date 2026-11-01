package tui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/arunksoman/tessera/internal/ioformat"
	"github.com/arunksoman/tessera/tile"
)

const (
	treePage    = 200
	treeTimeout = 15 * time.Second
)

// treeNode is one loaded object. Children load when it is first expanded.
type treeNode struct {
	node     tile.Node
	depth    int
	open     bool
	loading  bool
	children []*treeNode
	next     string // cursor of the next page of children
	err      string
}

type objectTree struct {
	root     treeNode // a hidden node above the schemas
	cursor   int
	top      int
	expanded map[string]bool // survives a reload, so refreshing keeps the view
}

// treeRow is a visible line: a node, or the "more" line that loads its parent's next page.
type treeRow struct {
	n    *treeNode
	more *treeNode
}

func (t *objectTree) rows() []treeRow {
	var out []treeRow
	var walk func(n *treeNode)
	walk = func(n *treeNode) {
		for _, c := range n.children {
			out = append(out, treeRow{n: c})
			if c.open {
				walk(c)
			}
		}
		if n.next != "" {
			out = append(out, treeRow{more: n})
		}
	}
	walk(&t.root)
	return out
}

func refKey(r tile.ObjectRef) string {
	var b strings.Builder
	for _, s := range r.Path {
		b.WriteString(s.Kind + "\x00" + s.Name + "\x00")
	}
	return b.String()
}

type treeLoadedMsg struct {
	cs     *connState
	parent *treeNode
	nodes  []tile.Node
	next   string
	err    error
}

func loadChildren(cs *connState, parent *treeNode, cursor string) tea.Cmd {
	parent.loading = true
	ref := parent.node.Ref
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), treeTimeout)
		defer cancel()
		nodes, next, err := cs.conn.Tree().Children(ctx, ref, tile.Page{Cursor: cursor, Limit: treePage})
		return treeLoadedMsg{cs, parent, nodes, next, err}
	}
}

// onLoaded adds a page of children, and reloads any that were open before a refresh.
func (t *objectTree) onLoaded(msg treeLoadedMsg) tea.Cmd {
	p := msg.parent
	p.loading = false
	if msg.err != nil {
		p.err = ioformat.Display(msg.err.Error())
		return nil
	}
	p.err = ""
	p.next = msg.next
	var cmds []tea.Cmd
	for _, n := range msg.nodes {
		c := &treeNode{node: n, depth: p.depth + 1}
		p.children = append(p.children, c)
		if n.HasChildren && t.expanded[refKey(n.Ref)] {
			c.open = true
			cmds = append(cmds, loadChildren(msg.cs, c, ""))
		}
	}
	return tea.Batch(cmds...)
}

// reload starts again from the top, keeping which nodes are open.
func (t *objectTree) reload(cs *connState) tea.Cmd {
	t.root = treeNode{depth: -1}
	if t.expanded == nil {
		t.expanded = map[string]bool{}
	}
	return loadChildren(cs, &t.root, "")
}

func (t *objectTree) view(th theme, width, height int, focused bool) []string {
	rows := t.rows()
	t.cursor = max(0, min(len(rows)-1, t.cursor))
	if t.cursor < t.top {
		t.top = t.cursor
	}
	if t.cursor >= t.top+height {
		t.top = t.cursor - height + 1
	}
	var lines []string
	if t.root.err != "" {
		lines = append(lines, th.danger.Render(fit(t.root.err, width)))
	}
	if len(rows) == 0 && t.root.loading {
		lines = append(lines, th.muted.Render("loading…"))
	}
	for i := t.top; i < len(rows) && len(lines) < height; i++ {
		text := rows[i].label()
		text = fit(text, width)
		switch {
		case focused && i == t.cursor:
			text = th.cursor.Render(pad(text, width, false))
		case rows[i].more != nil:
			text = th.accent.Render(text)
		case rows[i].n.err != "":
			text = th.danger.Render(text)
		default:
			text = th.text.Render(text)
		}
		lines = append(lines, text)
	}
	return lines
}

func (r treeRow) label() string {
	if r.more != nil {
		return strings.Repeat("  ", r.more.depth+1) + "… load more"
	}
	n := r.n
	mark := "  "
	switch {
	case n.node.HasChildren && n.open:
		mark = "▾ "
	case n.node.HasChildren:
		mark = "▸ "
	}
	suffix := ""
	if n.node.Kind == "view" {
		suffix = " (view)"
	}
	if n.loading {
		suffix += " …"
	}
	if n.err != "" {
		suffix += " ! " + n.err
	}
	return strings.Repeat("  ", n.depth) + mark + ioformat.Display(n.node.Label) + suffix
}

// key handles tree navigation. It returns a browse or describe request through the model.
func (m *Model) treeKey(key string) tea.Cmd {
	cs := m.activeConn()
	if cs == nil {
		return nil
	}
	t := &cs.tree
	rows := t.rows()
	if len(rows) == 0 {
		if key == "r" {
			return m.refreshTree()
		}
		return nil
	}
	t.cursor = max(0, min(len(rows)-1, t.cursor))
	row := rows[t.cursor]
	switch key {
	case "up", "k":
		t.cursor = max(0, t.cursor-1)
	case "down", "j":
		t.cursor = min(len(rows)-1, t.cursor+1)
	case "pgup":
		t.cursor = max(0, t.cursor-10)
	case "pgdown":
		t.cursor = min(len(rows)-1, t.cursor+10)
	case "home", "g":
		t.cursor = 0
	case "end", "G":
		t.cursor = len(rows) - 1
	case "r":
		return m.refreshTree()
	case "left", "h":
		if row.n != nil && row.n.open {
			row.n.open = false
			delete(t.expanded, refKey(row.n.node.Ref))
		}
	case "d":
		if row.n != nil && row.n.node.Browsable {
			return m.describe(cs, row.n.node)
		}
	case "right", "l", "enter", "space":
		if row.more != nil {
			return loadChildren(cs, row.more, row.more.next)
		}
		n := row.n
		switch {
		case n.node.HasChildren && !n.open:
			n.open = true
			t.expanded[refKey(n.node.Ref)] = true
			if len(n.children) == 0 {
				return loadChildren(cs, n, "")
			}
		case n.node.Browsable && key != "space":
			return m.openBrowse(cs, n.node)
		}
	}
	return nil
}
