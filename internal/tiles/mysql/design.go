package mysql

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/arunksoman/tessera/tile"
)

const (
	// One row per column. Collation is set only where it differs from the table's. The last
	// column says whether the server is MariaDB, which reports defaults as expressions where
	// MySQL reports literals as bare text.
	structFieldsSQL = `SELECT c.COLUMN_NAME, c.COLUMN_TYPE, c.IS_NULLABLE, c.COLUMN_DEFAULT, c.COLUMN_COMMENT, c.COLUMN_KEY,
			CASE WHEN c.COLLATION_NAME <> t.TABLE_COLLATION THEN c.COLLATION_NAME ELSE '' END,
			c.EXTRA, COALESCE(c.GENERATION_EXPRESSION, ''), VERSION() LIKE '%MariaDB%'
		FROM information_schema.COLUMNS c
		JOIN information_schema.TABLES t ON t.TABLE_SCHEMA = c.TABLE_SCHEMA AND t.TABLE_NAME = c.TABLE_NAME
		WHERE c.TABLE_SCHEMA = ? AND c.TABLE_NAME = ?
		ORDER BY c.ORDINAL_POSITION`
	// Index columns are separated by the unit separator, since names may hold commas.
	structIndexesSQL = `SELECT INDEX_NAME, GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX SEPARATOR 0x1f), MIN(NON_UNIQUE), MAX(INDEX_TYPE),
			GROUP_CONCAT(CONCAT('` + "`" + `', REPLACE(COLUMN_NAME, '` + "`" + `', '` + "``" + `'), '` + "`" + `', IFNULL(CONCAT('(', SUB_PART, ')'), ''))
				ORDER BY SEQ_IN_INDEX SEPARATOR ', ')
		FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
		GROUP BY INDEX_NAME
		ORDER BY INDEX_NAME <> 'PRIMARY', INDEX_NAME`
	structRelationsSQL = `SELECT k.CONSTRAINT_NAME, GROUP_CONCAT(k.COLUMN_NAME ORDER BY k.ORDINAL_POSITION SEPARATOR 0x1f),
			k.REFERENCED_TABLE_SCHEMA, k.REFERENCED_TABLE_NAME,
			GROUP_CONCAT(k.REFERENCED_COLUMN_NAME ORDER BY k.ORDINAL_POSITION SEPARATOR 0x1f), r.UPDATE_RULE, r.DELETE_RULE
		FROM information_schema.KEY_COLUMN_USAGE k
		JOIN information_schema.REFERENTIAL_CONSTRAINTS r
			ON r.CONSTRAINT_SCHEMA = k.CONSTRAINT_SCHEMA AND r.CONSTRAINT_NAME = k.CONSTRAINT_NAME AND r.TABLE_NAME = k.TABLE_NAME
		WHERE k.TABLE_SCHEMA = ? AND k.TABLE_NAME = ? AND k.REFERENCED_TABLE_NAME IS NOT NULL
		GROUP BY k.CONSTRAINT_NAME, k.REFERENCED_TABLE_SCHEMA, k.REFERENCED_TABLE_NAME, r.UPDATE_RULE, r.DELETE_RULE
		ORDER BY k.CONSTRAINT_NAME`
	structTriggersSQL = `SELECT TRIGGER_NAME, ACTION_TIMING, EVENT_MANIPULATION, ACTION_ORIENTATION, ACTION_STATEMENT
		FROM information_schema.TRIGGERS
		WHERE EVENT_OBJECT_SCHEMA = ? AND EVENT_OBJECT_TABLE = ?
		ORDER BY TRIGGER_NAME`
	tableKindSQL = `SELECT TABLE_TYPE FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`
)

var currentTime = regexp.MustCompile(`(?i)^(current_timestamp|now|localtime|localtimestamp)(\(\d*\))?$`)

