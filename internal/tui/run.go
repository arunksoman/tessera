package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/arunksoman/tessera/internal/engine"
	"github.com/arunksoman/tessera/internal/ioformat"
	"github.com/arunksoman/tessera/tile"
)

// Worker messages. Workers send these through Model.events and never touch model state.
type (
	resultStartMsg struct {
		t *tab
		g *grid
	}
	rowsMsg struct {
		g    *grid
		gen  int
		rows [][]tile.Value
	}
	truncatedMsg struct {
		g *grid
	}
	affectedMsg struct {
		t   *tab
		n   int64
		tag string
	}
	runDoneMsg struct {
		t   *tab
		ddl bool
		err error
	}
	pageDoneMsg struct {
		g    *grid
		t    *tab
		gen  int
		rows int
		err  error
	}
	askMsg struct {
		p     engine.Prompt
		cs    *connState
		reply chan bool
	}
	applyDoneMsg struct {
		t   *tab
		g   *grid
		n   int
		err error
	}
)

// onEvent handles worker messages. The second result says whether msg was one.
func (m *Model) onEvent(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case resultStartMsg:
		msg.t.results = append(msg.t.results, msg.g)
		msg.t.current = len(msg.t.results) - 1
	case rowsMsg:
		if msg.g.browse == nil || msg.g.browse.gen == msg.gen {
			msg.g.add(msg.rows)
		}
	case truncatedMsg:
		msg.g.truncated = true
		msg.g.note = fmt.Sprintf("Stopped at %d rows to protect memory. Narrow the query to see the rest.", len(msg.g.rows))
	case affectedMsg:
		msg.t.info = count(int(msg.n), "row") + " affected"
	case runDoneMsg:
		return m.onRunDone(msg), true
	case pageDoneMsg:
		return m.onPageDone(msg), true
	case askMsg:
		m.push(newConfirm(msg))
	case applyDoneMsg:
		return m.onApplyDone(msg), true
	default:
		return nil, false
	}
	return nil, true
}

func (m *Model) onRunDone(msg runDoneMsg) tea.Cmd {
	t := msg.t
	t.running = false
	if t.cancel != nil {
		t.cancel()
		t.cancel = nil
	}
	if t.closing {
		t.console.Close()
		return nil
	}
	m.status = ""
	if msg.err != nil {
		t.err = errText(msg.err)
		m.setStatus(t.err, true)
		return nil
	}
	if msg.ddl {
		return m.refreshTree()
	}
	return nil
}

// sinkFor streams one statement after another into new grids, stopping each at the caps.
func (m *Model) sinkFor(t *tab, title func(int) string) engine.Sink {
	var g *grid
	var rows, size int
	return func(index int, f tile.Frame) error {
		switch {
		case f.Schema != nil:
			g = newGrid(title(index), f.Schema.Fields)
			rows, size = 0, 0
			m.send(resultStartMsg{t, g})
		case f.Batch != nil && g != nil:
			batch := copyRows(f.Batch, len(g.fields))
			m.send(rowsMsg{g: g, rows: batch})
			rows += f.Batch.Rows
			for _, c := range f.Batch.Cells {
				size += len(c.Raw)
			}
			if rows >= maxGridRows || size >= maxGridBytes {
				m.send(truncatedMsg{g})
				return engine.ErrStop
			}
		case f.Done != nil && f.Done.HasAffected:
			m.send(affectedMsg{t, f.Done.RowsAffected, f.Done.Tag})
		}
		return nil
	}
}

func (m *Model) confirmFor(ctx context.Context, cs *connState) engine.Confirm {
	return func(p engine.Prompt) bool {
		reply := make(chan bool, 1)
		m.send(askMsg{p, cs, reply})
		select {
		case yes := <-reply:
			return yes
		case <-ctx.Done():
			return false
		case <-m.ctx.Done():
			return false
		}
	}
}

func (t *tab) begin(m *Model) context.Context {
	ctx, cancel := context.WithCancel(m.ctx)
	t.cancel = cancel
	t.running = true
	t.started = m.now()
	t.results, t.current, t.err, t.info = nil, 0, "", ""
	return ctx
}

func editorOffset(ta *textarea.Model) int {
	lines := strings.Split(ta.Value(), "\n")
	n := 0
	for i := 0; i < ta.Line() && i < len(lines); i++ {
		n += len(lines[i]) + 1
	}
	if ta.Line() < len(lines) {
		runes := []rune(lines[ta.Line()])
		n += len(string(runes[:min(ta.Column(), len(runes))]))
	}
	return n
}

