package sqlgen

import (
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/arunksoman/tessera/tile"
)

// Family selects statement forms that differ between SQL families beyond quoting.
type Family uint8

const (
	FamilyPostgres Family = iota
	FamilyMySQL
	FamilySQLite
)

// str renders a string literal for a statement that runs.
func (d Dialect) str(s string) string {
	if d.Family == FamilyMySQL {
		// Backslash is an escape character in MySQL strings unless NO_BACKSLASH_ESCAPES is set.
		s = strings.NewReplacer(`\`, `\\`, "\x00", `\0`).Replace(s)
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// PlanStructure turns structure changes into statements. PostgreSQL and SQLite run them in one
// transaction; MySQL commits each statement on its own, so the plan is per item there.
func (d Dialect) PlanStructure(ref tile.ObjectRef, changes []tile.StructureChange) (tile.Plan, error) {
	if len(ref.Path) == 0 {
		return tile.Plan{}, errors.New("no object")
	}
	if len(changes) == 0 {
		return tile.Plan{}, errors.New("no changes")
	}
	plan := tile.Plan{Atomicity: tile.AtomicPerItem}
	if d.Family != FamilyMySQL {
		plan = tile.Plan{
			Begin:    tile.Request{Text: d.Begin},
			Commit:   tile.Request{Text: "COMMIT"},
			Rollback: tile.Request{Text: "ROLLBACK"},
		}
	}
	for i, c := range changes {
		texts, err := d.change(ref, c)
		if err != nil {
			return tile.Plan{}, fmt.Errorf("change %d (%s): %w", i+1, c.Op, err)
		}
		for _, t := range texts {
			plan.Steps = append(plan.Steps, tile.PlanStep{Request: tile.Request{Text: t}, Preview: t, ExpectRows: -1})
		}
	}
	return plan, nil
}

func (d Dialect) change(ref tile.ObjectRef, c tile.StructureChange) ([]string, error) {
	obj := d.object(ref)
	kind := ref.Path[len(ref.Path)-1].Kind
	alter := "ALTER TABLE " + obj + " "
	one := func(s string) ([]string, error) { return []string{s}, nil }
	if d.Family == FamilySQLite {
		if out, err, ok := d.sqliteChange(ref, c); ok {
			return out, err
		}
	}
	switch c.Op {
	case tile.OpRename:
		if c.Name == "" {
			return nil, errors.New("no new name")
		}
		if d.Family == FamilyMySQL {
			return one("RENAME TABLE " + obj + " TO " + d.sibling(ref, c.Name))
		}
		return one(alter + "RENAME TO " + d.Quote(c.Name))
	case tile.OpTruncate:
		if kind != "table" {
			return nil, fmt.Errorf("cannot truncate a %s", kind)
		}
		return one("TRUNCATE TABLE " + obj)
	case tile.OpDrop:
		if kind == "view" {
			return one("DROP VIEW " + obj)
		}
		return one("DROP TABLE " + obj)
	case tile.OpFieldAdd:
		if c.Field == nil {
			return nil, errors.New("no field")
		}
		if d.Family == FamilyMySQL {
			def, err := d.mysqlField(*c.Field)
			if err != nil {
				return nil, err
			}
			return one(alter + "ADD COLUMN " + def)
		}
		def, err := d.pgField(*c.Field)
		if err != nil {
			return nil, err
		}
		out := []string{alter + "ADD COLUMN " + def}
		if c.Field.Comment != "" {
			out = append(out, d.pgComment(obj, c.Field.Name, c.Field.Comment))
		}
		return out, nil
	case tile.OpFieldAlter:
		if c.Field == nil || c.From == nil {
			return nil, errors.New("a field change needs the field as it was and as it becomes")
		}
		if d.Family == FamilyMySQL {
			if *c.Field == *c.From {
				return nil, nil
			}
			def, err := d.mysqlField(*c.Field)
			if err != nil {
				return nil, err
			}
			return one(alter + "CHANGE COLUMN " + d.Quote(c.From.Name) + " " + def)
		}
		return d.pgAlterField(obj, *c.From, *c.Field)
	case tile.OpFieldDrop:
		if c.Name == "" {
			return nil, errors.New("no field name")
		}
		return one(alter + "DROP COLUMN " + d.Quote(c.Name))
	case tile.OpIndexAdd:
		if c.Index == nil {
			return nil, errors.New("no index")
		}
		return d.addIndex(ref, *c.Index)
	case tile.OpIndexDrop:
		if c.Index == nil {
			return nil, errors.New("no index")
		}
		return d.dropIndex(ref, *c.Index)
	case tile.OpRelationAdd:
		if c.Relation == nil {
			return nil, errors.New("no relation")
		}
		def, err := d.relation(*c.Relation)
		if err != nil {
			return nil, err
		}
		return one(alter + "ADD " + def)
	case tile.OpRelationDrop:
		if c.Name == "" {
			return nil, errors.New("no relation name")
		}
		if d.Family == FamilyMySQL {
			return one(alter + "DROP FOREIGN KEY " + d.Quote(c.Name))
		}
		return one(alter + "DROP CONSTRAINT " + d.Quote(c.Name))
	case tile.OpTriggerAdd:
		if c.Trigger == nil {
			return nil, errors.New("no trigger")
		}
		return d.addTrigger(ref, *c.Trigger)
	case tile.OpTriggerDrop:
		if c.Name == "" {
			return nil, errors.New("no trigger name")
		}
		if d.Family == FamilyMySQL {
			return one("DROP TRIGGER " + d.sibling(ref, c.Name))
		}
		return one("DROP TRIGGER " + d.Quote(c.Name) + " ON " + obj)
	}
	return nil, fmt.Errorf("unknown change %q", c.Op)
}

// sibling names another object in the same schema as ref.
func (d Dialect) sibling(ref tile.ObjectRef, name string) string {
	return d.object(tile.ObjectRef{Path: ref.Path[:len(ref.Path)-1]}.Child("", name))
}

func (d Dialect) pgField(f tile.FieldDef) (string, error) {
	if err := needName("field", f.Name); err != nil {
		return "", err
	}
	if err := d.fragment("type", f.Type, false); err != nil {
		return "", err
	}
	s := d.Quote(f.Name) + " " + f.Type
	if f.Collation != "" {
		s += " COLLATE " + d.Quote(f.Collation)
	}
	if !f.Nullable {
		s += " NOT NULL"
	}
	if f.Default != "" {
		if err := d.fragment("default", f.Default, false); err != nil {
			return "", err
		}
		s += " DEFAULT " + f.Default
	}
	if f.Extra != "" {
		if err := d.fragment("extra", f.Extra, false); err != nil {
			return "", err
		}
		s += " " + f.Extra
	}
	return s, nil
}

func (d Dialect) pgComment(obj, field, comment string) string {
	text := "NULL"
	if comment != "" {
		text = d.str(comment)
	}
	return "COMMENT ON COLUMN " + obj + "." + d.Quote(field) + " IS " + text
}

func (d Dialect) pgAlterField(obj string, from, to tile.FieldDef) ([]string, error) {
	if err := needName("field", to.Name); err != nil {
		return nil, err
	}
	alter := "ALTER TABLE " + obj + " "
	col := d.Quote(to.Name)
	var out []string
	if from.Name != to.Name {
		out = append(out, alter+"RENAME COLUMN "+d.Quote(from.Name)+" TO "+col)
	}
	if from.Type != to.Type || from.Collation != to.Collation {
		if err := d.fragment("type", to.Type, false); err != nil {
			return nil, err
		}
		s := alter + "ALTER COLUMN " + col + " TYPE " + to.Type
		if to.Collation != "" {
			s += " COLLATE " + d.Quote(to.Collation)
		}
		out = append(out, s)
	}
	if from.Default != to.Default {
		if to.Default == "" {
			out = append(out, alter+"ALTER COLUMN "+col+" DROP DEFAULT")
		} else {
			if err := d.fragment("default", to.Default, false); err != nil {
				return nil, err
			}
			out = append(out, alter+"ALTER COLUMN "+col+" SET DEFAULT "+to.Default)
		}
	}
	if from.Nullable != to.Nullable {
		if to.Nullable {
			out = append(out, alter+"ALTER COLUMN "+col+" DROP NOT NULL")
		} else {
			out = append(out, alter+"ALTER COLUMN "+col+" SET NOT NULL")
		}
	}
	if from.Comment != to.Comment {
		out = append(out, d.pgComment(obj, to.Name, to.Comment))
	}
	if from.Extra != to.Extra {
		return nil, errors.New("identity and generated settings cannot be changed here; use SQL")
	}
	return out, nil
}

func (d Dialect) mysqlField(f tile.FieldDef) (string, error) {
	if err := needName("field", f.Name); err != nil {
		return "", err
	}
	if err := d.fragment("type", f.Type, false); err != nil {
		return "", err
	}
	s := d.Quote(f.Name) + " " + f.Type
	if f.Collation != "" && textual.MatchString(f.Type) {
		s += " COLLATE " + d.Quote(f.Collation)
	}
	generated := strings.Contains(strings.ToUpper(f.Extra), "GENERATED ALWAYS")
	if generated {
		// The expression comes before nullability in a generated column's definition.
		if err := d.fragment("extra", f.Extra, false); err != nil {
			return "", err
		}
		s += " " + f.Extra
	}
	if f.Nullable {
		s += " NULL"
	} else {
		s += " NOT NULL"
	}
	if f.Default != "" {
		if err := d.fragment("default", f.Default, false); err != nil {
			return "", err
		}
		s += " DEFAULT " + f.Default
	}
	if f.Extra != "" && !generated {
		if err := d.fragment("extra", f.Extra, false); err != nil {
			return "", err
		}
		s += " " + f.Extra
	}
	if f.Comment != "" {
		s += " COMMENT " + d.str(f.Comment)
	}
	return s, nil
}

// textual matches MySQL types that take a collation.
var textual = regexp.MustCompile(`(?i)(char|text|enum|set)\b`)

func (d Dialect) columns(fields []string) (string, error) {
	if len(fields) == 0 {
		return "", errors.New("no fields")
	}
	out := make([]string, len(fields))
	for i, f := range fields {
		f = strings.TrimSpace(f)
		switch {
		case f == "":
			return "", errors.New("empty field name")
		case strings.HasPrefix(f, "(") && strings.HasSuffix(f, ")"):
			// An expression, as an expression index lists it.
			if err := d.fragment("expression", f, false); err != nil {
				return "", err
			}
			out[i] = f
		default:
			out[i] = d.Quote(f)
		}
	}
	return "(" + strings.Join(out, ", ") + ")", nil
}

var pgMethod = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

func (d Dialect) addIndex(ref tile.ObjectRef, ix tile.IndexDef) ([]string, error) {
	obj := d.object(ref)
	cols, err := d.columns(ix.Fields)
	if err != nil {
		return nil, err
	}
	named := ""
	if ix.Name != "" {
		named = d.Quote(ix.Name) + " "
	}
	method := strings.ToLower(strings.TrimSpace(ix.Method))
	if d.Family == FamilyMySQL {
		if ix.Primary {
			return []string{"ALTER TABLE " + obj + " ADD PRIMARY KEY " + cols}, nil
		}
		kind, using := "INDEX ", ""
		switch method {
		case "", "btree", "hash":
			if method != "" {
				using = " USING " + strings.ToUpper(method)
			}
			if ix.Unique {
				kind = "UNIQUE INDEX "
			}
		case "fulltext", "spatial":
			kind = strings.ToUpper(method) + " INDEX "
		default:
			return nil, fmt.Errorf("unknown index method %q", ix.Method)
		}
		return []string{"ALTER TABLE " + obj + " ADD " + kind + named + cols + using}, nil
	}
	constraint := ""
	if ix.Name != "" {
		constraint = "CONSTRAINT " + d.Quote(ix.Name) + " "
	}
	switch {
	case ix.Primary:
		return []string{"ALTER TABLE " + obj + " ADD " + constraint + "PRIMARY KEY " + cols}, nil
	case ix.Unique && ix.Constraint:
		return []string{"ALTER TABLE " + obj + " ADD " + constraint + "UNIQUE " + cols}, nil
	}
	if method != "" && !pgMethod.MatchString(method) {
		return nil, fmt.Errorf("unknown index method %q", ix.Method)
	}
	s := "CREATE "
	if ix.Unique {
		s += "UNIQUE "
	}
	s += "INDEX " + named + "ON " + obj + " "
	if method != "" {
		s += "USING " + method + " "
	}
	return []string{s + cols}, nil
}

func (d Dialect) dropIndex(ref tile.ObjectRef, ix tile.IndexDef) ([]string, error) {
	obj := d.object(ref)
	if d.Family == FamilyMySQL {
		if ix.Primary {
			return []string{"ALTER TABLE " + obj + " DROP PRIMARY KEY"}, nil
		}
		if err := needName("index", ix.Name); err != nil {
			return nil, err
		}
		return []string{"ALTER TABLE " + obj + " DROP INDEX " + d.Quote(ix.Name)}, nil
	}
	if err := needName("index", ix.Name); err != nil {
		return nil, err
	}
	if ix.Primary || ix.Constraint {
		return []string{"ALTER TABLE " + obj + " DROP CONSTRAINT " + d.Quote(ix.Name)}, nil
	}
	return []string{"DROP INDEX " + d.sibling(ref, ix.Name)}, nil
}

func (d Dialect) relation(r tile.Relation) (string, error) {
	if len(r.Fields) != len(r.TargetFields) {
		return "", fmt.Errorf("%d fields refer to %d target fields", len(r.Fields), len(r.TargetFields))
	}
	if len(r.Target.Path) == 0 {
		return "", errors.New("no target")
	}
	from, err := d.columns(r.Fields)
	if err != nil {
		return "", err
	}
	to, err := d.columns(r.TargetFields)
	if err != nil {
		return "", err
	}
	s := ""
	if r.Name != "" {
		s = "CONSTRAINT " + d.Quote(r.Name) + " "
	}
	s += "FOREIGN KEY " + from + " REFERENCES " + d.object(r.Target) + " " + to
	for _, a := range []struct{ on, action string }{{"UPDATE", r.OnUpdate}, {"DELETE", r.OnDelete}} {
		if a.action == "" {
			continue
		}
		act := strings.ToUpper(strings.TrimSpace(a.action))
		if !slices.Contains(tile.RelationActions, act) {
			return "", fmt.Errorf("unknown action %q", a.action)
		}
		s += " ON " + a.on + " " + act
	}
	return s, nil
}

var triggerEvents = []string{"INSERT", "UPDATE", "DELETE", "TRUNCATE"}

func (d Dialect) addTrigger(ref tile.ObjectRef, tr tile.TriggerDef) ([]string, error) {
	if err := needName("trigger", tr.Name); err != nil {
		return nil, err
	}
	timing := strings.ToUpper(strings.Join(strings.Fields(tr.Timing), " "))
	if !slices.Contains([]string{"BEFORE", "AFTER", "INSTEAD OF"}, timing) {
		return nil, fmt.Errorf("unknown timing %q", tr.Timing)
	}
	if len(tr.Events) == 0 {
		return nil, errors.New("no events")
	}
	events := make([]string, len(tr.Events))
	for i, e := range tr.Events {
		events[i] = strings.ToUpper(strings.TrimSpace(e))
		if !slices.Contains(triggerEvents, events[i]) {
			return nil, fmt.Errorf("unknown event %q", e)
		}
	}
	forEach := strings.ToUpper(strings.TrimSpace(tr.ForEach))
	if forEach != "" && forEach != "ROW" && forEach != "STATEMENT" {
		return nil, fmt.Errorf("unknown level %q", tr.ForEach)
	}
	action := strings.TrimSpace(tr.Action)
	if action == "" {
		return nil, errors.New("no action")
	}
	// A MySQL trigger body may be a BEGIN ... END block of several statements.
	if err := d.fragment("action", action, d.Family == FamilyMySQL); err != nil {
		return nil, err
	}
	obj := d.object(ref)
	if d.Family == FamilyMySQL {
		if len(events) != 1 || events[0] == "TRUNCATE" {
			return nil, errors.New("a MySQL trigger fires on one of INSERT, UPDATE, or DELETE")
		}
		if timing == "INSTEAD OF" || forEach == "STATEMENT" || tr.Condition != "" {
			return nil, errors.New("MySQL triggers run before or after each row, with no condition")
		}
		return []string{fmt.Sprintf("CREATE TRIGGER %s %s %s ON %s FOR EACH ROW %s",
			d.sibling(ref, tr.Name), timing, events[0], obj, action)}, nil
	}
	s := fmt.Sprintf("CREATE TRIGGER %s %s %s ON %s", d.Quote(tr.Name), timing, strings.Join(events, " OR "), obj)
	if forEach != "" {
		s += " FOR EACH " + forEach
	}
	if tr.Condition != "" {
		if err := d.fragment("condition", tr.Condition, false); err != nil {
			return nil, err
		}
		s += " WHEN (" + tr.Condition + ")"
	}
	return []string{s + " " + action}, nil
}

func needName(what, name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("the %s needs a name", what)
	}
	return nil
}

// fragment checks text the user typed for part of a statement, such as a type or a default.
// It must not end the statement or start a comment that hides the rest of it. Each step runs
// as one statement anyway; this keeps a typing slip from turning into something else.
func (d Dialect) fragment(what, s string, semicolons bool) error {
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("the %s is empty", what)
	}
	var quote byte
	depth := 0
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if quote != 0 {
			switch {
			case ch == '\\' && quote == '\'' && d.Family == FamilyMySQL:
				i++
			case ch == quote:
				quote = 0
			}
			continue
		}
		next := byte(0)
		if i+1 < len(s) {
			next = s[i+1]
		}
		switch {
		case ch == '\'' || ch == '"' || (ch == '`' && d.Family != FamilyPostgres):
			quote = ch
		case ch == '[' && d.Family == FamilySQLite:
			quote = ']'
		case ch == '(':
			depth++
		case ch == ')':
			depth--
			if depth < 0 {
				return fmt.Errorf("the %s closes a parenthesis it did not open", what)
			}
		case ch == ';' && !semicolons:
			return fmt.Errorf("the %s holds a semicolon outside quotes", what)
		case ch == '-' && next == '-', ch == '/' && next == '*', ch == '#' && d.Family == FamilyMySQL:
			return fmt.Errorf("the %s holds a comment", what)
		}
	}
	if quote != 0 {
		return fmt.Errorf("the %s has an unclosed quote", what)
	}
	if depth != 0 {
		return fmt.Errorf("the %s has an unclosed parenthesis", what)
	}
	return nil
}

