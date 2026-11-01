// Package tui is the terminal front-end, built on Bubble Tea. Every statement goes through
// the engine, so the safety gate, history, and audit apply here as in the CLI.
package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/arunksoman/tessera/internal/engine"
	"github.com/arunksoman/tessera/internal/ioformat"
	"github.com/arunksoman/tessera/internal/store"
	"github.com/arunksoman/tessera/tile"
)

const (
	maxOpenConns = 8
	idleClose    = 10 * time.Minute
	connTimeout  = 15 * time.Second
	closeWait    = 3 * time.Second
	minWidth     = 40
	minHeight    = 16
)

type focusArea uint8

const (
	focusTree focusArea = iota
	focusEditor
	focusGrid
)

// connState is one open connection and what is shared by its tabs.
type connState struct {
	profile store.Profile
	conn    *engine.Conn
	release func()
	style   tile.Style
	tree    objectTree
	tabs    int
}

// tab is a console with an editor, or a browse of one object.
type tab struct {
	title   string
	cs      *connState
	console *engine.Console // nil for a browse tab
	editor  textarea.Model
	results []*grid
	current int
	running bool
	closing bool
	cancel  context.CancelFunc
	started time.Time
	err     string
	info    string
	comp    *completion
}

func (t *tab) grid() *grid {
	if t.current < len(t.results) {
		return t.results[t.current]
	}
	return nil
}

// Model is the whole application state.
type Model struct {
	e       *engine.Engine
	mgr     *engine.Manager
	initial string

	ctx     context.Context
	stop    context.CancelFunc
	events  chan tea.Msg // workers report here, so they never touch model state
	workers sync.WaitGroup

	width, height int
	sysDark       bool
	themeChoice   string // light, dark, or empty for system
	th            theme

	conns     map[string]*connState
	tabs      []*tab
	active    int
	focus     focusArea
	overlays  []overlay
	status    string
	statusBad bool
	nextTab   int
	now       func() time.Time
}

// New builds the model. Profile, if set, connects on start.
func New(e *engine.Engine, profile string) *Model {
	ctx, stop := context.WithCancel(context.Background())
	m := &Model{
		e: e, mgr: engine.NewManager(e, maxOpenConns, idleClose), initial: profile,
		ctx: ctx, stop: stop, events: make(chan tea.Msg),
		sysDark: true, conns: map[string]*connState{}, focus: focusEditor, now: time.Now,
	}
	if s, err := e.Store().Settings(); err == nil {
		m.themeChoice = s.Theme
	}
	m.applyTheme()
	return m
}

// Close ends every session and connection. It rolls back open transactions.
func (m *Model) Close() {
	m.stop()
	for _, t := range m.tabs {
		if t.cancel != nil {
			t.cancel()
		}
	}
	// Give each worker time to stop its statement on the server, since closing the connection
	// first would leave it running.
	stopped := make(chan struct{})
	go func() {
		m.workers.Wait()
		close(stopped)
	}()
	select {
	case <-stopped:
		for _, t := range m.tabs {
			if t.console != nil {
				t.console.Close()
			}
		}
	case <-time.After(closeWait):
	}
	m.mgr.Close()
}

// spawn starts a worker that Close waits for.
func (m *Model) spawn(f func()) {
	m.workers.Add(1)
	go func() {
		defer m.workers.Done()
		f()
	}()
}

// Run starts the terminal program and returns when the user quits.
func Run(e *engine.Engine, profile string) error {
	m := New(e, profile)
	defer m.Close()
	_, err := tea.NewProgram(m).Run()
	return err
}

// send reports from a worker. It gives up when the program has ended.
func (m *Model) send(msg tea.Msg) {
	select {
	case m.events <- msg:
	case <-m.ctx.Done():
	}
}

func (m *Model) listen() tea.Cmd {
	return func() tea.Msg {
		select {
		case msg := <-m.events:
			return msg
		case <-m.ctx.Done():
			return nil
		}
	}
}

type tickMsg struct{}

type reapMsg struct{}