// consoleTab returns the active tab if it can run statements, or says why not.
func (m *Model) consoleTab() *tab {
	t := m.activeTab()
	switch {
	case t == nil:
		m.setStatus("connect first: Alt+C", true)
	case t.console == nil:
		m.setStatus("this tab browses a table; Alt+T opens an editor", true)
	case t.running:
		m.setStatus("still running; Esc cancels", true)
	default:
		return t
	}
	return nil
}

func (m *Model) runEditor(script bool) tea.Cmd {
	t := m.consoleTab()
	if t == nil {
		return nil
	}
	text := t.editor.Value()
	if !script {
		text = t.cs.conn.StatementAt(text, editorOffset(&t.editor))
	}
	if strings.TrimSpace(text) == "" {
		m.setStatus("nothing to run", true)
		return nil
	}
	t.comp = nil
	m.status = ""
	ctx := t.begin(m)
	opt := engine.RunOptions{Confirm: m.confirmFor(ctx, t.cs)}
	ddl := t.cs.conn.IsDDL(text)
	sink := m.sinkFor(t, func(i int) string { return fmt.Sprintf("Statement %d", i+1) })
	m.spawn(func() {
		err := t.console.Run(ctx, text, opt, sink)
		m.send(runDoneMsg{t: t, ddl: ddl && err == nil, err: err})
	})
	return tick()
}

func (m *Model) explain(asJSON bool) tea.Cmd {
	t := m.consoleTab()
	if t == nil {
		return nil
	}
	stmt := t.cs.conn.StatementAt(t.editor.Value(), editorOffset(&t.editor))
	if strings.TrimSpace(stmt) == "" {
		m.setStatus("nothing to explain", true)
		return nil
	}
	format := tile.ExplainText
	if asJSON {
		format = tile.ExplainJSON
	}
	ctx := t.begin(m)
	sink := m.sinkFor(t, func(int) string { return "Plan" })
	m.spawn(func() {
		err := t.cs.conn.Explain(ctx, stmt, format, func(f tile.Frame) error { return sink(0, f) })
		m.send(runDoneMsg{t: t, err: err})
	})
	return tick()
}

// New tabs and connections.

type connectedMsg struct {
	profile string
	cs      *connState
	err     error
}

func (m *Model) connect(profile string) tea.Cmd {
	if cs, ok := m.conns[profile]; ok {
		return func() tea.Msg { return connectedMsg{profile: profile, cs: cs} }
	}
	m.setStatus("connecting to "+profile+"…", false)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, connTimeout)
		defer cancel()
		conn, release, err := m.mgr.Acquire(ctx, profile)
		if err != nil {
			return connectedMsg{profile: profile, err: err}
		}
		cs := &connState{profile: conn.Profile(), conn: conn, release: release}
		if t, ok := m.e.Tile(cs.profile.Tile); ok {
			cs.style = t.Manifest().Style
		}
		return connectedMsg{profile: profile, cs: cs}
	}
}

func (m *Model) onConnected(msg connectedMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus(errText(msg.err), true)
		// A failed start still needs a way forward.
		if len(m.overlays) == 0 && len(m.tabs) == 0 {
			return m.openConnections()
		}
		return nil
	}
	m.status = ""
	cs := msg.cs
	var cmds []tea.Cmd
	if _, known := m.conns[msg.profile]; known {
		// Switching to a connection already open goes to its tab.
		if i := slices.IndexFunc(m.tabs, func(t *tab) bool { return t.cs == cs }); i >= 0 {
			m.active = i
			m.layout()
			return m.syncFocus()
		}
	} else {
		m.conns[msg.profile] = cs
		cs.tree.expanded = map[string]bool{}
		cmds = append(cmds, cs.tree.reload(cs))
		if w := m.e.Warnings(cs.profile); len(w) > 0 {
			m.setStatus("warning: "+strings.Join(w, "; "), true)
		}
	}
	cmds = append(cmds, m.addConsole(cs))
	return tea.Batch(cmds...)
}

func (m *Model) addConsole(cs *connState) tea.Cmd {
	m.nextTab++
	ta := textarea.New()
	ta.ShowLineNumbers = true
	ta.Prompt = ""
	ta.MaxHeight = 0
	ta.CharLimit = 0
	ta.Placeholder = "Write SQL here. F5 runs the statement at the cursor."
	ta.SetStyles(textarea.DefaultStyles(m.th.dark))
	t := &tab{title: fmt.Sprintf("%s %d", cs.profile.Name, m.nextTab), cs: cs, console: cs.conn.NewConsole(), editor: ta}
	cs.tabs++
	m.tabs = append(m.tabs, t)
	m.active = len(m.tabs) - 1
	m.focus = focusEditor
	m.layout()
	return m.syncFocus()
}