// mysqlDefault turns information_schema's COLUMN_DEFAULT into an expression. MySQL shows a
// literal as bare text and flags an expression with DEFAULT_GENERATED; MariaDB shows both
// as expressions, with NULL spelled out.
func mysqlDefault(v tile.Value, typ, extra string, mariadb bool) string {
	if v.Kind == tile.KindNull {
		return ""
	}
	d := string(v.Raw)
	switch {
	case mariadb:
		if strings.EqualFold(d, "NULL") {
			return ""
		}
		return d
	case strings.Contains(strings.ToUpper(extra), "DEFAULT_GENERATED"):
		if currentTime.MatchString(d) {
			return d
		}
		return "(" + d + ")"
	case strings.HasPrefix(strings.ToLower(typ), "bit"):
		return d // shown as b'101'
	}
	return dialect.Literal(d)
}

// mysqlExtra keeps the attributes a restated column needs, and rebuilds a generated column's
// expression.
func mysqlExtra(extra, expr string) string {
	upper := strings.ToUpper(extra)
	switch {
	case strings.Contains(upper, "VIRTUAL GENERATED"):
		return "GENERATED ALWAYS AS (" + expr + ") VIRTUAL"
	case strings.Contains(upper, "STORED GENERATED"), strings.Contains(upper, "PERSISTENT GENERATED"):
		return "GENERATED ALWAYS AS (" + expr + ") STORED"
	}
	words := strings.Fields(extra)
	words = slicesDelete(words, "DEFAULT_GENERATED")
	return strings.Join(words, " ")
}

func slicesDelete(words []string, drop string) []string {
	out := words[:0]
	for _, w := range words {
		if !strings.EqualFold(w, drop) {
			out = append(out, w)
		}
	}
	return out
}

func splitUnit(v tile.Value) []string {
	if len(v.Raw) == 0 {
		return nil
	}
	return strings.Split(string(v.Raw), "\x1f")
}

func target(ref tile.ObjectRef) (string, string, error) {
	if len(ref.Path) != 2 {
		return "", "", fmt.Errorf("mysql: %v is not a table or view", ref.Path)
	}
	return ref.Path[0].Name, ref.Path[1].Name, nil
}

// DescribeStructure reads columns, indexes, foreign keys, and triggers from information_schema.
func (t tree) DescribeStructure(ctx context.Context, ref tile.ObjectRef) (tile.Structure, error) {
	schema, name, err := target(ref)
	if err != nil {
		return tile.Structure{}, err
	}
	var s tile.Structure
	cols, err := t.section(ctx, "", structFieldsSQL, schema, name)
	if err != nil {
		return s, err
	}
	if len(cols.Rows) == 0 {
		return s, fmt.Errorf("mysql: %s.%s not found", schema, name)
	}
	for _, r := range cols.Rows {
		typ, extra := string(r[1].Raw), string(r[7].Raw)
		s.Fields = append(s.Fields, tile.FieldDef{
			Name: string(r[0].Raw), Type: typ, Nullable: string(r[2].Raw) == "YES",
			Default: mysqlDefault(r[3], typ, extra, string(r[9].Raw) == "1"),
			Comment: string(r[4].Raw), Primary: string(r[5].Raw) == "PRI", Collation: string(r[6].Raw),
			Extra: mysqlExtra(extra, string(r[8].Raw)),
		})
	}
	idx, err := t.section(ctx, "", structIndexesSQL, schema, name)
	if err != nil {
		return s, err
	}
	for _, r := range idx.Rows {
		ix := tile.IndexDef{
			Name: string(r[0].Raw), Fields: splitUnit(r[1]), Unique: string(r[2].Raw) == "0",
			Primary: string(r[0].Raw) == "PRIMARY", Method: strings.ToLower(string(r[3].Raw)),
		}
		kind := "INDEX"
		switch {
		case ix.Primary:
			kind = "PRIMARY KEY"
		case ix.Unique:
			kind = "UNIQUE INDEX"
		case ix.Method == "fulltext" || ix.Method == "spatial":
			kind = strings.ToUpper(ix.Method) + " INDEX"
		}
		ix.Definition = fmt.Sprintf("%s %s (%s)", kind, dialect.Quote(ix.Name), string(r[4].Raw))
		s.Indexes = append(s.Indexes, ix)
	}
	rels, err := t.section(ctx, "", structRelationsSQL, schema, name)
	if err != nil {
		return s, err
	}
	for _, r := range rels.Rows {
		s.Relations = append(s.Relations, tile.Relation{
			ForeignKey: tile.ForeignKey{
				Name: string(r[0].Raw), Fields: splitUnit(r[1]),
				Target:       tile.ObjectRef{}.Child("schema", string(r[2].Raw)).Child("table", string(r[3].Raw)),
				TargetFields: splitUnit(r[4]),
			},
			OnUpdate: string(r[5].Raw), OnDelete: string(r[6].Raw),
		})
	}
	trs, err := t.section(ctx, "", structTriggersSQL, schema, name)
	if err != nil {
		return s, err
	}
	for _, r := range trs.Rows {
		tr := tile.TriggerDef{
			Name: string(r[0].Raw), Timing: string(r[1].Raw), Events: []string{string(r[2].Raw)},
			ForEach: string(r[3].Raw), Action: string(r[4].Raw),
		}
		tr.Definition = fmt.Sprintf("CREATE TRIGGER %s %s %s ON %s FOR EACH %s %s",
			dialect.Quote(tr.Name), tr.Timing, tr.Events[0], dialect.Quote(name), tr.ForEach, tr.Action)
		s.Triggers = append(s.Triggers, tr)
	}
	return s, nil
}

