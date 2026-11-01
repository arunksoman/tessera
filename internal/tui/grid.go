package tui

import (
	"fmt"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/arunksoman/tessera/internal/ioformat"
	"github.com/arunksoman/tessera/tile"
)

const (
	// shortcut: rows are held in memory up to these caps, then the query is cancelled and the
	// grid says it was truncated. Replace with the encrypted spool when it exists.
	maxGridRows  = 50_000
	maxGridBytes = 64 << 20

	browsePage  = 500
	loadMargin  = 50 // rows from the end at which the next page loads
	maxColWidth = 40
	minColWidth = 4
	widthSample = 200
	maxCellScan = 1024 // bytes of a cell inspected to draw it
)

// grid is one result: rows, a cursor, and any pending edits.
type grid struct {
	title     string
	fields    []tile.Field
	rows      [][]tile.Value
	widths    []int
	row, col  int
	top, left int
	note      string
	truncated bool
	pending   map[[2]int]tile.Value
	browse    *browse // nil for a console result
}

// browse is the state of a grid paging through one object.
type browse struct {
	ref       tile.ObjectRef
	layout    tile.Layout
	sort      []tile.SortKey
	filter    []tile.Condition
	gen       int // bumped on reload, so a stale page is dropped
	loading   bool
	exhausted bool
}

func (b *browse) editable() bool { return len(b.layout.Key) > 0 }

func newGrid(title string, fields []tile.Field) *grid {
	g := &grid{title: title, fields: slices.Clone(fields), pending: map[[2]int]tile.Value{}}
	g.widths = make([]int, len(fields))
	for i, f := range fields {
		g.widths[i] = max(minColWidth, min(maxColWidth, lipgloss.Width(ioformat.Display(f.Name))+2))
	}
	return g
}

// copyRows detaches a batch from the driver's buffers, which are valid only until the next frame.
func copyRows(b *tile.Batch, cols int) [][]tile.Value {
	if cols == 0 {
		return nil
	}
	size := 0
	for _, c := range b.Cells {
		size += len(c.Raw)
	}
	arena := make([]byte, 0, size)
	cells := make([]tile.Value, len(b.Cells))
	for i, c := range b.Cells {
		cells[i].Kind = c.Kind
		if c.Kind != tile.KindNull {
			from := len(arena)
			arena = append(arena, c.Raw...)
			cells[i].Raw = arena[from:len(arena):len(arena)]
		}
	}
	rows := make([][]tile.Value, b.Rows)
	for r := range rows {
		rows[r] = cells[r*cols : (r+1)*cols : (r+1)*cols]
	}
	return rows
}

func (g *grid) add(rows [][]tile.Value) {
	for _, r := range rows {
		if len(g.rows) < widthSample {
			for i, v := range r {
				if i < len(g.widths) {
					// A browse layout has no kinds until values arrive.
					if g.fields[i].Kind == tile.KindNull {
						g.fields[i].Kind = v.Kind
					}
					g.widths[i] = max(g.widths[i], min(maxColWidth, lipgloss.Width(cellPreview(v))))
				}
			}
		}
		g.rows = append(g.rows, r)
	}
}

// cellPreview renders a cell without scanning all of a huge value.
func cellPreview(v tile.Value) string {
	if v.Kind != tile.KindBytes && len(v.Raw) > maxCellScan {
		v.Raw = v.Raw[:maxCellScan]
	}
	return ioformat.CellText(v)
}

// fit truncates s to width display columns.
func fit(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		w := lipgloss.Width(string(r))
		if used+w > width-1 {
			break
		}
		b.WriteRune(r)
		used += w
	}
	b.WriteString("…")
	return b.String()
}

func pad(s string, width int, right bool) string {
	gap := width - lipgloss.Width(s)
	if gap <= 0 {
		return s
	}
	if right {
		return strings.Repeat(" ", gap) + s
	}
	return s + strings.Repeat(" ", gap)
}

func isNumeric(k tile.ValueKind) bool {
	return k == tile.KindInt || k == tile.KindDecimal || k == tile.KindFloat
}

// move shifts the cursor and clamps it to the grid.
func (g *grid) move(rows, cols int) {
	g.row = max(0, min(len(g.rows)-1, g.row+rows))
	g.col = max(0, min(len(g.fields)-1, g.col+cols))
}

// keepVisible scrolls so the cursor is on screen for a body of the given size.
func (g *grid) keepVisible(width, height int) {
	height = max(height, 1)
	if g.row < g.top {
		g.top = g.row
	}
	if g.row >= g.top+height {
		g.top = g.row - height + 1
	}
	if g.col < g.left {
		g.left = g.col
	}
	for g.left < g.col {
		used := 0
		for i := g.left; i <= g.col; i++ {
			used += g.widths[i] + 1
		}
		if used <= width {
			break
		}
		g.left++
	}
}

func (g *grid) sortMark(name string) string {
	if g.browse != nil {
		for _, s := range g.browse.sort {
			if s.Field == name {
				if s.Desc {
					return " ▼"
				}
				return " ▲"
			}
		}
	}
	return ""
}

