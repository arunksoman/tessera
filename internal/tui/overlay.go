package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/arunksoman/tessera/internal/engine"
	"github.com/arunksoman/tessera/internal/ioformat"
	"github.com/arunksoman/tessera/tile"
)

// overlay is a modal screen. The top one receives all input.
type overlay interface {
	update(m *Model, msg tea.Msg) tea.Cmd
	view(m *Model, width, height int) string
}

// box frames overlay content with a title.
func box(m *Model, title string, lines []string, width int) string {
	width = max(10, width)
	inner := width - 4
	out := make([]string, 0, len(lines)+1)
	out = append(out, m.th.focused.Render(fit(title, inner)), "")
	for _, l := range lines {
		if lipgloss.Width(l) > inner {
			l = lipgloss.NewStyle().MaxWidth(inner).Render(l)
		}
		out = append(out, l)
	}
	style := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(m.th.color("lapis")).Padding(0, 1).Width(width)
	return style.Render(strings.Join(out, "\n"))
}

func newInput(placeholder string) textinput.Model {
	in := textinput.New()
	in.Prompt = "> "
	in.Placeholder = placeholder
	in.SetWidth(60)
	in.Focus()
	return in
}

// picker is a filtered list, used for connections, the palette, and history.
type picker struct {
	title  string
	hint   string
	input  textinput.Model
	items  []pickItem
	cursor int
	top    int
	onEdit func(item pickItem) tea.Cmd
}

type pickItem struct {
	label, detail string
	run           func() tea.Cmd
}

func newPicker(title, hint string, items []pickItem) *picker {
	return &picker{title: title, hint: hint, input: newInput("type to filter"), items: items}
}

func (p *picker) matches() []pickItem {
	q := strings.ToLower(strings.TrimSpace(p.input.Value()))
	var out []pickItem
	for _, it := range p.items {
		if q == "" || strings.Contains(strings.ToLower(it.label+" "+it.detail), q) {
			out = append(out, it)
		}
	}
	return out
}