func (m *Model) newTab() tea.Cmd {
	cs := m.activeConn()
	if cs == nil {
		return m.openConnections()
	}
	return m.addConsole(cs)
}

func (m *Model) closeTab() tea.Cmd {
	t := m.activeTab()
	if t == nil {
		return nil
	}
	var risk []string
	if t.console != nil && t.console.InTx() {
		risk = append(risk, "It has an open transaction, which will roll back.")
	}
	for _, g := range t.results {
		if len(g.pending) > 0 {
			risk = append(risk, "It has edits that were not applied.")
		}
	}
	if len(risk) > 0 {
		m.push(newQuestion("Close "+t.title+"?", strings.Join(risk, "\n"), func(yes bool) tea.Cmd {
			if yes {
				return m.dropTab(t)
			}
			return nil
		}))
		return nil
	}
	return m.dropTab(t)
}

func (m *Model) dropTab(t *tab) tea.Cmd {
	i := slices.Index(m.tabs, t)
	if i < 0 {
		return nil
	}
	m.tabs = slices.Delete(m.tabs, i, i+1)
	m.active = max(0, min(m.active, len(m.tabs)-1))
	t.closing = true
	if t.running && t.cancel != nil {
		t.cancel()
	} else if t.console != nil {
		t.console.Close()
	}
	t.cs.tabs--
	if t.cs.tabs == 0 {
		delete(m.conns, t.cs.profile.Name)
		t.cs.release()
	}
	if at := m.activeTab(); at != nil && at.console == nil && m.focus == focusEditor {
		m.focus = focusGrid
	}
	m.layout()
	return m.syncFocus()
}

func (m *Model) cycleTab(by int) tea.Cmd {
	if len(m.tabs) == 0 {
		return nil
	}
	m.active = (m.active + by + len(m.tabs)) % len(m.tabs)
	if m.activeTab().console == nil && m.focus == focusEditor {
		m.focus = focusGrid
	}
	m.layout()
	return m.syncFocus()
}

func (m *Model) refreshTree() tea.Cmd {
	cs := m.activeConn()
	if cs == nil {
		return nil
	}
	cs.conn.Refresh()
	return cs.tree.reload(cs)
}

// Browsing.

type layoutMsg struct {
	t      *tab
	g      *grid
	layout tile.Layout
	err    error
}

func objectTitle(ref tile.ObjectRef) string {
	parts := make([]string, len(ref.Path))
	for i, s := range ref.Path {
		parts[i] = s.Name
	}
	return strings.Join(parts, ".")
}

func (m *Model) openBrowse(cs *connState, n tile.Node) tea.Cmd {
	g := newGrid(objectTitle(n.Ref), nil)
	g.browse = &browse{ref: n.Ref, loading: true}
	t := &tab{title: n.Label, cs: cs, results: []*grid{g}, running: false}
	cs.tabs++
	m.tabs = append(m.tabs, t)
	m.active = len(m.tabs) - 1
	m.focus = focusGrid
	m.layout()
	syncCmd := m.syncFocus()
	return tea.Batch(syncCmd, func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, treeTimeout)
		defer cancel()
		l, err := cs.conn.Layout(ctx, n.Ref)
		return layoutMsg{t, g, l, err}
	})
}

func (m *Model) onLayout(msg layoutMsg) tea.Cmd {
	g := msg.g
	if msg.err != nil {
		g.browse.loading = false
		msg.t.err = errText(msg.err)
		return nil
	}
	g.browse.layout = msg.layout
	fresh := newGrid(g.title, msg.layout.Fields)
	g.fields, g.widths = fresh.fields, fresh.widths
	if msg.layout.Note != "" {
		g.note = "Read-only: " + msg.layout.Note
	}
	return m.loadPage(msg.t, g)
}