// InsertPlan adds rows in one statement, with values bound. Rows go in one transaction.
func (d Dialect) InsertPlan(ref tile.ObjectRef, fields []string, rows [][]tile.Value) (tile.Plan, error) {
	if len(rows) == 0 {
		return tile.Plan{}, errors.New("no rows")
	}
	b := &builder{d: d}
	var text, preview strings.Builder
	head, err := d.insertHead(ref, fields)
	if err != nil {
		return tile.Plan{}, err
	}
	text.WriteString(head)
	preview.WriteString(head)
	for r, row := range rows {
		if len(row) != len(fields) {
			return tile.Plan{}, fmt.Errorf("row %d has %d values, want %d", r+1, len(row), len(fields))
		}
		sep := "("
		if r > 0 {
			sep = ", ("
		}
		text.WriteString(sep)
		preview.WriteString(sep)
		for i, v := range row {
			if i > 0 {
				text.WriteString(", ")
				preview.WriteString(", ")
			}
			text.WriteString(b.bind(v))
			preview.WriteString(literal(v))
		}
		text.WriteString(")")
		preview.WriteString(")")
	}
	return tile.Plan{
		Begin:    tile.Request{Text: d.Begin},
		Commit:   tile.Request{Text: "COMMIT"},
		Rollback: tile.Request{Text: "ROLLBACK"},
		Steps: []tile.PlanStep{{
			Request: tile.Request{Text: text.String(), Args: b.args}, Preview: preview.String(), ExpectRows: int64(len(rows)),
		}},
	}, nil
}

