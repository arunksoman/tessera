package sqlite

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/arunksoman/tessera/tile"
)

const (
	indexListSQL = `SELECT il.name, il."unique", il.origin, coalesce(s.sql, '')
		FROM pragma_index_list(?1, ?2) il LEFT JOIN %s.sqlite_schema s ON s.type = 'index' AND s.name = il.name
		ORDER BY il.origin = 'pk' DESC, il.name`
	indexColumnsSQL = `SELECT coalesce(name, '(expression)') FROM pragma_index_xinfo(?1, ?2) WHERE key ORDER BY seqno`
	triggersSQL     = `SELECT name, sql FROM %s.sqlite_schema WHERE type = 'trigger' AND tbl_name = ? ORDER BY name`
	// The object first, then its indexes and triggers. Indexes SQLite makes for keys have no text.
	createSQL = `SELECT type, sql FROM %s.sqlite_schema WHERE tbl_name = ? AND sql IS NOT NULL
		ORDER BY name <> tbl_name, type = 'trigger', name`
)

// DescribeStructure reads columns, indexes, foreign keys, and triggers. SQLite keeps no
// comments, and a column's collation only inside the table's text, so both stay empty.
func (t tree) DescribeStructure(ctx context.Context, ref tile.ObjectRef) (tile.Structure, error) {
	schema, name, err := target(ref)
	if err != nil {
		return tile.Structure{}, err
	}
	var s tile.Structure
	cols, err := t.c.query(ctx, columnsSQL, name, schema)
	if err != nil {
		return s, err
	}
	if len(cols) == 0 {
		return s, fmt.Errorf("sqlite: %s.%s not found", schema, name)
	}
	for _, r := range cols {
		f := tile.FieldDef{Name: r[0].String, Type: r[1].String, Nullable: r[2].String == "YES" && r[4].String == "0",
			Default: r[3].String, Primary: r[4].String != "0"}
		switch r[5].String {
		case "2":
			f.Extra = "GENERATED ALWAYS VIRTUAL"
		case "3":
			f.Extra = "GENERATED ALWAYS STORED"
		}
		s.Fields = append(s.Fields, f)
	}
	pk, err := t.primary(ctx, schema, name)
	if err != nil {
		return s, err
	}
	idx, err := t.c.query(ctx, fmt.Sprintf(indexListSQL, quote(schema)), name, schema)
	if err != nil {
		return s, err
	}
	// An INTEGER PRIMARY KEY is the rowid and has no index of its own, so it is listed as one.
	if len(pk) > 0 && (len(idx) == 0 || idx[0][2].String != "pk") {
		s.Indexes = append(s.Indexes, tile.IndexDef{Name: "PRIMARY", Fields: pk, Unique: true, Primary: true, Constraint: true,
			Definition: "PRIMARY KEY (" + strings.Join(pk, ", ") + ")"})
	}
	for _, r := range idx {
		ix := tile.IndexDef{Name: r[0].String, Unique: r[1].String == "1", Primary: r[2].String == "pk", Constraint: r[2].String != "c",
			Definition: r[3].String}
		fields, err := t.c.query(ctx, indexColumnsSQL, ix.Name, schema)
		if err != nil {
			return s, err
		}
		for _, f := range fields {
			ix.Fields = append(ix.Fields, f[0].String)
		}
		if ix.Definition == "" {
			ix.Definition = map[bool]string{true: "PRIMARY KEY", false: "UNIQUE"}[ix.Primary] + " (" + strings.Join(ix.Fields, ", ") + ")"
		}
		s.Indexes = append(s.Indexes, ix)
	}
	if s.Relations, err = t.foreign(ctx, schema, name); err != nil {
		return s, err
	}
	trs, err := t.c.query(ctx, fmt.Sprintf(triggersSQL, quote(schema)), name)
	if err != nil {
		return s, err
	}
	for _, r := range trs {
		s.Triggers = append(s.Triggers, triggerOf(r[0].String, r[1].String))
	}
	return s, nil
}

// triggerHead reads the timing and event from a trigger's text. It is for display only; the
// definition is the store's own text.
var triggerHead = regexp.MustCompile(`(?is)\b(BEFORE|AFTER|INSTEAD\s+OF)?\s*\b(INSERT|UPDATE|DELETE)\b.*?\bON\b`)

func triggerOf(name, def string) tile.TriggerDef {
	tr := tile.TriggerDef{Name: name, Definition: def, Timing: "BEFORE", ForEach: "ROW"}
	if m := triggerHead.FindStringSubmatch(def); m != nil {
		if m[1] != "" {
			tr.Timing = strings.ToUpper(strings.Join(strings.Fields(m[1]), " "))
		}
		tr.Events = []string{strings.ToUpper(m[2])}
	}
	upper := strings.ToUpper(def)
	begin, end := strings.Index(upper, "BEGIN"), strings.LastIndex(upper, "END")
	if begin >= 0 && end > begin {
		tr.Action = strings.TrimSpace(def[begin+len("BEGIN") : end])
		if w := strings.Index(upper[:begin], "WHEN"); w >= 0 {
			tr.Condition = strings.TrimSpace(def[w+len("WHEN") : begin])
		}
	}
	return tr
}

// PlanStructure builds DDL, run in one transaction. Changes SQLite can make only by rebuilding
// the table are refused.
func (t tree) PlanStructure(ref tile.ObjectRef, changes []tile.StructureChange) (tile.Plan, error) {
	if _, _, err := target(ref); err != nil {
		return tile.Plan{}, err
	}
	return dialect.PlanStructure(ref, changes)
}

// CreateScript returns the text SQLite kept for the object, its indexes, and its triggers.
func (t tree) CreateScript(ctx context.Context, ref tile.ObjectRef) (string, error) {
	schema, name, err := target(ref)
	if err != nil {
		return "", err
	}
	rows, err := t.c.query(ctx, fmt.Sprintf(createSQL, quote(schema)), name)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", fmt.Errorf("sqlite: %s.%s not found", schema, name)
	}
	var b strings.Builder
	for i, r := range rows {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(strings.TrimRight(r[1].String, "; \n") + ";\n")
	}
	return b.String(), nil
}

func (c *conn) InsertPlan(ref tile.ObjectRef, fields []string, rows [][]tile.Value) (tile.Plan, error) {
	return dialect.InsertPlan(ref, fields, rows)
}

func (c *conn) InsertScript(ref tile.ObjectRef, fields []string, rows [][]tile.Value) (string, error) {
	return dialect.InsertScript(ref, fields, rows)
}
