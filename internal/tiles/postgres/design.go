package postgres

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/arunksoman/tessera/tile"
)

const (
	// One row per column: name, type, nullable, default, comment, primary, collation where it
	// differs from the type's, identity, generated.
	structFieldsSQL = `SELECT a.attname, format_type(a.atttypid, a.atttypmod), (NOT a.attnotnull)::text,
			COALESCE(pg_get_expr(d.adbin, d.adrelid), ''), COALESCE(col_description(c.oid, a.attnum), ''),
			EXISTS (SELECT 1 FROM pg_index x WHERE x.indrelid = c.oid AND x.indisprimary AND a.attnum = ANY (x.indkey))::text,
			CASE WHEN a.attcollation <> 0 AND a.attcollation <> t.typcollation THEN co.collname ELSE '' END,
			a.attidentity::text, a.attgenerated::text
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_type t ON t.oid = a.atttypid
		LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		LEFT JOIN pg_collation co ON co.oid = a.attcollation
		WHERE n.nspname = $1 AND c.relname = $2 AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY a.attnum`
	// Key columns are separated by the unit separator, since names may hold commas. An
	// expression column is wrapped in parentheses.
	structIndexesSQL = `SELECT i.relname, x.indisunique::text, x.indisprimary::text,
			EXISTS (SELECT 1 FROM pg_constraint con WHERE con.conindid = x.indexrelid AND con.conrelid = x.indrelid)::text,
			am.amname, pg_get_indexdef(x.indexrelid),
			(SELECT string_agg(CASE WHEN x.indkey[k] = 0 THEN '(' || pg_get_indexdef(x.indexrelid, k + 1, true) || ')' ELSE a.attname END,
				E'\x1f' ORDER BY k)
			 FROM generate_series(0, x.indnkeyatts - 1) k
			 LEFT JOIN pg_attribute a ON a.attrelid = x.indrelid AND a.attnum = x.indkey[k])
		FROM pg_index x
		JOIN pg_class c ON c.oid = x.indrelid
		JOIN pg_class i ON i.oid = x.indexrelid
		JOIN pg_am am ON am.oid = i.relam
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = $2
		ORDER BY x.indisprimary DESC, i.relname`
	structRelationsSQL = `SELECT con.conname, string_agg(a.attname, E'\x1f' ORDER BY k.ord), fn.nspname, fc.relname,
			string_agg(fa.attname, E'\x1f' ORDER BY k.ord), con.confupdtype::text, con.confdeltype::text
		FROM pg_constraint con
		JOIN pg_class c ON c.oid = con.conrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		CROSS JOIN LATERAL unnest(con.conkey, con.confkey) WITH ORDINALITY AS k(attnum, fattnum, ord)
		JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum
		JOIN pg_class fc ON fc.oid = con.confrelid
		JOIN pg_namespace fn ON fn.oid = fc.relnamespace
		JOIN pg_attribute fa ON fa.attrelid = con.confrelid AND fa.attnum = k.fattnum
		WHERE con.contype = 'f' AND n.nspname = $1 AND c.relname = $2
		GROUP BY con.conname, fn.nspname, fc.relname, con.confupdtype, con.confdeltype
		ORDER BY con.conname`
	structTriggersSQL = `SELECT t.tgname, t.tgtype::int::text, pg_get_triggerdef(t.oid, true)
		FROM pg_trigger t
		JOIN pg_class c ON c.oid = t.tgrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE NOT t.tgisinternal AND n.nspname = $1 AND c.relname = $2
		ORDER BY t.tgname`
	relationSQL = `SELECT c.oid::text, c.relkind::text, COALESCE(obj_description(c.oid, 'pg_class'), ''),
			c.relispartition::text, COALESCE(pg_get_expr(c.relpartbound, c.oid), ''),
			CASE WHEN c.relkind = 'p' THEN pg_get_partkeydef(c.oid) ELSE '' END,
			COALESCE((SELECT string_agg(quote_ident(pn.nspname) || '.' || quote_ident(p.relname), ', ' ORDER BY i.inhseqno)
				FROM pg_inherits i JOIN pg_class p ON p.oid = i.inhparent JOIN pg_namespace pn ON pn.oid = p.relnamespace
				WHERE i.inhrelid = c.oid), '')
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = $2`
	// Constraints in the order a table definition lists them: primary key, unique, check,
	// exclusion, then foreign keys. Constraints inherited from a parent are left out.
	constraintsSQL = `SELECT conname, pg_get_constraintdef(oid, true) FROM pg_constraint
		WHERE conrelid = $1::oid AND contype IN ('p', 'u', 'c', 'x', 'f') AND conislocal
		ORDER BY array_position(ARRAY['p', 'u', 'c', 'x', 'f']::"char"[], contype), conname`
	plainIndexesSQL = `SELECT pg_get_indexdef(x.indexrelid) FROM pg_index x
		JOIN pg_class i ON i.oid = x.indexrelid
		WHERE x.indrelid = $1::oid AND NOT EXISTS (SELECT 1 FROM pg_constraint con WHERE con.conindid = x.indexrelid AND con.conrelid = x.indrelid)
		ORDER BY i.relname`
	viewSQL = `SELECT pg_get_viewdef($1::oid, true)`
)