func tick() tea.Cmd {
	return tea.Tick(250*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		m.listen(),
		tea.RequestBackgroundColor,
		tea.Every(time.Minute, func(time.Time) tea.Msg { return reapMsg{} }),
	}
	if m.initial != "" {
		cmds = append(cmds, m.connect(m.initial))
		return tea.Batch(cmds...)
	}
	profiles, err := m.e.Store().Profiles()
	switch {
	case err != nil:
		m.setStatus(err.Error(), true)
	case len(profiles) == 0:
		cmds = append(cmds, m.openNewConnection())
	default:
		cmds = append(cmds, m.openConnections())
	}
	return tea.Batch(cmds...)
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	return m, m.update(msg)
}

func (m *Model) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return nil
	case tea.BackgroundColorMsg:
		m.sysDark = msg.IsDark()
		m.applyTheme()
		return nil
	case tea.KeyPressMsg:
		return m.key(msg)
	case tickMsg:
		for _, t := range m.tabs {
			if t.running {
				return tick()
			}
		}
		return nil
	case reapMsg:
		m.mgr.CloseIdle()
		return tea.Every(time.Minute, func(time.Time) tea.Msg { return reapMsg{} })
	case connectedMsg:
		return m.onConnected(msg)
	case treeLoadedMsg:
		return msg.cs.tree.onLoaded(msg)
	case completionMsg:
		m.onCompletion(msg)
		return nil
	case layoutMsg:
		return m.onLayout(msg)
	case planMsg:
		return m.onPlan(msg)
	case formSavedMsg:
		return m.onFormSaved(msg)
	case describedMsg:
		return m.onDescribed(msg)
	}
	if cmd, ok := m.onEvent(msg); ok {
		return tea.Batch(cmd, m.listen())
	}
	// Paste and cursor blinks go to whichever input has focus.
	if len(m.overlays) > 0 {
		return m.overlays[len(m.overlays)-1].update(m, msg)
	}
	if t := m.activeTab(); t != nil && t.console != nil && m.focus == focusEditor {
		var cmd tea.Cmd
		t.editor, cmd = t.editor.Update(msg)
		return cmd
	}
	return nil
}

func (m *Model) key(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	if key == "ctrl+c" || key == "ctrl+q" {
		// A confirmation must be answered, since a statement is waiting on it.
		_, answering := m.topOverlay().(*confirm)
		_, asking := m.topOverlay().(*question)
		if !answering && !asking {
			return m.quit()
		}
	}
	if len(m.overlays) > 0 {
		return m.overlays[len(m.overlays)-1].update(m, msg)
	}
	t := m.activeTab()
	if key == "esc" {
		if t != nil && t.running && t.cancel != nil {
			t.cancel()
			m.setStatus("cancelling…", false)
			return nil
		}
		if t != nil && t.comp != nil {
			t.comp = nil
			return nil
		}
	}
	// With completion open, the list owns its keys.
	if t != nil && t.comp != nil && m.focus == focusEditor {
		if cmd, used := m.completionKey(t, key); used {
			return cmd
		}
	}
	if a := actionForKey(key); a != nil {
		// Tab inserts completion only in the editor. Elsewhere it moves between panes.
		if key == "tab" && m.focus != focusEditor {
			return m.nextFocus()
		}
		return a.run(m)
	}
	switch m.focus {
	case focusTree:
		return m.treeKey(key)
	case focusGrid:
		return m.gridKey(key)
	case focusEditor:
		if t != nil && t.console != nil {
			var cmd tea.Cmd
			t.editor, cmd = t.editor.Update(msg)
			if t.comp != nil {
				return tea.Batch(cmd, m.requestCompletion(t))
			}
			return cmd
		}
	}
	return nil
}

func (m *Model) topOverlay() overlay {
	if len(m.overlays) == 0 {
		return nil
	}
	return m.overlays[len(m.overlays)-1]
}

func (m *Model) activeTab() *tab {
	if m.active < len(m.tabs) {
		return m.tabs[m.active]
	}
	return nil
}

func (m *Model) activeConn() *connState {
	if t := m.activeTab(); t != nil {
		return t.cs
	}
	return nil
}

func (m *Model) setStatus(s string, bad bool) {
	m.status, m.statusBad = ioformat.Display(s), bad
}