func (p *picker) update(m *Model, msg tea.Msg) tea.Cmd {
	key, isKey := msg.(tea.KeyPressMsg)
	if isKey {
		items := p.matches()
		switch key.String() {
		case "esc":
			return m.pop()
		case "up":
			p.cursor = max(0, p.cursor-1)
			return nil
		case "down":
			p.cursor = min(len(items)-1, p.cursor+1)
			return nil
		case "enter":
			if p.cursor < len(items) {
				run := items[p.cursor].run
				return tea.Sequence(m.pop(), run())
			}
			return nil
		case "ctrl+e":
			if p.onEdit != nil && p.cursor < len(items) {
				return p.onEdit(items[p.cursor])
			}
			return nil
		}
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	if isKey {
		p.cursor = max(0, min(p.cursor, len(p.matches())-1))
	}
	return cmd
}

func (p *picker) view(m *Model, width, height int) string {
	items := p.matches()
	rows := max(1, height-8)
	if p.cursor < p.top {
		p.top = p.cursor
	}
	if p.cursor >= p.top+rows {
		p.top = p.cursor - rows + 1
	}
	lines := []string{p.input.View(), ""}
	inner := width - 4
	for i := p.top; i < len(items) && i < p.top+rows; i++ {
		label := items[i].label
		if items[i].detail != "" {
			label += "  " + m.th.muted.Render(items[i].detail)
		}
		if i == p.cursor {
			plain := items[i].label
			if items[i].detail != "" {
				plain += "  " + items[i].detail
			}
			label = m.th.cursor.Render(pad(fit(plain, inner), inner, false))
		}
		lines = append(lines, label)
	}
	if len(items) == 0 {
		lines = append(lines, m.th.muted.Render("nothing matches"))
	}
	if p.hint != "" {
		lines = append(lines, "", m.th.muted.Render(p.hint))
	}
	return box(m, p.title, lines, width)
}

func (m *Model) openPalette() tea.Cmd {
	items := make([]pickItem, 0, len(actions))
	for _, a := range actions {
		items = append(items, pickItem{label: a.title, detail: a.keyLabel(), run: func() tea.Cmd { return a.run(m) }})
	}
	m.push(newPicker("Command palette", "Enter runs, Esc closes", items))
	return nil
}

func (m *Model) openConnections() tea.Cmd {
	profiles, err := m.e.Store().Profiles()
	if err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	items := make([]pickItem, 0, len(profiles)+1)
	for _, prof := range profiles {
		detail := prof.Tile
		if h := prof.Values["host"]; h != "" {
			detail += " " + ioformat.Display(h)
		}
		if prof.Env != "" {
			detail += " [" + ioformat.Display(prof.Env) + "]"
		}
		if prof.ReadOnly {
			detail += " [read-only]"
		}
		items = append(items, pickItem{label: ioformat.Display(prof.Name), detail: detail, run: func() tea.Cmd { return m.connect(prof.Name) }})
	}
	items = append(items, pickItem{label: "+ New connection", run: m.openNewConnection})
	p := newPicker("Connections", "Enter connects, Ctrl+E edits, Esc closes", items)
	p.onEdit = func(it pickItem) tea.Cmd {
		for _, prof := range profiles {
			if ioformat.Display(prof.Name) == it.label {
				return m.openEditConnection(prof)
			}
		}
		return nil
	}
	m.push(p)
	return nil
}

func (m *Model) openHistory() tea.Cmd {
	t := m.consoleTab()
	if t == nil {
		return nil
	}
	entries, err := m.e.Store().History(t.cs.profile.Name, 200)
	if err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	items := make([]pickItem, 0, len(entries))
	for _, e := range entries {
		stmt := e.Statement
		detail := e.At.Format(time.DateTime)
		if e.Error != "" {
			detail += " failed"
		}
		items = append(items, pickItem{label: ioformat.Display(stmt), detail: detail, run: func() tea.Cmd {
			t.editor.SetValue(stmt)
			return nil
		}})
	}
	m.push(newPicker("Statement history", "Enter copies it to the editor, Esc closes", items))
	return nil
}

func (m *Model) openHelp() tea.Cmd {
	var b strings.Builder
	for _, a := range actions {
		label := a.keyLabel()
		if label == "" {
			label = "palette"
		}
		fmt.Fprintf(&b, "%-24s %s\n", label, a.title)
	}
	b.WriteString("\n" + strings.Join(paneHelp, "\n"))
	m.push(newTextView("Help · tess"+m.versionLabel(), b.String()))
	return nil
}

// textView shows scrollable text.
type textView struct {
	title, text string
	top         int
}

func newTextView(title, text string) *textView { return &textView{title: title, text: text} }

func (v *textView) update(m *Model, msg tea.Msg) tea.Cmd {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	switch key.String() {
	case "esc", "enter", "q", "f1":
		return m.pop()
	case "up", "k":
		v.top = max(0, v.top-1)
	case "down", "j":
		v.top++
	case "pgup":
		v.top = max(0, v.top-10)
	case "pgdown":
		v.top += 10
	}
	return nil
}

func (v *textView) view(m *Model, width, height int) string {
	lines := wrapText(v.text, width-4)
	rows := max(1, height-6)
	v.top = max(0, min(v.top, len(lines)-rows))
	end := min(len(lines), v.top+rows)
	out := append([]string{}, lines[v.top:end]...)
	out = append(out, "", m.th.muted.Render(fmt.Sprintf("lines %d-%d of %d. Arrows scroll, Esc closes", v.top+1, end, len(lines))))
	return box(m, v.title, out, width)
}

// question is a yes or no prompt.
type question struct {
	title, body string
	answer      func(bool) tea.Cmd
}

func newQuestion(title, body string, answer func(bool) tea.Cmd) *question {
	return &question{title, body, answer}
}

func (q *question) update(m *Model, msg tea.Msg) tea.Cmd {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	switch key.String() {
	case "y", "Y":
		return tea.Sequence(m.pop(), q.answer(true))
	case "n", "N", "esc":
		return tea.Sequence(m.pop(), q.answer(false))
	}
	return nil
}

func (q *question) view(m *Model, width, _ int) string {
	lines := wrapText(ioformat.Display(strings.ReplaceAll(q.body, "\n", " ")), width-4)
	lines = append(lines, "", m.th.muted.Render("y yes, n no"))
	return box(m, q.title, lines, width)
}

// confirm is the engine gate's question: the user types a word to run the statement.
type confirm struct {
	msg   askMsg
	want  string
	input textinput.Model
	wrong bool
}

func newConfirm(msg askMsg) *confirm {
	want := "yes"
	if msg.p.Env == engine.EnvProduction {
		want = msg.cs.profile.Name
	}
	return &confirm{msg: msg, want: want, input: newInput(want)}
}

func (c *confirm) update(m *Model, msg tea.Msg) tea.Cmd {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "esc", "ctrl+c":
			c.msg.reply <- false
			return m.pop()
		case "enter":
			if strings.TrimSpace(c.input.Value()) == c.want {
				c.msg.reply <- true
				return m.pop()
			}
			c.wrong = true
			return nil
		}
	}
	var cmd tea.Cmd
	c.input, cmd = c.input.Update(msg)
	return cmd
}