// PlanStructure builds DDL. MySQL commits each statement, so the plan is per item.
func (t tree) PlanStructure(ref tile.ObjectRef, changes []tile.StructureChange) (tile.Plan, error) {
	if _, _, err := target(ref); err != nil {
		return tile.Plan{}, err
	}
	return dialect.PlanStructure(ref, changes)
}

// CreateScript returns the server's own SHOW CREATE text, then the table's triggers between
// DELIMITER lines, since a trigger body may hold semicolons.
func (t tree) CreateScript(ctx context.Context, ref tile.ObjectRef) (string, error) {
	schema, name, err := target(ref)
	if err != nil {
		return "", err
	}
	kind, err := t.section(ctx, "", tableKindSQL, schema, name)
	if err != nil {
		return "", err
	}
	if len(kind.Rows) == 0 {
		return "", fmt.Errorf("mysql: %s.%s not found", schema, name)
	}
	obj := dialect.Quote(schema) + "." + dialect.Quote(name)
	show := "SHOW CREATE TABLE " + obj
	view := string(kind.Rows[0][0].Raw) == "VIEW"
	if view {
		show = "SHOW CREATE VIEW " + obj
	}
	out, err := t.section(ctx, "", show)
	if err != nil {
		return "", err
	}
	if len(out.Rows) == 0 || len(out.Rows[0]) < 2 {
		return "", fmt.Errorf("mysql: no definition for %s.%s", schema, name)
	}
	var b strings.Builder
	b.WriteString(string(out.Rows[0][1].Raw) + ";\n")
	if view {
		return b.String(), nil
	}
	trs, err := t.section(ctx, "", structTriggersSQL, schema, name)
	if err != nil {
		return "", err
	}
	if len(trs.Rows) > 0 {
		b.WriteString("\nDELIMITER ;;\n")
		for _, r := range trs.Rows {
			fmt.Fprintf(&b, "CREATE TRIGGER %s %s %s ON %s FOR EACH %s %s;;\n",
				dialect.Quote(string(r[0].Raw)), r[1].Raw, r[2].Raw, dialect.Quote(name), r[3].Raw, r[4].Raw)
		}
		b.WriteString("DELIMITER ;\n")
	}
	return b.String(), nil
}

func (c *conn) InsertPlan(ref tile.ObjectRef, fields []string, rows [][]tile.Value) (tile.Plan, error) {
	return dialect.InsertPlan(ref, fields, rows)
}

func (c *conn) InsertScript(ref tile.ObjectRef, fields []string, rows [][]tile.Value) (string, error) {
	return dialect.InsertScript(ref, fields, rows)
}