func (m *Model) applyTheme() {
	dark := m.sysDark
	switch m.themeChoice {
	case "light":
		dark = false
	case "dark":
		dark = true
	}
	m.th = newTheme(dark)
	for _, t := range m.tabs {
		if t.console != nil {
			t.editor.SetStyles(textarea.DefaultStyles(dark))
		}
	}
}

// cycleTheme steps through system, light, and dark, and remembers the choice.
func (m *Model) cycleTheme() tea.Cmd {
	next := map[string]string{"": "light", "light": "dark", "dark": ""}[m.themeChoice]
	m.themeChoice = next
	m.applyTheme()
	name := next
	if name == "" {
		name = "system"
	}
	st := m.e.Store()
	s, err := st.Settings()
	if err == nil {
		s.Theme = next
		err = st.SaveSettings(s)
	}
	if err != nil {
		m.setStatus("theme not saved: "+err.Error(), true)
		return nil
	}
	m.setStatus("theme: "+name, false)
	return nil
}

func (m *Model) setFocus(f focusArea) tea.Cmd {
	t := m.activeTab()
	if f == focusEditor && (t == nil || t.console == nil) {
		f = focusGrid
	}
	m.focus = f
	return m.syncFocus()
}

func (m *Model) nextFocus() tea.Cmd {
	t := m.activeTab()
	next := map[focusArea]focusArea{focusTree: focusEditor, focusEditor: focusGrid, focusGrid: focusTree}[m.focus]
	if next == focusEditor && (t == nil || t.console == nil) {
		next = focusGrid
	}
	m.focus = next
	return m.syncFocus()
}

func (m *Model) syncFocus() tea.Cmd {
	var cmd tea.Cmd
	for i, t := range m.tabs {
		if t.console == nil {
			continue
		}
		if i == m.active && m.focus == focusEditor && len(m.overlays) == 0 {
			cmd = t.editor.Focus()
		} else {
			t.editor.Blur()
		}
	}
	return cmd
}

func (m *Model) quit() tea.Cmd {
	var risks []string
	for _, t := range m.tabs {
		if t.console != nil && t.console.InTx() {
			risks = append(risks, t.title+" has an open transaction, which will roll back")
		}
		for _, g := range t.results {
			if len(g.pending) > 0 {
				risks = append(risks, t.title+" has edits that were not applied")
			}
		}
	}
	if len(risks) == 0 {
		return tea.Quit
	}
	m.push(newQuestion("Quit?", strings.Join(risks, "\n"), func(yes bool) tea.Cmd {
		if yes {
			return tea.Quit
		}
		return nil
	}))
	return nil
}

func (m *Model) push(o overlay) {
	m.overlays = append(m.overlays, o)
	m.syncFocus()
}

func (m *Model) pop() tea.Cmd {
	if len(m.overlays) > 0 {
		m.overlays = m.overlays[:len(m.overlays)-1]
	}
	return m.syncFocus()
}

// layout gives each editor its share of the window.
func (m *Model) layout() {
	_, _, editorH, _ := m.paneSizes()
	_, rightW := m.widths()
	for _, t := range m.tabs {
		if t.console != nil {
			t.editor.SetWidth(max(1, rightW-2))
			t.editor.SetHeight(max(1, editorH-2))
		}
	}
}

func (m *Model) widths() (tree, right int) {
	tree = max(18, min(32, m.width/4))
	return tree, m.width - tree
}

// paneSizes returns the header lines above the panes, the panes' total height, and the
// editor and grid heights.
func (m *Model) paneSizes() (header, body, editorH, gridH int) {
	header = 1 // tab bar
	if cs := m.activeConn(); cs != nil && cs.profile.Env != "" {
		header++
	}
	body = max(0, m.height-header-1) // status line
	t := m.activeTab()
	if t != nil && t.console == nil {
		return header, body, 0, body
	}
	editorH = max(4, body*2/5)
	return header, body, editorH, max(0, body-editorH)
}

// View draws a frame of exactly the terminal's size.
func (m *Model) View() tea.View {
	v := tea.NewView(m.frame())
	v.AltScreen = true
	v.WindowTitle = "Tessera"
	if cs := m.activeConn(); cs != nil {
		v.WindowTitle = "Tessera: " + ioformat.Display(cs.profile.Name)
	}
	if m.themeChoice != "" {
		v.BackgroundColor = m.th.color("plaster")
		v.ForegroundColor = m.th.color("text")
	}
	return v
}