// loadPage reads the next page in a worker.
func (m *Model) loadPage(t *tab, g *grid) tea.Cmd {
	b := g.browse
	if t.cancel != nil {
		t.cancel()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	t.cancel = cancel
	t.running = true
	t.started = m.now()
	t.err = ""
	b.loading = true
	q := tile.BrowseQuery{
		Sort: slices.Clone(b.sort), Filter: slices.Clone(b.filter),
		Offset: int64(len(g.rows)), Limit: browsePage,
	}
	if len(g.rows) > 0 {
		q.After = g.rows[len(g.rows)-1]
	}
	cs, gen, ref, layout := t.cs, b.gen, b.ref, b.layout
	m.spawn(func() {
		rows := 0
		err := cs.conn.Browse(ctx, ref, layout, q, func(f tile.Frame) error {
			if f.Batch != nil {
				m.send(rowsMsg{g: g, gen: gen, rows: copyRows(f.Batch, len(layout.Fields))})
				rows += f.Batch.Rows
			}
			return nil
		})
		m.send(pageDoneMsg{g: g, t: t, gen: gen, rows: rows, err: err})
	})
	return tick()
}

func (m *Model) onPageDone(msg pageDoneMsg) tea.Cmd {
	g, t := msg.g, msg.t
	if g.browse.gen != msg.gen {
		return nil // a reload replaced this page
	}
	t.running = false
	if t.cancel != nil {
		t.cancel()
		t.cancel = nil
	}
	g.browse.loading = false
	if msg.err != nil {
		t.err = errText(msg.err)
		g.browse.exhausted = true
		return nil
	}
	g.browse.exhausted = msg.rows < browsePage
	if len(g.rows) >= maxGridRows {
		g.browse.exhausted = true
		g.truncated = true
		g.note = fmt.Sprintf("Stopped at %d rows to protect memory. Filter to see the rest.", len(g.rows))
	}
	g.move(0, 0)
	return nil
}

// reload discards rows and reads from the first page, after a sort, filter, or apply.
func (m *Model) reload(t *tab, g *grid) tea.Cmd {
	g.browse.gen++
	g.browse.exhausted = false
	g.rows = nil
	g.pending = map[[2]int]tile.Value{}
	g.top = 0
	g.row = 0
	t.info = ""
	return m.loadPage(t, g)
}

func (m *Model) maybeLoadMore(t *tab, g *grid) tea.Cmd {
	b := g.browse
	if b == nil || b.loading || b.exhausted || g.truncated || g.row < len(g.rows)-loadMargin {
		return nil
	}
	return m.loadPage(t, g)
}

func (m *Model) gridKey(key string) tea.Cmd {
	t := m.activeTab()
	if t == nil {
		return nil
	}
	g := t.grid()
	if g == nil {
		return nil
	}
	_, _, _, gridH := m.paneSizes()
	page := max(1, gridH-5)
	switch key {
	case "up", "k":
		g.move(-1, 0)
	case "down", "j":
		g.move(1, 0)
	case "left", "h":
		g.move(0, -1)
	case "right", "l":
		g.move(0, 1)
	case "pgup":
		g.move(-page, 0)
	case "pgdown":
		g.move(page, 0)
	case "home":
		g.col = 0
	case "end":
		g.move(0, len(g.fields))
	case "g":
		g.row = 0
	case "G":
		g.move(len(g.rows), 0)
	case "[":
		t.current = max(0, t.current-1)
	case "]":
		t.current = min(len(t.results)-1, t.current+1)
	case "v":
		m.showCell(g)
	case "enter":
		if g.browse != nil && g.browse.editable() {
			m.editCell(t, g)
		} else {
			m.showCell(g)
		}
	case "e":
		m.editCell(t, g)
	case "u":
		delete(g.pending, [2]int{g.row, g.col})
	case "s":
		return m.toggleSort(t, g)
	case "f", "/":
		m.openFilter(t, g)
	case "c":
		if g.browse != nil && len(g.browse.filter) > 0 && m.okToDiscard(g) {
			g.browse.filter = nil
			return m.reload(t, g)
		}
	}
	return m.maybeLoadMore(t, g)
}

// okToDiscard refuses to throw away pending edits by reloading.
func (m *Model) okToDiscard(g *grid) bool {
	if len(g.pending) > 0 {
		m.setStatus("apply with Ctrl+S or undo with u first", true)
		return false
	}
	return true
}

func (m *Model) toggleSort(t *tab, g *grid) tea.Cmd {
	if g.browse == nil {
		m.setStatus("only a browsed table can be sorted here; add ORDER BY to the query", true)
		return nil
	}
	if len(g.fields) == 0 || !m.okToDiscard(g) {
		return nil
	}
	name := g.fields[g.col].Name
	next := []tile.SortKey{{Field: name}}
	if cur := g.browse.sort; len(cur) == 1 && cur[0].Field == name {
		if cur[0].Desc {
			next = nil
		} else {
			next = []tile.SortKey{{Field: name, Desc: true}}
		}
	}
	g.browse.sort = next
	return m.reload(t, g)
}

func (m *Model) showCell(g *grid) {
	if len(g.rows) == 0 || len(g.fields) == 0 {
		return
	}
	v := g.rows[g.row][g.col]
	if p, ok := g.pending[[2]int{g.row, g.col}]; ok {
		v = p
	}
	text := "NULL"
	switch v.Kind {
	case tile.KindNull:
	case tile.KindBytes:
		text = fmt.Sprintf("%d bytes\n% x", len(v.Raw), v.Raw[:min(len(v.Raw), 4096)])
	default:
		lines := strings.Split(string(v.Raw), "\n")
		for i := range lines {
			lines[i] = ioformat.Display(lines[i])
		}
		text = strings.Join(lines, "\n")
	}
	m.push(newTextView(ioformat.Display(g.fields[g.col].Name), text))
}

func (m *Model) editCell(t *tab, g *grid) {
	switch {
	case g.browse == nil:
		m.setStatus("console results are read-only; open the table from the tree to edit it", true)
	case !g.browse.editable():
		m.setStatus("read-only: "+g.browse.layout.Note, true)
	case t.cs.profile.ReadOnly:
		m.setStatus("this connection is read-only", true)
	case len(g.rows) == 0:
	default:
		v := g.rows[g.row][g.col]
		if p, ok := g.pending[[2]int{g.row, g.col}]; ok {
			v = p
		}
		if err := canEditText(v); err != nil {
			m.setStatus(err.Error(), true)
			return
		}
		m.push(newCellEdit(g, g.row, g.col, v, m.th.dark))
	}
}

func (m *Model) openFilter(t *tab, g *grid) {
	if g.browse == nil {
		m.setStatus("only a browsed table can be filtered here; add WHERE to the query", true)
		return
	}
	if len(g.fields) == 0 || !m.okToDiscard(g) {
		return
	}
	m.push(newFilterPrompt(t, g))
}

// Applying edits.

type planMsg struct {
	t    *tab
	g    *grid
	plan *engine.Pending
	err  error
}

func (m *Model) applyPending() tea.Cmd {
	t := m.activeTab()
	if t == nil {
		return nil
	}
	g := t.grid()
	if g == nil || g.browse == nil || len(g.pending) == 0 {
		m.setStatus("no pending edits", false)
		return nil
	}
	if g.browse.loading {
		m.setStatus("wait for the page to finish loading", true)
		return nil
	}
	cs, ref, layout, changes := t.cs, g.browse.ref, g.browse.layout, g.changes()
	return func() tea.Msg {
		p, err := cs.conn.PlanChanges(ref, layout, changes)
		return planMsg{t, g, p, err}
	}
}

func (m *Model) onPlan(msg planMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus(errText(msg.err), true)
		return nil
	}
	m.push(newPreview(msg.t, msg.g, msg.plan))
	return nil
}

