package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// action is one entry in the registry that drives keys, the palette, and help alike.
// The key map is section 12 of the design. Terminals that cannot report a primary key use
// its fallback, listed after it.
type action struct {
	title string
	keys  []string
	run   func(m *Model) tea.Cmd
}

var actions []action

// Set here because some actions list the registry itself.
func init() {
	actions = []action{
		{"Command palette", []string{"ctrl+k"}, (*Model).openPalette},
		{"Run statement at cursor", []string{"ctrl+enter", "f5"}, func(m *Model) tea.Cmd { return m.runEditor(false) }},
		{"Run whole script", []string{"f6"}, func(m *Model) tea.Cmd { return m.runEditor(true) }},
		{"Explain statement", []string{"f7"}, func(m *Model) tea.Cmd { return m.explain(false) }},
		{"Explain statement as JSON", []string{"f8"}, func(m *Model) tea.Cmd { return m.explain(true) }},
		{"Complete at cursor", []string{"tab", "ctrl+space"}, (*Model).completeKey},
		{"New tab", []string{"alt+t"}, (*Model).newTab},
		{"Close tab", []string{"alt+w"}, (*Model).closeTab},
		{"Next tab", []string{"alt+]"}, func(m *Model) tea.Cmd { return m.cycleTab(1) }},
		{"Previous tab", []string{"alt+["}, func(m *Model) tea.Cmd { return m.cycleTab(-1) }},
		{"Switch connection", []string{"alt+c"}, (*Model).openConnections},
		{"New connection", nil, (*Model).openNewConnection},
		{"Apply pending edits", []string{"ctrl+s"}, (*Model).applyPending},
		{"Focus tree", []string{"alt+1"}, func(m *Model) tea.Cmd { return m.setFocus(focusTree) }},
		{"Focus editor", []string{"alt+2"}, func(m *Model) tea.Cmd { return m.setFocus(focusEditor) }},
		{"Focus grid", []string{"alt+3"}, func(m *Model) tea.Cmd { return m.setFocus(focusGrid) }},
		{"Focus next pane", []string{"shift+tab"}, (*Model).nextFocus},
		{"Statement history", []string{"ctrl+r"}, (*Model).openHistory},
		{"Refresh object tree", nil, (*Model).refreshTree},
		{"Theme: system, light, dark", []string{"f9"}, (*Model).cycleTheme},
		{"Help", []string{"f1"}, (*Model).openHelp},
		{"Quit", []string{"ctrl+q"}, (*Model).quit},
	}
}

// paneHelp lists keys that belong to one pane, shown in help.
var paneHelp = []string{
	"Any pane    Esc cancels a running query",
	"Tree        arrows or hjkl move, Enter opens, d describes, r refreshes",
	"Grid        arrows or hjkl move, PgUp/PgDn, g and G jump, Enter edits or shows a cell, v shows it",
	"Grid        s sorts, f or / filters, c clears filters, u undoes an edit, [ and ] change result",
	"Edit box    Enter saves, Alt+Enter adds a line, Ctrl+N sets NULL, Esc cancels",
}

func actionForKey(key string) *action {
	for i := range actions {
		for _, k := range actions[i].keys {
			if k == key {
				return &actions[i]
			}
		}
	}
	return nil
}

func (a action) keyLabel() string { return strings.Join(a.keys, " / ") }