func (m *Model) frame() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}
	if m.width < minWidth || m.height < minHeight {
		return fit(fmt.Sprintf("Tessera needs at least %d by %d cells.", minWidth, minHeight), m.width)
	}
	var lines []string
	if banner := m.banner(); banner != "" {
		lines = append(lines, banner)
	}
	lines = append(lines, m.tabBar())
	_, body, editorH, gridH := m.paneSizes()
	if len(m.overlays) > 0 {
		o := m.overlays[len(m.overlays)-1]
		drawn := strings.Split(o.view(m, max(10, min(m.width-4, 100)), body), "\n")
		content := strings.Join(firstLines(drawn, body), "\n")
		lines = append(lines, lipgloss.Place(m.width, body, lipgloss.Center, lipgloss.Center, content))
	} else {
		treeW, rightW := m.widths()
		left := m.treePane(treeW, body)
		var right []string
		t := m.activeTab()
		if t != nil && t.console != nil {
			right = append(right, m.editorPane(t, rightW, editorH)...)
		}
		right = append(right, m.gridPane(t, rightW, gridH)...)
		for i := range left {
			line := left[i]
			if i < len(right) {
				line += right[i]
			}
			lines = append(lines, line)
		}
	}
	lines = append(lines, m.statusLine())
	return strings.Join(lines, "\n")
}

func (m *Model) banner() string {
	cs := m.activeConn()
	if cs == nil || cs.profile.Env == "" {
		return ""
	}
	style := m.th.warnBanner
	text := strings.ToUpper(cs.profile.Env)
	if cs.profile.Env == engine.EnvProduction {
		style = m.th.prodBanner
		text += ": every write needs typed confirmation"
	}
	return style.Render(pad(fit(" "+ioformat.Display(text)+" | "+ioformat.Display(cs.profile.Name), m.width), m.width, false))
}

func (m *Model) tabBar() string {
	if len(m.tabs) == 0 {
		return lipgloss.NewStyle().MaxWidth(m.width).Render(m.th.bold.Render("Tessera") + m.th.muted.Render("  no connection. Alt+C connects, F1 shows help"))
	}
	var b strings.Builder
	used := 0
	for i, t := range m.tabs {
		label := " " + t.cs.style.Glyph + " " + ioformat.Display(t.title) + " "
		if t.running {
			label += "… "
		}
		if t.console != nil && t.console.InTx() {
			label += "TX "
		}
		w := lipgloss.Width(label)
		if used+w > m.width {
			break
		}
		used += w
		switch {
		case i == m.active:
			b.WriteString(m.th.cursor.Render(label))
		default:
			b.WriteString(m.th.muted.Render(label))
		}
	}
	return b.String()
}

func (m *Model) statusLine() string {
	var parts []string
	style := m.th.muted
	if cs := m.activeConn(); cs != nil {
		tag := cs.style.Glyph + " " + ioformat.Display(cs.profile.Name)
		if cs.profile.ReadOnly {
			tag += " read-only"
		}
		parts = append(parts, m.th.bright(cs.style.Color).Render(tag))
	}
	if t := m.activeTab(); t != nil {
		if t.running {
			parts = append(parts, m.th.accent.Render(fmt.Sprintf("running %.1fs, Esc cancels", m.now().Sub(t.started).Seconds())))
		}
		if t.console != nil && t.console.InTx() {
			parts = append(parts, m.th.warn.Bold(true).Render("in transaction"))
		}
	}
	if m.status != "" {
		s := style
		if m.statusBad {
			s = m.th.danger.Bold(true)
		}
		parts = append(parts, s.Render(m.status))
	} else {
		parts = append(parts, style.Render("F1 help  Ctrl+K palette  F5 run  Ctrl+Q quit"))
	}
	line := strings.Join(parts, m.th.muted.Render("  |  "))
	if lipgloss.Width(line) > m.width {
		return lipgloss.NewStyle().MaxWidth(m.width).Render(line)
	}
	return line
}

func (t theme) bright(name string) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.color(name)).Bold(true)
}