// Trigger type bits from PostgreSQL's pg_trigger.h.
const (
	tgRow      = 1 << 0
	tgBefore   = 1 << 1
	tgInsert   = 1 << 2
	tgDelete   = 1 << 3
	tgUpdate   = 1 << 4
	tgTruncate = 1 << 5
	tgInstead  = 1 << 6
)

var relationActions = map[string]string{"a": "NO ACTION", "r": "RESTRICT", "c": "CASCADE", "n": "SET NULL", "d": "SET DEFAULT"}

func splitUnit(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\x1f")
}

func (t tree) target(ref tile.ObjectRef) (string, string, error) {
	if len(ref.Path) != 2 {
		return "", "", fmt.Errorf("postgres: %v is not a table or view", ref.Path)
	}
	return ref.Path[0].Name, ref.Path[1].Name, nil
}

// DescribeStructure reads columns, indexes, foreign keys, and triggers from the catalog.
func (t tree) DescribeStructure(ctx context.Context, ref tile.ObjectRef) (tile.Structure, error) {
	schema, name, err := t.target(ref)
	if err != nil {
		return tile.Structure{}, err
	}
	var s tile.Structure
	rows, _, err := t.c.query(ctx, structFieldsSQL, schema, name)
	if err != nil {
		return s, err
	}
	if len(rows) == 0 {
		return s, fmt.Errorf("postgres: %s.%s not found", schema, name)
	}
	for _, r := range rows {
		f := tile.FieldDef{
			Name: string(r[0]), Type: string(r[1]), Nullable: string(r[2]) == "true", Default: string(r[3]),
			Comment: string(r[4]), Primary: string(r[5]) == "true", Collation: string(r[6]),
		}
		switch string(r[7]) {
		case "a":
			f.Extra = "GENERATED ALWAYS AS IDENTITY"
		case "d":
			f.Extra = "GENERATED BY DEFAULT AS IDENTITY"
		}
		if string(r[8]) == "s" {
			f.Extra, f.Default = "GENERATED ALWAYS AS ("+f.Default+") STORED", ""
		}
		s.Fields = append(s.Fields, f)
	}
	if rows, _, err = t.c.query(ctx, structIndexesSQL, schema, name); err != nil {
		return s, err
	}
	for _, r := range rows {
		s.Indexes = append(s.Indexes, tile.IndexDef{
			Name: string(r[0]), Unique: string(r[1]) == "true", Primary: string(r[2]) == "true", Constraint: string(r[3]) == "true",
			Method: string(r[4]), Definition: string(r[5]), Fields: splitUnit(string(r[6])),
		})
	}
	if rows, _, err = t.c.query(ctx, structRelationsSQL, schema, name); err != nil {
		return s, err
	}
	for _, r := range rows {
		s.Relations = append(s.Relations, tile.Relation{
			ForeignKey: tile.ForeignKey{
				Name: string(r[0]), Fields: splitUnit(string(r[1])),
				Target:       tile.ObjectRef{}.Child("schema", string(r[2])).Child("table", string(r[3])),
				TargetFields: splitUnit(string(r[4])),
			},
			OnUpdate: relationActions[string(r[5])], OnDelete: relationActions[string(r[6])],
		})
	}
	if rows, _, err = t.c.query(ctx, structTriggersSQL, schema, name); err != nil {
		return s, err
	}
	for _, r := range rows {
		typ, _ := strconv.Atoi(string(r[1]))
		s.Triggers = append(s.Triggers, triggerOf(string(r[0]), typ, string(r[2])))
	}
	return s, nil
}

// triggerOf reads a trigger from its type bits and its definition, which ends with the
// action and may hold a WHEN condition before it.
func triggerOf(name string, typ int, def string) tile.TriggerDef {
	tr := tile.TriggerDef{Name: name, Definition: def, Timing: "AFTER", ForEach: "STATEMENT"}
	switch {
	case typ&tgInstead != 0:
		tr.Timing = "INSTEAD OF"
	case typ&tgBefore != 0:
		tr.Timing = "BEFORE"
	}
	if typ&tgRow != 0 {
		tr.ForEach = "ROW"
	}
	for _, e := range []struct {
		bit  int
		name string
	}{{tgInsert, "INSERT"}, {tgUpdate, "UPDATE"}, {tgDelete, "DELETE"}, {tgTruncate, "TRUNCATE"}} {
		if typ&e.bit != 0 {
			tr.Events = append(tr.Events, e.name)
		}
	}
	at := max(strings.LastIndex(def, " EXECUTE FUNCTION "), strings.LastIndex(def, " EXECUTE PROCEDURE "))
	if at < 0 {
		return tr
	}
	tr.Action = def[at+1:]
	head := def[:at]
	if w := strings.Index(head, " WHEN ("); w >= 0 && strings.HasSuffix(head, ")") {
		tr.Condition = head[w+len(" WHEN (") : len(head)-1]
	}
	return tr
}