func (m *Model) startApply(t *tab, g *grid, p *engine.Pending) tea.Cmd {
	ctx, cancel := context.WithCancel(m.ctx)
	t.cancel = cancel
	t.running = true
	t.started = m.now()
	cs := t.cs
	m.spawn(func() {
		err := cs.conn.Apply(ctx, p, engine.RunOptions{Confirm: m.confirmFor(ctx, cs)})
		m.send(applyDoneMsg{t, g, len(p.Plan.Steps), err})
	})
	return tick()
}

func (m *Model) onApplyDone(msg applyDoneMsg) tea.Cmd {
	t, g := msg.t, msg.g
	t.running = false
	if t.cancel != nil {
		t.cancel()
		t.cancel = nil
	}
	if msg.err != nil {
		text := errText(msg.err)
		// Edits stay pending, so nothing typed is lost.
		if errors.Is(msg.err, engine.ErrConflict) {
			text = "conflict: " + text
		}
		t.err = text
		m.setStatus(text, true)
		return nil
	}
	row, col := g.row, g.col
	cmd := m.reload(t, g)
	g.row, g.col = row, col
	t.info = fmt.Sprintf("applied %s", count(msg.n, "change"))
	m.setStatus(t.info, false)
	return cmd
}

func (m *Model) describe(cs *connState, n tile.Node) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, treeTimeout)
		defer cancel()
		d, err := cs.conn.Tree().Describe(ctx, n.Ref)
		return describedMsg{cs, n, d, err}
	}
}

type describedMsg struct {
	cs  *connState
	n   tile.Node
	d   tile.ObjectDetail
	err error
}

// onDescribed opens the object's details in a tab of their own, one result per section.
func (m *Model) onDescribed(msg describedMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus(errText(msg.err), true)
		return nil
	}
	t := &tab{title: msg.n.Label + " details", cs: msg.cs}
	for _, s := range msg.d.Sections {
		g := newGrid(s.Title, s.Fields)
		g.add(s.Rows)
		t.results = append(t.results, g)
	}
	msg.cs.tabs++
	m.tabs = append(m.tabs, t)
	m.active = len(m.tabs) - 1
	m.focus = focusGrid
	m.layout()
	return m.syncFocus()
}
