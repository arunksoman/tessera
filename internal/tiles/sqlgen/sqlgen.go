// Package sqlgen builds the browse and edit statements shared by the SQL tiles. Values are
// always bound and identifiers always quoted, and every field is checked against the layout.
package sqlgen

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/arunksoman/tessera/tile"
)

// Dialect holds what differs between SQL families.
type Dialect struct {
	Quote func(string) string
	Mark  func(i int) string // the placeholder for the i-th argument, from 1
	// Text renders a quoted column as the text the server sends, so a guard compares what the
	// user saw and works on types without an equality operator.
	Text  func(column string) string
	Like  string // case-insensitive LIKE
	Begin string
}

type builder struct {
	d    Dialect
	args []tile.Value
}

func (b *builder) bind(v tile.Value) string {
	b.args = append(b.args, v)
	return b.d.Mark(len(b.args))
}

func (d Dialect) object(ref tile.ObjectRef) string {
	parts := make([]string, len(ref.Path))
	for i, s := range ref.Path {
		parts[i] = d.Quote(s.Name)
	}
	return strings.Join(parts, ".")
}

func fieldIndex(l tile.Layout, name string) int {
	return slices.IndexFunc(l.Fields, func(f tile.Field) bool { return f.Name == name })
}

// Select builds one page. The order always ends in the key, so paging is stable. NULLs sort
// last in both directions and both dialects, which PostgreSQL and MySQL otherwise disagree on.
func (d Dialect) Select(ref tile.ObjectRef, l tile.Layout, q tile.BrowseQuery) (tile.Request, error) {
	b := &builder{d: d}
	cols := make([]string, len(l.Fields))
	for i, f := range l.Fields {
		cols[i] = d.Quote(f.Name)
	}
	var where []string
	for _, c := range q.Filter {
		term, err := b.condition(l, c)
		if err != nil {
			return tile.Request{}, err
		}
		where = append(where, term)
	}
	order := slices.Clone(q.Sort)
	for _, k := range order {
		if fieldIndex(l, k.Field) < 0 {
			return tile.Request{}, fmt.Errorf("unknown field %q", k.Field)
		}
	}
	for _, k := range l.Key {
		if !slices.ContainsFunc(order, func(s tile.SortKey) bool { return s.Field == k }) {
			order = append(order, tile.SortKey{Field: k})
		}
	}
	if len(l.Key) > 0 && q.After != nil {
		where = append(where, "("+b.after(l, order, q.After)+")")
	}

	var s strings.Builder
	fmt.Fprintf(&s, "SELECT %s FROM %s", strings.Join(cols, ", "), d.object(ref))
	if len(where) > 0 {
		s.WriteString(" WHERE " + strings.Join(where, " AND "))
	}
	if len(order) > 0 {
		terms := make([]string, 0, len(order))
		for _, k := range order {
			if !slices.Contains(l.Key, k.Field) {
				terms = append(terms, "("+d.Quote(k.Field)+" IS NULL) ASC")
			}
			dir := "ASC"
			if k.Desc {
				dir = "DESC"
			}
			terms = append(terms, d.Quote(k.Field)+" "+dir)
		}
		s.WriteString(" ORDER BY " + strings.Join(terms, ", "))
	}
	fmt.Fprintf(&s, " LIMIT %d", q.Limit)
	if len(l.Key) == 0 && q.Offset > 0 {
		fmt.Fprintf(&s, " OFFSET %d", q.Offset)
	}
	return tile.Request{Text: s.String(), Args: b.args}, nil
}

func (b *builder) condition(l tile.Layout, c tile.Condition) (string, error) {
	if fieldIndex(l, c.Field) < 0 {
		return "", fmt.Errorf("unknown field %q", c.Field)
	}
	col := b.d.Quote(c.Field)
	switch c.Op {
	case tile.OpEq, tile.OpLt, tile.OpLe, tile.OpGt, tile.OpGe:
		return col + " " + string(c.Op) + " " + b.bind(c.Value), nil
	case tile.OpNe:
		return col + " <> " + b.bind(c.Value), nil
	case tile.OpContains:
		like := "%" + likeEscape.Replace(string(c.Value.Raw)) + "%"
		return b.d.Text(col) + " " + b.d.Like + " " + b.bind(tile.Text(like)), nil
	case tile.OpNull:
		return col + " IS NULL", nil
	case tile.OpNotNull:
		return col + " IS NOT NULL", nil
	}
	return "", fmt.Errorf("unknown filter %q", c.Op)
}

