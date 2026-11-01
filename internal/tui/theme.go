package tui

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"image/color"

	"charm.land/lipgloss/v2"
)

// tokens.json is the one source of colors from section 12 of the design. The web frontend is
// to generate its CSS variables from the same file, so the two cannot drift.
//
//go:embed tokens.json
var tokensJSON []byte

type tokenPair struct{ Light, Dark string }

var tokens = func() map[string]tokenPair {
	var t map[string]tokenPair
	if err := json.Unmarshal(tokensJSON, &t); err != nil {
		panic(fmt.Sprintf("tokens.json: %v", err))
	}
	return t
}()

// theme holds styles for one background. The renderer downgrades colors to what the terminal
// supports and drops them under NO_COLOR, so every state also has a non-color signal: bold,
// reverse video, a marker character, or a text label.
type theme struct {
	dark                                bool
	text, muted, accent, danger, warn   lipgloss.Style
	success, bold, cursor, focused, dim lipgloss.Style
	prodBanner, warnBanner, infoBanner  lipgloss.Style
}

func (t theme) color(name string) color.Color {
	pair, ok := tokens[name]
	if !ok {
		pair = tokens["text"]
	}
	if t.dark {
		return lipgloss.Color(pair.Dark)
	}
	return lipgloss.Color(pair.Light)
}

func newTheme(dark bool) theme {
	t := theme{dark: dark}
	fg := func(name string) lipgloss.Style { return lipgloss.NewStyle().Foreground(t.color(name)) }
	t.text = fg("text")
	t.muted = fg("muted")
	t.accent = fg("lapis")
	t.danger = fg("danger")
	t.warn = fg("warning")
	t.success = fg("success")
	t.bold = fg("text").Bold(true)
	t.dim = fg("muted")
	t.focused = fg("lapis").Bold(true)
	t.cursor = lipgloss.NewStyle().Reverse(true).Bold(true)
	// White on #B3261E is 6.5:1. Danger is reserved for this and for errors.
	t.prodBanner = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFFFF")).Background(lipgloss.Color("#B3261E")).Bold(true)
	t.warnBanner = lipgloss.NewStyle().Foreground(lipgloss.Color("#1C1F24")).Background(lipgloss.Color("#E5B84B")).Bold(true)
	t.infoBanner = lipgloss.NewStyle().Reverse(true)
	return t
}
