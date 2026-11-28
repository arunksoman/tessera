package main

import (
	"context"
	"fmt"

	"github.com/arunksoman/tessera/internal/tiles/sqlgen"
	"github.com/arunksoman/tessera/tile"
)

// query runs a catalog statement and returns its values as the driver gives them.
func (c *conn) query(ctx context.Context, text string, args ...any) ([][]any, error) {
	rows, err := c.db.QueryContext(ctx, text, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out [][]any
	for rows.Next() {
		r := make([]any, len(cols))
		dest := make([]any, len(cols))
		for i := range r {
			dest[i] = &r[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// str reads a catalog value as text; NULL is empty.
func str(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// names reads a catalog list of names.
func names(v any) []string {
	l, _ := v.([]any)
	out := make([]string, len(l))
	for i, e := range l {
		out[i] = str(e)
	}
	return out
}

type tree struct{ c *conn }

// The tree shows the database the connection opened. Attached databases and temporary objects
// belong to one connection, so a pooled tree cannot show them; the system schemas are left out.
const (
	schemasSQL = `SELECT schema_name FROM duckdb_schemas()
		WHERE database_name = current_database() AND schema_name NOT IN ('information_schema', 'pg_catalog') AND schema_name > $1
		ORDER BY schema_name LIMIT $2`
	tablesSQL = `SELECT name, kind FROM (
			SELECT table_name AS name, 'table' AS kind FROM duckdb_tables()
				WHERE database_name = current_database() AND schema_name = $1
			UNION ALL
			SELECT view_name, 'view' FROM duckdb_views()
				WHERE database_name = current_database() AND schema_name = $1 AND NOT internal)
		WHERE name > $2 ORDER BY name LIMIT $3`
	columnsSQL = `SELECT column_name, data_type, CASE WHEN is_nullable THEN 'YES' ELSE 'NO' END, column_default
		FROM duckdb_columns()
		WHERE database_name = current_database() AND schema_name = $1 AND table_name = $2
		ORDER BY column_index`
	indexesSQL = `SELECT index_name, expressions, CASE WHEN is_unique THEN 'YES' ELSE 'NO' END
		FROM duckdb_indexes()
		WHERE database_name = current_database() AND schema_name = $1 AND table_name = $2
		ORDER BY index_name`
	// constraint_type is PRIMARY KEY, UNIQUE, or FOREIGN KEY here; the primary key comes first.
	constraintsSQL = `SELECT constraint_type, constraint_name, constraint_column_names, referenced_table, referenced_column_names
		FROM duckdb_constraints()
		WHERE database_name = current_database() AND schema_name = $1 AND table_name = $2
			AND constraint_type IN ('PRIMARY KEY', 'UNIQUE', 'FOREIGN KEY')
		ORDER BY constraint_type <> 'PRIMARY KEY', constraint_index`
)

func (t tree) Children(ctx context.Context, parent tile.ObjectRef, p tile.Page) ([]tile.Node, string, error) {
	stage := func(text string, node func(r []any) tile.Node, args ...any) sqlgen.Stage {
		return func(after string, n int) ([]tile.Node, error) {
			rows, err := t.c.query(ctx, text, append(args, after, n)...)
			nodes := make([]tile.Node, len(rows))
			for i, r := range rows {
				nodes[i] = node(r)
			}
			return nodes, err
		}
	}
	switch len(parent.Path) {
	case 0:
		return sqlgen.PageStages(p.Limit, p.Cursor, stage(schemasSQL, func(r []any) tile.Node {
			return tile.Node{Ref: parent.Child("schema", str(r[0])), Kind: "schema", Label: str(r[0]), HasChildren: true}
		}))
	case 1:
		return sqlgen.PageStages(p.Limit, p.Cursor, stage(tablesSQL, func(r []any) tile.Node {
			name, kind := str(r[0]), str(r[1])
			return tile.Node{Ref: parent.Child(kind, name), Kind: kind, Label: name, Browsable: true}
		}, parent.Path[0].Name))
	}
	return nil, "", nil
}

func target(ref tile.ObjectRef) (string, string, error) {
	if len(ref.Path) != 2 {
		return "", "", fmt.Errorf("duckdb: %v is not a table or view", ref.Path)
	}
	return ref.Path[0].Name, ref.Path[1].Name, nil
}

// columns reads a table's or view's columns, failing when it has none, so a missing table is an
// error rather than an empty one.
func (t tree) columns(ctx context.Context, schema, name string) ([][]any, error) {
	cols, err := t.c.query(ctx, columnsSQL, schema, name)
	if err == nil && len(cols) == 0 {
		err = fmt.Errorf("duckdb: %s.%s not found", schema, name)
	}
	return cols, err
}

func (t tree) Describe(ctx context.Context, ref tile.ObjectRef) (tile.ObjectDetail, error) {
	schema, name, err := target(ref)
	if err != nil {
		return tile.ObjectDetail{}, err
	}
	cols, err := t.columns(ctx, schema, name)
	if err != nil {
		return tile.ObjectDetail{}, err
	}
	idx, err := t.c.query(ctx, indexesSQL, schema, name)
	if err != nil {
		return tile.ObjectDetail{}, err
	}
	return tile.ObjectDetail{Sections: []tile.DetailSection{
		section("Columns", []string{"name", "type", "nullable", "default"}, cols),
		section("Indexes", []string{"name", "columns", "unique"}, idx),
	}}, nil
}

func section(title string, fields []string, rows [][]any) tile.DetailSection {
	s := tile.DetailSection{Title: title}
	for _, n := range fields {
		s.Fields = append(s.Fields, tile.Field{Name: n, Kind: tile.KindText})
	}
	for _, r := range rows {
		row := make([]tile.Value, len(fields))
		for i := range fields {
			row[i] = tile.Value{Kind: tile.KindNull}
			if r[i] != nil {
				row[i] = tile.Text(str(r[i]))
			}
		}
		s.Rows = append(s.Rows, row)
	}
	return s
}

type constraint struct {
	kind, name   string
	fields       []string
	target       string
	targetFields []string
}

func (t tree) constraints(ctx context.Context, schema, name string) ([]constraint, error) {
	rows, err := t.c.query(ctx, constraintsSQL, schema, name)
	out := make([]constraint, len(rows))
	for i, r := range rows {
		out[i] = constraint{kind: str(r[0]), name: str(r[1]), fields: names(r[2]), target: str(r[3]), targetFields: names(r[4])}
	}
	return out, err
}

// DescribeKeys reads the primary key and the foreign keys. DuckDB keeps a foreign key's target
// in the same schema.
func (t tree) DescribeKeys(ctx context.Context, ref tile.ObjectRef) (tile.Keys, error) {
	schema, name, err := target(ref)
	if err != nil {
		return tile.Keys{}, err
	}
	cs, err := t.constraints(ctx, schema, name)
	var k tile.Keys
	for _, c := range cs {
		switch c.kind {
		case "PRIMARY KEY":
			k.Primary = c.fields
		case "FOREIGN KEY":
			k.Foreign = append(k.Foreign, tile.ForeignKey{Name: c.name, Fields: c.fields,
				Target: tile.ObjectRef{}.Child("schema", schema).Child("table", c.target), TargetFields: c.targetFields})
		}
	}
	return k, err
}

func (c *conn) Layout(ctx context.Context, ref tile.ObjectRef) (tile.Layout, error) {
	schema, name, err := target(ref)
	if err != nil {
		return tile.Layout{}, err
	}
	t := tree{c}
	cols, err := t.columns(ctx, schema, name)
	if err != nil {
		return tile.Layout{}, err
	}
	l := tile.Layout{Estimate: -1}
	notNull := map[string]bool{}
	for _, r := range cols {
		l.Fields = append(l.Fields, tile.Field{Name: str(r[0]), NativeType: str(r[1]), Kind: kindOf(str(r[1]))})
		notNull[str(r[0])] = str(r[2]) == "NO"
	}
	cs, err := t.constraints(ctx, schema, name)
	if err != nil {
		return tile.Layout{}, err
	}
	// The primary key, or else the first unique key whose columns are all NOT NULL.
	for _, k := range cs {
		usable := k.kind == "PRIMARY KEY"
		if k.kind == "UNIQUE" {
			usable = true
			for _, f := range k.fields {
				usable = usable && notNull[f]
			}
		}
		if usable && len(k.fields) > 0 {
			l.Key = k.fields
			break
		}
	}
	if len(l.Key) == 0 {
		l.Note = "no primary key or unique not-null key"
	}
	return l, nil
}