func (c *confirm) view(m *Model, width, height int) string {
	var lines []string
	if c.msg.p.Env == engine.EnvProduction {
		lines = append(lines, m.th.prodBanner.Render(" PRODUCTION: "+ioformat.Display(c.msg.cs.profile.Name)+" "), "")
	}
	lines = append(lines, m.th.warn.Bold(true).Render(ioformat.Display(c.msg.p.Class.Reason)), "")
	stmt := wrapText(ioformat.Display(c.msg.p.Statement), width-4)
	lines = append(lines, stmt[:min(len(stmt), max(1, height-12))]...)
	lines = append(lines, "", fmt.Sprintf("Type %q to run it, or Esc to refuse:", ioformat.Display(c.want)), c.input.View())
	if c.wrong {
		lines = append(lines, m.th.danger.Render("that does not match"))
	}
	return box(m, "Confirm", lines, width)
}

// filterPrompt adds a condition on the current column.
type filterPrompt struct {
	t     *tab
	g     *grid
	input textinput.Model
	err   string
}

func newFilterPrompt(t *tab, g *grid) *filterPrompt {
	return &filterPrompt{t: t, g: g, input: newInput("= value, != , > , < , ~ contains, null, !null")}
}

func (f *filterPrompt) update(m *Model, msg tea.Msg) tea.Cmd {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "esc":
			return m.pop()
		case "enter":
			c, err := parseFilter(f.g.fields[f.g.col].Name, f.input.Value())
			if err != nil {
				f.err = err.Error()
				return nil
			}
			f.g.browse.filter = append(f.g.browse.filter, c)
			return tea.Sequence(m.pop(), m.reload(f.t, f.g))
		}
	}
	var cmd tea.Cmd
	f.input, cmd = f.input.Update(msg)
	return cmd
}

func (f *filterPrompt) view(m *Model, width, _ int) string {
	lines := []string{f.input.View()}
	if f.err != "" {
		lines = append(lines, m.th.danger.Render(f.err))
	}
	lines = append(lines, "", m.th.muted.Render("Bare text means contains. Enter applies, Esc closes."))
	return box(m, "Filter "+ioformat.Display(f.g.fields[f.g.col].Name), lines, width)
}

// cellEdit changes one cell in memory. Nothing reaches the database until apply.
type cellEdit struct {
	g        *grid
	row, col int
	old      tile.Value
	area     textarea.Model
}

func newCellEdit(g *grid, row, col int, v tile.Value, dark bool) *cellEdit {
	a := textarea.New()
	a.Prompt = ""
	a.ShowLineNumbers = false
	a.CharLimit = 0
	a.MaxHeight = 0
	a.SetStyles(textarea.DefaultStyles(dark))
	a.SetHeight(5)
	a.KeyMap.InsertNewline.SetKeys("alt+enter")
	if v.Kind != tile.KindNull {
		a.SetValue(string(v.Raw))
	}
	a.Focus()
	return &cellEdit{g: g, row: row, col: col, old: v, area: a}
}

func (e *cellEdit) update(m *Model, msg tea.Msg) tea.Cmd {
	at := [2]int{e.row, e.col}
	original := e.g.rows[e.row][e.col]
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "esc":
			return m.pop()
		case "ctrl+n":
			if original.Kind == tile.KindNull {
				delete(e.g.pending, at)
			} else {
				e.g.pending[at] = tile.Value{Kind: tile.KindNull}
				e.g.widths[e.col] = max(e.g.widths[e.col], len("NULL*"))
			}
			return m.pop()
		case "enter":
			text := e.area.Value()
			if original.Kind != tile.KindNull && text == string(original.Raw) {
				delete(e.g.pending, at)
			} else if original.Kind == tile.KindNull && text == "" {
				delete(e.g.pending, at)
			} else {
				e.g.pending[at] = tile.Value{Kind: tile.KindText, Raw: []byte(text)}
				// Room for the value and its pending marker.
				e.g.widths[e.col] = max(e.g.widths[e.col], min(maxColWidth, lipgloss.Width(ioformat.Display(text))+1))
			}
			return m.pop()
		}
	}
	var cmd tea.Cmd
	e.area, cmd = e.area.Update(msg)
	return cmd
}