func (d Dialect) insertHead(ref tile.ObjectRef, fields []string) (string, error) {
	if len(fields) == 0 {
		return "", errors.New("no fields")
	}
	cols := make([]string, len(fields))
	for i, f := range fields {
		if f == "" {
			return "", errors.New("empty field name")
		}
		cols[i] = d.Quote(f)
	}
	return "INSERT INTO " + d.object(ref) + " (" + strings.Join(cols, ", ") + ") VALUES ", nil
}

var number = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?([eE][-+]?[0-9]+)?$`)

// InsertScript renders rows as one INSERT statement with literal values, to run elsewhere.
func (d Dialect) InsertScript(ref tile.ObjectRef, fields []string, rows [][]tile.Value) (string, error) {
	if len(rows) == 0 {
		return "", nil
	}
	head, err := d.insertHead(ref, fields)
	if err != nil {
		return "", err
	}
	var s strings.Builder
	s.WriteString(strings.TrimSuffix(head, " "))
	for r, row := range rows {
		if len(row) != len(fields) {
			return "", fmt.Errorf("row %d has %d values, want %d", r+1, len(row), len(fields))
		}
		if r > 0 {
			s.WriteString(",")
		}
		s.WriteString("\n(")
		for i, v := range row {
			if i > 0 {
				s.WriteString(", ")
			}
			s.WriteString(d.value(v))
		}
		s.WriteString(")")
	}
	s.WriteString(";\n")
	return s.String(), nil
}

// value renders a value as a literal that the server reads back as the same value.
func (d Dialect) value(v tile.Value) string {
	raw := string(v.Raw)
	switch v.Kind {
	case tile.KindNull:
		return "NULL"
	case tile.KindInt, tile.KindDecimal, tile.KindFloat:
		if number.MatchString(raw) {
			return raw
		}
	case tile.KindBool:
		switch raw {
		case "t", "true", "1":
			return "TRUE"
		case "f", "false", "0":
			return "FALSE"
		}
	case tile.KindBytes:
		if d.Family != FamilyPostgres {
			return "X'" + hex.EncodeToString(v.Raw) + "'"
		}
		return `'\x` + hex.EncodeToString(v.Raw) + "'"
	}
	return d.str(raw)
}