var likeEscape = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// after returns a predicate for "sorts after this record": t1 OR (e1 AND (t2 OR (e2 AND ...))).
// A plain row comparison cannot be used, because any NULL in it makes the result unknown.
func (b *builder) after(l tile.Layout, order []tile.SortKey, last []tile.Value) string {
	k := order[0]
	col := b.d.Quote(k.Field)
	v := last[fieldIndex(l, k.Field)]
	op := ">"
	if k.Desc {
		op = "<"
	}
	strict := ""
	if v.Kind != tile.KindNull {
		strict = col + " " + op + " " + b.bind(v)
		if !slices.Contains(l.Key, k.Field) {
			strict = "(" + strict + " OR " + col + " IS NULL)" // NULLs sort last
		}
	}
	if len(order) == 1 {
		return strict
	}
	equal := col + " IS NULL"
	if v.Kind != tile.KindNull {
		equal = col + " = " + b.bind(v)
	}
	rest := equal + " AND (" + b.after(l, order[1:], last) + ")"
	if strict == "" {
		return rest
	}
	return strict + " OR (" + rest + ")"
}

// Plan builds guarded updates, run in one transaction. It has no side effects.
func (d Dialect) Plan(ref tile.ObjectRef, l tile.Layout, cs tile.ChangeSet) (tile.Plan, error) {
	if len(l.Key) == 0 {
		return tile.Plan{}, fmt.Errorf("not editable: %s", l.Note)
	}
	plan := tile.Plan{
		Begin:    tile.Request{Text: d.Begin},
		Commit:   tile.Request{Text: "COMMIT"},
		Rollback: tile.Request{Text: "ROLLBACK"},
	}
	for _, c := range cs.Changes {
		st, err := d.update(ref, l, c)
		if err != nil {
			return tile.Plan{}, err
		}
		plan.Steps = append(plan.Steps, st)
	}
	return plan, nil
}

func (d Dialect) update(ref tile.ObjectRef, l tile.Layout, c tile.Change) (tile.PlanStep, error) {
	if len(c.Key) != len(l.Key) {
		return tile.PlanStep{}, fmt.Errorf("change has %d key values, want %d", len(c.Key), len(l.Key))
	}
	names := make([]string, 0, len(c.Set))
	for name := range c.Set {
		if fieldIndex(l, name) < 0 {
			return tile.PlanStep{}, fmt.Errorf("unknown field %q", name)
		}
		if _, ok := c.Old[name]; !ok {
			return tile.PlanStep{}, fmt.Errorf("no old value for %q, so the update has no guard", name)
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return tile.PlanStep{}, errors.New("change sets no fields")
	}
	slices.Sort(names)

	b := &builder{d: d}
	var text, preview strings.Builder
	write := func(s string) { text.WriteString(s); preview.WriteString(s) }
	bind := func(v tile.Value) {
		text.WriteString(b.bind(v))
		preview.WriteString(literal(v))
	}
	write("UPDATE " + d.object(ref) + " SET ")
	for i, name := range names {
		if i > 0 {
			write(", ")
		}
		write(d.Quote(name) + " = ")
		bind(c.Set[name])
	}
	write(" WHERE ")
	for i, name := range l.Key {
		if i > 0 {
			write(" AND ")
		}
		write(d.Quote(name) + " = ")
		bind(c.Key[i])
	}
	for _, name := range names {
		write(" AND ")
		if old := c.Old[name]; old.Kind == tile.KindNull {
			write(d.Quote(name) + " IS NULL")
		} else {
			write(d.Text(d.Quote(name)) + " = ")
			bind(old)
		}
	}
	return tile.PlanStep{Request: tile.Request{Text: text.String(), Args: b.args}, Preview: preview.String(), ExpectRows: 1}, nil
}

// literal renders a value for reading only. It is never sent to a server.
func literal(v tile.Value) string {
	switch v.Kind {
	case tile.KindNull:
		return "NULL"
	case tile.KindBytes:
		return "<" + strconv.Itoa(len(v.Raw)) + " bytes>"
	}
	return "'" + strings.ReplaceAll(string(v.Raw), "'", "''") + "'"
}