func (e *cellEdit) view(m *Model, width, _ int) string {
	e.area.SetWidth(max(1, width-4))
	lines := strings.Split(e.area.View(), "\n")
	lines = append(lines, "", m.th.muted.Render("Enter keeps it pending, Alt+Enter adds a line, Ctrl+N sets NULL, Esc cancels"))
	return box(m, "Edit "+ioformat.Display(e.g.fields[e.col].Name), lines, width)
}

// preview shows exactly what apply will run.
type preview struct {
	t    *tab
	g    *grid
	plan *engine.Pending
	top  int
}

func newPreview(t *tab, g *grid, plan *engine.Pending) *preview {
	return &preview{t: t, g: g, plan: plan}
}

func (p *preview) update(m *Model, msg tea.Msg) tea.Cmd {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	switch key.String() {
	case "esc", "n":
		return m.pop()
	case "up", "k":
		p.top = max(0, p.top-1)
	case "down", "j":
		p.top++
	case "y", "enter":
		return tea.Sequence(m.pop(), m.startApply(p.t, p.g, p.plan))
	}
	return nil
}

func (p *preview) view(m *Model, width, height int) string {
	atomicity := "All changes run in one transaction: any conflict rolls everything back."
	if p.plan.Plan.Atomicity != tile.AtomicAll {
		atomicity = "Each change commits on its own: a failure leaves earlier changes in place."
	}
	var body []string
	for i, st := range p.plan.Plan.Steps {
		body = append(body, wrapText(fmt.Sprintf("%d. %s", i+1, ioformat.Display(st.Preview)), width-4)...)
	}
	rows := max(1, height-8)
	p.top = max(0, min(p.top, len(body)-rows))
	lines := []string{m.th.muted.Render(atomicity), ""}
	lines = append(lines, body[p.top:min(len(body), p.top+rows)]...)
	lines = append(lines, "", m.th.muted.Render("Values are shown as literals for reading. They are sent bound. y applies, Esc goes back"))
	return box(m, "Apply "+count(len(p.plan.Plan.Steps), "change")+"?", lines, width)
}

// Completion.

type completion struct {
	prefix string
	items  []tile.Candidate
	cursor int
}

type completionMsg struct {
	t      *tab
	prefix string
	items  []tile.Candidate
	opened bool
}

func (m *Model) completeKey() tea.Cmd {
	t := m.activeTab()
	if t == nil || t.console == nil || m.focus != focusEditor {
		return nil
	}
	return m.requestCompletion(t)
}

func (m *Model) requestCompletion(t *tab) tea.Cmd {
	text, offset := t.editor.Value(), editorOffset(&t.editor)
	cs := t.cs
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 3*time.Second)
		defer cancel()
		prefix, items := cs.conn.Complete(ctx, text, offset)
		return completionMsg{t: t, prefix: prefix, items: items}
	}
}

func (m *Model) onCompletion(msg completionMsg) {
	if len(msg.items) == 0 {
		msg.t.comp = nil
		return
	}
	msg.t.comp = &completion{prefix: msg.prefix, items: msg.items}
}

// completionKey handles keys while the list is open. Anything else goes to the editor.
func (m *Model) completionKey(t *tab, key string) (tea.Cmd, bool) {
	c := t.comp
	switch key {
	case "up":
		c.cursor = max(0, c.cursor-1)
	case "down":
		c.cursor = min(len(c.items)-1, c.cursor+1)
	case "enter", "tab":
		chosen := c.items[c.cursor].Text
		for range []rune(c.prefix) {
			t.editor, _ = t.editor.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
		}
		t.editor.InsertString(chosen)
		t.comp = nil
	default:
		return nil, false
	}
	return nil, true
}

const compRows = 6

// overlayOn draws the list over the editor, on the side away from the cursor line.
func (c *completion) overlayOn(th theme, content []string, width, height, cursorLine int) []string {
	rows := min(compRows, max(1, height-1), len(c.items))
	start := max(0, min(c.cursor-rows+1, len(c.items)-rows))
	out := make([]string, max(height, len(content)))
	copy(out, content)
	at := cursorLine + 1
	if at+rows > height {
		at = max(0, cursorLine-rows)
	}
	for i := 0; i < rows; i++ {
		item := c.items[start+i]
		text := pad(fit(ioformat.Display(item.Text)+"  "+item.Kind, min(width, 40)), min(width, 40), false)
		if start+i == c.cursor {
			text = th.cursor.Render(text)
		} else {
			text = th.accent.Render(text)
		}
		out[at+i] = text
	}
	return out
}