// ColumnDef renders a field as a table definition lists it.
func (d Dialect) ColumnDef(f tile.FieldDef) (string, error) {
	if d.Family == FamilyMySQL {
		return d.mysqlField(f)
	}
	return d.pgField(f)
}

// Literal renders a string literal for a statement that runs.
func (d Dialect) Literal(s string) string { return d.str(s) }

// sqliteChange plans the changes SQLite makes differently, and refuses those that need the table
// rebuilt, which SQLite's ALTER TABLE cannot do in place. ok is false for changes planned as for
// PostgreSQL.
func (d Dialect) sqliteChange(ref tile.ObjectRef, c tile.StructureChange) (out []string, err error, ok bool) {
	obj := d.object(ref)
	table := d.Quote(ref.Path[len(ref.Path)-1].Name) // SQLite names the schema on the index or trigger, not on its table
	rebuild := func(what string) ([]string, error, bool) {
		return nil, fmt.Errorf("SQLite cannot %s without rebuilding the table; use SQL", what), true
	}
	switch c.Op {
	case tile.OpTruncate:
		if kind := ref.Path[len(ref.Path)-1].Kind; kind != "table" {
			return nil, fmt.Errorf("cannot truncate a %s", kind), true
		}
		return []string{"DELETE FROM " + obj}, nil, true
	case tile.OpFieldAdd:
		if c.Field != nil && c.Field.Comment != "" {
			return nil, errors.New("SQLite keeps no comments"), true
		}
	case tile.OpFieldAlter:
		if c.Field == nil || c.From == nil {
			return nil, errors.New("a field change needs the field as it was and as it becomes"), true
		}
		renamed := *c.From
		renamed.Name = c.Field.Name
		if renamed != *c.Field {
			return rebuild("change a field's type, default, nullability, or comment")
		}
		if c.From.Name == c.Field.Name {
			return nil, nil, true
		}
		if err := needName("field", c.Field.Name); err != nil {
			return nil, err, true
		}
		return []string{"ALTER TABLE " + obj + " RENAME COLUMN " + d.Quote(c.From.Name) + " TO " + d.Quote(c.Field.Name)}, nil, true
	case tile.OpIndexAdd:
		if c.Index == nil {
			return nil, errors.New("no index"), true
		}
		ix := *c.Index
		if ix.Primary || ix.Constraint {
			return rebuild("add a key")
		}
		if ix.Method != "" {
			return nil, fmt.Errorf("SQLite has no index method %q", ix.Method), true
		}
		if err := needName("index", ix.Name); err != nil {
			return nil, err, true
		}
		cols, err := d.columns(ix.Fields)
		if err != nil {
			return nil, err, true
		}
		s := "CREATE "
		if ix.Unique {
			s += "UNIQUE "
		}
		return []string{s + "INDEX " + d.sibling(ref, ix.Name) + " ON " + table + " " + cols}, nil, true
	case tile.OpIndexDrop:
		if c.Index != nil && (c.Index.Primary || c.Index.Constraint) {
			return rebuild("drop a key")
		}
	case tile.OpRelationAdd, tile.OpRelationDrop:
		return rebuild("add or drop a relation")
	case tile.OpTriggerAdd:
		if c.Trigger == nil {
			return nil, errors.New("no trigger"), true
		}
		return d.sqliteTrigger(ref, table, *c.Trigger)
	case tile.OpTriggerDrop:
		if err := needName("trigger", c.Name); err != nil {
			return nil, err, true
		}
		return []string{"DROP TRIGGER " + d.sibling(ref, c.Name)}, nil, true
	}
	return nil, nil, false
}

