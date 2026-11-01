package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/arunksoman/tessera/internal/ioformat"
	"github.com/arunksoman/tessera/internal/store"
	"github.com/arunksoman/tessera/tile"
)

// Host-owned fields around each tile's own form. Everything between them comes from
// Manifest.ConnForm, so a new tile gets its form with no front-end code.
const (
	fieldName      = "\x00name"
	fieldTile      = "\x00tile"
	fieldEnv       = "\x00env"
	fieldReadOnly  = "\x00read_only"
	fieldNoHistory = "\x00no_history"
)

type formField struct {
	spec  tile.FormField
	input textinput.Model
	value string // enum and bool fields
}

func (f *formField) text() string {
	if f.spec.Type == "enum" || f.spec.Type == "bool" {
		return f.value
	}
	return f.input.Value()
}

// connForm is the connection form, rendered from data.
type connForm struct {
	m       *Model
	editing string // the profile being edited, if any
	fields  []*formField
	at      int
	err     string
}

func newField(spec tile.FormField, value string) *formField {
	f := &formField{spec: spec, value: value}
	if value == "" && spec.Default != "" {
		f.value = spec.Default
		value = spec.Default
	}
	if spec.Type == "enum" && f.value == "" && len(spec.Options) > 0 {
		f.value = spec.Options[0]
	}
	if spec.Type == "bool" && f.value == "" {
		f.value = "false"
	}
	in := textinput.New()
	in.Prompt = ""
	in.CharLimit = 4096
	in.SetValue(value)
	if spec.Type == "secret" {
		in.EchoMode = textinput.EchoPassword
	}
	f.input = in
	return f
}

func (m *Model) tileIDs() []string {
	var ids []string
	for _, t := range m.e.Tiles() {
		ids = append(ids, t.Manifest().ID)
	}
	return ids
}

func (m *Model) buildForm(editing string, p store.Profile) *connForm {
	if p.Tile == "" {
		if ids := m.tileIDs(); len(ids) > 0 {
			p.Tile = ids[0]
		}
	}
	f := &connForm{m: m, editing: editing}
	f.rebuild(p)
	return f
}

func (f *connForm) rebuild(p store.Profile) {
	m := f.m
	fields := []*formField{
		newField(tile.FormField{Name: fieldName, Label: "Name", Type: "text", Required: true}, p.Name),
		newField(tile.FormField{Name: fieldTile, Label: "Database type", Type: "enum", Options: m.tileIDs()}, p.Tile),
	}
	if t, ok := m.e.Tile(p.Tile); ok {
		for _, spec := range t.Manifest().ConnForm.Fields {
			fields = append(fields, newField(spec, p.Values[spec.Name]))
		}
	}
	fields = append(fields,
		newField(tile.FormField{Name: fieldEnv, Label: "Environment", Type: "enum", Options: []string{"", "development", "staging", "production"}}, p.Env),
		newField(tile.FormField{Name: fieldReadOnly, Label: "Read-only", Type: "bool"}, fmt.Sprint(p.ReadOnly)),
		newField(tile.FormField{Name: fieldNoHistory, Label: "Keep no history", Type: "bool"}, fmt.Sprint(p.NoHistory)),
	)
	f.fields = fields
	f.at = 0
	f.focus()
}

func (f *connForm) focus() tea.Cmd {
	var cmd tea.Cmd
	for i, fl := range f.fields {
		if i == f.at {
			cmd = fl.input.Focus()
		} else {
			fl.input.Blur()
		}
	}
	return cmd
}

// profile reads the form back. Secrets are returned apart, since they never enter a profile.
func (f *connForm) profile() (store.Profile, map[string]string) {
	p := store.Profile{Values: map[string]string{}}
	secrets := map[string]string{}
	for _, fl := range f.fields {
		v := fl.text()
		switch fl.spec.Name {
		case fieldName:
			p.Name = strings.TrimSpace(v)
		case fieldTile:
			p.Tile = v
		case fieldEnv:
			p.Env = v
		case fieldReadOnly:
			p.ReadOnly = v == "true"
		case fieldNoHistory:
			p.NoHistory = v == "true"
		default:
			if fl.spec.Type == "secret" {
				if v != "" {
					secrets[fl.spec.Name] = v
				}
			} else if v = strings.TrimSpace(v); v != "" {
				p.Values[fl.spec.Name] = v
			}
		}
	}
	return p, secrets
}

// validate checks the rules the form declares.
func (f *connForm) validate() error {
	for _, fl := range f.fields {
		v := strings.TrimSpace(fl.text())
		if fl.spec.Required && v == "" {
			return fmt.Errorf("%s is required", fl.spec.Label)
		}
		if fl.spec.Type == "int" && v != "" {
			for _, r := range v {
				if r < '0' || r > '9' {
					return fmt.Errorf("%s must be a number", fl.spec.Label)
				}
			}
		}
	}
	return nil
}