// PlanStructure builds DDL, run in one transaction.
func (t tree) PlanStructure(ref tile.ObjectRef, changes []tile.StructureChange) (tile.Plan, error) {
	if _, _, err := t.target(ref); err != nil {
		return tile.Plan{}, err
	}
	return dialect.PlanStructure(ref, changes)
}

// CreateScript rebuilds a table's definition from the catalog, since PostgreSQL has no
// SHOW CREATE TABLE: columns, constraints, other indexes, comments, then triggers.
func (t tree) CreateScript(ctx context.Context, ref tile.ObjectRef) (string, error) {
	schema, name, err := t.target(ref)
	if err != nil {
		return "", err
	}
	rel, _, err := t.c.query(ctx, relationSQL, schema, name)
	if err != nil {
		return "", err
	}
	if len(rel) == 0 {
		return "", fmt.Errorf("postgres: %s.%s not found", schema, name)
	}
	oid, kind, comment := string(rel[0][0]), string(rel[0][1]), string(rel[0][2])
	partition, bound, partKey, parents := string(rel[0][3]) == "true", string(rel[0][4]), string(rel[0][5]), string(rel[0][6])
	obj := dialect.Quote(schema) + "." + dialect.Quote(name)
	var b strings.Builder
	switch kind {
	case "v", "m":
		view, _, err := t.c.query(ctx, viewSQL, oid)
		if err != nil {
			return "", err
		}
		what := "VIEW"
		if kind == "m" {
			what = "MATERIALIZED VIEW"
		}
		fmt.Fprintf(&b, "CREATE %s %s AS\n%s;\n", what, obj, strings.TrimRight(string(view[0][0]), "; \n"))
	default:
		s, err := t.DescribeStructure(ctx, ref)
		if err != nil {
			return "", err
		}
		if partition {
			// A partition takes its columns from its parent.
			fmt.Fprintf(&b, "CREATE TABLE %s PARTITION OF %s\n%s;\n", obj, parents, bound)
			break
		}
		var lines []string
		for _, f := range s.Fields {
			def, err := dialect.ColumnDef(f)
			if err != nil {
				return "", err
			}
			lines = append(lines, def)
		}
		cons, _, err := t.c.query(ctx, constraintsSQL, oid)
		if err != nil {
			return "", err
		}
		for _, r := range cons {
			lines = append(lines, "CONSTRAINT "+dialect.Quote(string(r[0]))+" "+string(r[1]))
		}
		fmt.Fprintf(&b, "CREATE TABLE %s (\n    %s\n)", obj, strings.Join(lines, ",\n    "))
		if parents != "" {
			fmt.Fprintf(&b, "\nINHERITS (%s)", parents)
		}
		if partKey != "" {
			fmt.Fprintf(&b, "\nPARTITION BY %s", partKey)
		}
		b.WriteString(";\n")
		idx, _, err := t.c.query(ctx, plainIndexesSQL, oid)
		if err != nil {
			return "", err
		}
		for _, r := range idx {
			b.WriteString("\n" + string(r[0]) + ";")
		}
		if len(idx) > 0 {
			b.WriteString("\n")
		}
		for _, f := range s.Fields {
			if f.Comment != "" {
				fmt.Fprintf(&b, "\nCOMMENT ON COLUMN %s.%s IS %s;", obj, dialect.Quote(f.Name), dialect.Literal(f.Comment))
			}
		}
		for _, tr := range s.Triggers {
			b.WriteString("\n" + tr.Definition + ";")
		}
	}
	if comment != "" {
		what := map[string]string{"v": "VIEW", "m": "MATERIALIZED VIEW"}[kind]
		if what == "" {
			what = "TABLE"
		}
		fmt.Fprintf(&b, "\nCOMMENT ON %s %s IS %s;", what, obj, dialect.Literal(comment))
	}
	return strings.TrimRight(b.String(), "\n") + "\n", nil
}

func (c *conn) InsertPlan(ref tile.ObjectRef, fields []string, rows [][]tile.Value) (tile.Plan, error) {
	return dialect.InsertPlan(ref, fields, rows)
}

func (c *conn) InsertScript(ref tile.ObjectRef, fields []string, rows [][]tile.Value) (string, error) {
	return dialect.InsertScript(ref, fields, rows)
}