// pane draws a box of exactly width by height with a title in its top edge.
func (m *Model) pane(title, right string, content []string, width, height int, focused bool) []string {
	border := m.th.muted
	if focused {
		border = m.th.focused
	}
	if height < 2 {
		return nil
	}
	inner := max(0, width-2)
	if focused {
		title = "▶ " + title
	}
	title = fit(title, max(0, inner-3))
	right = fit(right, max(0, inner-3-lipgloss.Width(title)-2))
	fill := max(0, inner-lipgloss.Width(title)-lipgloss.Width(right)-2)
	top := border.Render("╭ "+title+" ") + border.Render(strings.Repeat("─", fill)) + m.th.muted.Render(right) + border.Render("╮")
	if right == "" {
		top = border.Render("╭ " + title + " " + strings.Repeat("─", fill) + "╮")
	}
	out := []string{top}
	side := border.Render("│")
	for i := 0; i < height-2; i++ {
		line := ""
		if i < len(content) {
			line = content[i]
		}
		w := lipgloss.Width(line)
		if w > inner {
			line = lipgloss.NewStyle().MaxWidth(inner).Render(line)
			w = lipgloss.Width(line)
		}
		out = append(out, side+line+strings.Repeat(" ", max(0, inner-w))+side)
	}
	return append(out, border.Render("╰"+strings.Repeat("─", inner)+"╯"))
}

func (m *Model) treePane(width, height int) []string {
	focused := m.focus == focusTree
	cs := m.activeConn()
	if cs == nil {
		return m.pane("Objects", "", []string{m.th.muted.Render(fit("Alt+C connects", width-2))}, width, height, focused)
	}
	return m.pane("Objects", "", cs.tree.view(m.th, width-2, height-2, focused), width, height, focused)
}

func (m *Model) editorPane(t *tab, width, height int) []string {
	focused := m.focus == focusEditor
	content := strings.Split(t.editor.View(), "\n")
	if t.comp != nil {
		content = t.comp.overlayOn(m.th, content, width-2, height-2, t.editor.Line()-t.editor.ScrollYOffset())
	}
	return m.pane("Editor", " F5 runs ", content, width, height, focused)
}

func (m *Model) gridPane(t *tab, width, height int) []string {
	focused := m.focus == focusGrid
	if t == nil {
		hint := []string{
			"Nothing is open yet.",
			"Alt+C picks a connection. Ctrl+K lists every command.",
		}
		for i := range hint {
			hint[i] = m.th.muted.Render(fit(hint[i], width-2))
		}
		return m.pane("Results", "", hint, width, height, focused)
	}
	var content []string
	if t.err != "" {
		for _, l := range wrapText(t.err, width-2) {
			content = append(content, m.th.danger.Render(l))
		}
	}
	title := "Results"
	right := ""
	g := t.grid()
	if g != nil {
		title = g.title
		if t.console != nil && len(t.results) > 1 {
			title = fmt.Sprintf("Result %d of %d: %s", t.current+1, len(t.results), g.title)
		}
		if g.browse != nil && len(g.browse.filter) > 0 {
			title += " where " + describeFilter(g.browse.filter)
		}
		if g.browse != nil && g.browse.loading {
			right = "loading…"
		} else {
			right = g.summary()
		}
		right = " " + right + " "
		content = append(content, g.view(m.th, width-2, height-2-len(content), focused)...)
	}
	if t.info != "" {
		content = append(content, m.th.success.Render(fit(t.info, width-2)))
	}
	if g == nil && t.err == "" && t.info == "" && !t.running {
		content = append(content, m.th.muted.Render(fit("Run a statement to see its result here.", width-2)))
	}
	if g == nil && t.running {
		content = append(content, m.th.muted.Render(fit("waiting for the first row…", width-2)))
	}
	return m.pane(ioformat.Display(title), right, content, width, height, focused)
}

// errText turns an error into what the user reads.
func errText(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, engine.ErrDeclined):
		return "not run: declined"
	}
	return ioformat.Display(err.Error())
}

func wrapText(s string, width int) []string {
	if width <= 0 {
		return nil
	}
	return strings.Split(lipgloss.NewStyle().Width(width).Render(s), "\n")
}

func firstLines(lines []string, n int) []string {
	if len(lines) > n {
		return lines[:n]
	}
	return lines
}