func (f *connForm) update(m *Model, msg tea.Msg) tea.Cmd {
	key, isKey := msg.(tea.KeyPressMsg)
	cur := f.fields[f.at]
	if isKey {
		s := key.String()
		switch s {
		case "esc":
			return m.pop()
		case "ctrl+s":
			return f.save(m)
		case "down", "tab":
			f.at = min(len(f.fields)-1, f.at+1)
			return f.focus()
		case "up", "shift+tab":
			f.at = max(0, f.at-1)
			return f.focus()
		case "enter":
			if f.at == len(f.fields)-1 {
				return f.save(m)
			}
			f.at++
			return f.focus()
		}
		switch cur.spec.Type {
		case "enum":
			if s == "left" || s == "right" || s == "space" {
				f.stepEnum(cur, s)
			}
			return nil
		case "bool":
			if s == "left" || s == "right" || s == "space" {
				cur.value = map[string]string{"true": "false", "false": "true"}[cur.value]
			}
			return nil
		}
	}
	if cur.spec.Type == "enum" || cur.spec.Type == "bool" {
		return nil
	}
	var cmd tea.Cmd
	cur.input, cmd = cur.input.Update(msg)
	return cmd
}

func (f *connForm) stepEnum(fl *formField, key string) {
	opts := fl.spec.Options
	if len(opts) == 0 {
		return
	}
	i := slices.Index(opts, fl.value)
	if key == "left" {
		i = (i - 1 + len(opts)) % len(opts)
	} else {
		i = (i + 1) % len(opts)
	}
	fl.value = opts[i]
	// A different database type has a different form.
	if fl.spec.Name == fieldTile {
		p, _ := f.profile()
		p.Values = nil
		at := f.at
		f.rebuild(p)
		f.at = at
		f.focus()
	}
}

type formSavedMsg struct {
	form    *connForm
	profile string
	err     error
}

func (f *connForm) save(m *Model) tea.Cmd {
	if err := f.validate(); err != nil {
		f.err = err.Error()
		return nil
	}
	p, secrets := f.profile()
	e := m.e
	editing := f.editing
	return func() tea.Msg {
		st := e.Store()
		if err := st.SaveProfile(p); err != nil {
			return formSavedMsg{form: f, err: err}
		}
		for name, value := range secrets {
			if err := e.SetSecret(p.Name, name, []byte(value)); err != nil {
				return formSavedMsg{form: f, err: err}
			}
		}
		if editing != "" && editing != p.Name {
			for _, fl := range f.fields {
				if _, typed := secrets[fl.spec.Name]; fl.spec.Type == "secret" && !typed {
					if err := e.MoveSecret(editing, p.Name, fl.spec.Name); err != nil {
						return formSavedMsg{form: f, err: err}
					}
				}
			}
			st.DeleteProfile(editing)
		}
		m.mgr.Forget(p.Name)
		// Connecting now means a mistake shows here, while the form is still open.
		ctx, cancel := context.WithTimeout(m.ctx, connTimeout)
		defer cancel()
		conn, err := e.Open(ctx, p.Name)
		if err != nil {
			return formSavedMsg{form: f, profile: p.Name, err: fmt.Errorf("saved, but could not connect: %w", err)}
		}
		conn.Close()
		return formSavedMsg{form: f, profile: p.Name}
	}
}

func (m *Model) onFormSaved(msg formSavedMsg) tea.Cmd {
	if msg.err != nil {
		msg.form.err = errText(msg.err)
		return nil
	}
	if len(m.overlays) > 0 && m.overlays[len(m.overlays)-1] == overlay(msg.form) {
		m.pop()
	}
	// Close an open connection picker behind the form too, as it is now out of date.
	if len(m.overlays) > 0 {
		if _, ok := m.overlays[len(m.overlays)-1].(*picker); ok {
			m.pop()
		}
	}
	return m.connect(msg.profile)
}

func (f *connForm) view(m *Model, width, height int) string {
	title := "New connection"
	if f.editing != "" {
		title = "Edit " + ioformat.Display(f.editing)
	}
	const labelW = 26
	var lines []string
	for i, fl := range f.fields {
		label := fl.spec.Label
		if fl.spec.Required {
			label += " *"
		}
		label = pad(fit(label, labelW-1), labelW, false)
		value := ""
		switch fl.spec.Type {
		case "enum":
			shown := fl.value
			if shown == "" {
				shown = "(none)"
			}
			value = "< " + shown + " >"
		case "bool":
			value = "[ ]"
			if fl.value == "true" {
				value = "[x]"
			}
		case "secret":
			fl.input.SetWidth(max(1, width-labelW-8))
			value = fl.input.View()
			if f.editing != "" && fl.input.Value() == "" {
				value += m.th.muted.Render(" unchanged if blank")
			}
		default:
			fl.input.SetWidth(max(1, width-labelW-8))
			value = fl.input.View()
		}
		if i == f.at {
			lines = append(lines, m.th.focused.Render("> "+label)+value)
		} else {
			lines = append(lines, "  "+m.th.text.Render(label)+value)
		}
	}
	rows := max(1, height-8)
	start := max(0, min(f.at-rows/2, len(lines)-rows))
	shown := lines[start:min(len(lines), start+rows)]
	out := append([]string{}, shown...)
	if f.err != "" {
		out = append(out, "")
		out = append(out, m.th.danger.Render(fit(ioformat.Display(f.err), width-4)))
	}
	out = append(out, "", m.th.muted.Render(fit("Up/Down move, Left/Right choose, Ctrl+S saves and connects, Esc closes", width-4)))
	return box(m, title, out, width)
}

func (m *Model) openNewConnection() tea.Cmd {
	f := m.buildForm("", store.Profile{})
	m.push(f)
	return f.focus()
}

func (m *Model) openEditConnection(p store.Profile) tea.Cmd {
	f := m.buildForm(p.Name, p)
	m.push(f)
	return f.focus()
}