func (d Dialect) sqliteTrigger(ref tile.ObjectRef, table string, tr tile.TriggerDef) ([]string, error, bool) {
	if err := needName("trigger", tr.Name); err != nil {
		return nil, err, true
	}
	timing := strings.ToUpper(strings.Join(strings.Fields(tr.Timing), " "))
	if !slices.Contains([]string{"BEFORE", "AFTER", "INSTEAD OF"}, timing) {
		return nil, fmt.Errorf("unknown timing %q", tr.Timing), true
	}
	if len(tr.Events) != 1 {
		return nil, errors.New("a SQLite trigger fires on one of INSERT, UPDATE, or DELETE"), true
	}
	event := strings.ToUpper(strings.TrimSpace(tr.Events[0]))
	if !slices.Contains([]string{"INSERT", "UPDATE", "DELETE"}, event) {
		return nil, errors.New("a SQLite trigger fires on one of INSERT, UPDATE, or DELETE"), true
	}
	if f := strings.ToUpper(strings.TrimSpace(tr.ForEach)); f != "" && f != "ROW" {
		return nil, errors.New("SQLite triggers run for each row"), true
	}
	// The body is one or more statements, each ending in a semicolon.
	action := strings.TrimRight(strings.TrimSpace(tr.Action), ";")
	if action == "" {
		return nil, errors.New("no action"), true
	}
	if err := d.fragment("action", action, true); err != nil {
		return nil, err, true
	}
	s := fmt.Sprintf("CREATE TRIGGER %s %s %s ON %s FOR EACH ROW", d.sibling(ref, tr.Name), timing, event, table)
	if tr.Condition != "" {
		if err := d.fragment("condition", tr.Condition, false); err != nil {
			return nil, err, true
		}
		s += " WHEN (" + tr.Condition + ")"
	}
	return []string{s + " BEGIN " + action + "; END"}, nil, true
}