// view draws the grid into width by height cells, one string per line.
func (g *grid) view(th theme, width, height int, focused bool) []string {
	lines := make([]string, 0, height)
	if g.note != "" {
		lines = append(lines, th.muted.Render(fit(ioformat.Display(g.note), width)))
	}
	if len(g.fields) == 0 {
		return lines
	}
	body := height - len(lines) - 2
	g.keepVisible(width, body)

	var head, rule strings.Builder
	last := g.left
	used := 0
	for i := g.left; i < len(g.fields); i++ {
		w := g.widths[i]
		if used+w > width && i > g.left {
			break
		}
		w = min(w, width-used)
		last = i
		label := fit(ioformat.Display(g.fields[i].Name)+g.sortMark(g.fields[i].Name), w)
		head.WriteString(pad(label, w, isNumeric(g.fields[i].Kind)) + " ")
		rule.WriteString(strings.Repeat("─", w) + " ")
		used += w + 1
	}
	lines = append(lines, th.bold.Render(fit(head.String(), width)), th.muted.Render(fit(rule.String(), width)))

	if len(g.rows) == 0 && body > 0 {
		lines = append(lines, th.muted.Render(fit("(no rows)", width)))
	}
	for r := g.top; r < len(g.rows) && r < g.top+body; r++ {
		var line strings.Builder
		used := 0
		for c := g.left; c <= last; c++ {
			w := min(g.widths[c], width-used)
			line.WriteString(g.cell(th, r, c, w, focused))
			used += w + 1
			if used < width {
				line.WriteString(" ")
			}
		}
		lines = append(lines, line.String())
	}
	return lines
}

func (g *grid) cell(th theme, r, c, width int, focused bool) string {
	v := g.rows[r][c]
	pendingValue, isPending := g.pending[[2]int{r, c}]
	if isPending {
		v = pendingValue
	}
	text := cellPreview(v)
	if isPending {
		text = fit(text, width-1) + "*"
	}
	text = pad(fit(text, width), width, isNumeric(g.fields[c].Kind))
	switch {
	case focused && r == g.row && c == g.col:
		return th.cursor.Render(text)
	case isPending:
		return th.warn.Bold(true).Render(text)
	case v.Kind == tile.KindNull:
		return th.muted.Render(text)
	}
	return th.text.Render(text)
}

// summary is the pane title's right side.
func (g *grid) summary() string {
	n := count(len(g.rows), "row")
	if len(g.rows) > 0 {
		n = fmt.Sprintf("row %d of %d", g.row+1, len(g.rows))
	}
	if g.truncated {
		n += " (truncated)"
	} else if b := g.browse; b != nil && !b.exhausted {
		n += "+"
		if b.layout.Estimate >= 0 {
			n += fmt.Sprintf(", about %d", b.layout.Estimate)
		}
	}
	if len(g.pending) > 0 {
		n += fmt.Sprintf(", %d pending", len(g.pending))
	}
	return n
}

// changes groups pending edits into one change per record.
func (g *grid) changes() tile.ChangeSet {
	l := g.browse.layout
	byRow := map[int]*tile.Change{}
	var order []int
	for at, v := range g.pending {
		r, c := at[0], at[1]
		ch, ok := byRow[r]
		if !ok {
			ch = &tile.Change{Old: map[string]tile.Value{}, Set: map[string]tile.Value{}}
			for _, k := range l.Key {
				i := slices.IndexFunc(g.fields, func(f tile.Field) bool { return f.Name == k })
				ch.Key = append(ch.Key, g.rows[r][i])
			}
			byRow[r] = ch
			order = append(order, r)
		}
		name := g.fields[c].Name
		ch.Old[name] = g.rows[r][c]
		ch.Set[name] = v
	}
	slices.Sort(order)
	var cs tile.ChangeSet
	for _, r := range order {
		cs.Changes = append(cs.Changes, *byRow[r])
	}
	return cs
}

// canEditText reports whether a single-line or multi-line text box would keep the value exact.
func canEditText(v tile.Value) error {
	if v.Kind == tile.KindBytes {
		return fmt.Errorf("binary values cannot be edited here; use an UPDATE in the editor")
	}
	for _, r := range string(v.Raw) {
		if r != '\n' && (r < 0x20 || r == 0x7f) {
			return fmt.Errorf("this value holds control characters that a text box would change; use an UPDATE in the editor")
		}
	}
	return nil
}

// parseFilter reads the filter prompt for a column: "= x", "!= x", "<= x", ">= x", "< x",
// "> x", "~ x", "null", "!null". Bare text means contains.
func parseFilter(field, text string) (tile.Condition, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return tile.Condition{}, fmt.Errorf("type a value, or an operator and a value")
	}
	switch strings.ToLower(text) {
	case "null":
		return tile.Condition{Field: field, Op: tile.OpNull}, nil
	case "!null":
		return tile.Condition{Field: field, Op: tile.OpNotNull}, nil
	}
	for _, op := range []tile.FilterOp{tile.OpNe, tile.OpLe, tile.OpGe, tile.OpEq, tile.OpLt, tile.OpGt, tile.OpContains} {
		if rest, ok := strings.CutPrefix(text, string(op)); ok {
			rest = strings.TrimSpace(rest)
			if rest == "" {
				return tile.Condition{}, fmt.Errorf("%s needs a value", op)
			}
			return tile.Condition{Field: field, Op: op, Value: tile.Text(rest)}, nil
		}
	}
	return tile.Condition{Field: field, Op: tile.OpContains, Value: tile.Text(text)}, nil
}

func describeFilter(conds []tile.Condition) string {
	parts := make([]string, len(conds))
	for i, c := range conds {
		parts[i] = strings.TrimSpace(ioformat.Display(c.Field) + " " + string(c.Op) + " " + ioformat.Display(string(c.Value.Raw)))
	}
	return strings.Join(parts, " and ")
}

func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
