package sqlish

import (
	"slices"
	"strings"

	"github.com/arunksoman/tessera/tile"
)

// Clause is where the cursor sits inside a statement.
type Clause uint8

const (
	ClauseStart  Clause = iota // no verb yet
	ClauseTables               // a table name is expected: FROM, JOIN, INTO, UPDATE, TABLE
	ClauseValues               // a column or expression is expected
)

// TableRef is a table in scope at the cursor.
type TableRef struct {
	Schema, Name, Alias string
	// Columns is set for a subquery or CTE, whose columns come from its own select list.
	Columns []string
	Derived bool
}

// Cursor describes the text around a cursor position.
type Cursor struct {
	Clause    Clause
	Prefix    string // the partial word before the cursor
	Qualifier string // "a" in "a.na|"
	Tables    []TableRef
	CTEs      []string
	Quiet     bool // inside a string or comment, where nothing should be offered
}

// Locate is tolerant: the statement being typed is almost always invalid.
func (d Dialect) Locate(text string, cursor int) Cursor {
	cursor = min(max(cursor, 0), len(text))
	before := text[:cursor]
	if d.inStringOrComment(before) {
		return Cursor{Quiet: true}
	}
	// Only the statement holding the cursor counts.
	toks := d.lex(text)
	start, end := 0, len(toks)
	for i, t := range toks {
		if t.kind != tokSemi {
			continue
		}
		if t.end <= cursor {
			start = i + 1
		} else {
			end = i
			break
		}
	}
	toks = toks[start:end]

	var cur Cursor
	cur.Prefix, cur.Qualifier = partialWord(before)
	tokenStart := cursor - len(cur.Prefix)
	if cur.Qualifier != "" {
		tokenStart -= len(cur.Qualifier) + 1
	}
	var prior []token
	for _, t := range toks {
		if t.start < tokenStart {
			prior = append(prior, t)
		}
	}
	cur.Clause = clauseOf(prior)
	cur.CTEs = cteNames(text, toks)
	cur.Tables = d.scope(text, toks, cursor)
	return cur
}

func (d Dialect) inStringOrComment(before string) bool {
	toks := d.lex(before)
	last := 0
	if len(toks) > 0 {
		t := toks[len(toks)-1]
		last = t.end
		if (t.kind == tokString || t.kind == tokIdent) && t.end == len(before) && !closed(before[t.start:t.end]) {
			return true
		}
	}
	gap := before[last:]
	if strings.TrimSpace(gap) == "" {
		return false
	}
	// Text after the last token that produced no token is a comment. A line comment ends at
	// its newline, and a block comment at its closer.
	switch {
	case strings.Contains(gap, "/*"):
		return !strings.HasSuffix(strings.TrimRight(gap, " \t\r\n"), "*/")
	default:
		return !strings.Contains(gap[strings.IndexAny(gap, "-#"):], "\n")
	}
}

// closed reports whether a quoted token has its closing quote.
func closed(s string) bool {
	if strings.HasPrefix(s, "$") {
		tag := s[:strings.IndexByte(s[1:], '$')+2]
		return len(s) >= 2*len(tag) && strings.HasSuffix(s, tag)
	}
	if s[0] == 'e' || s[0] == 'E' {
		s = s[1:]
	}
	if len(s) < 2 || s[len(s)-1] != s[0] {
		return false
	}
	// An odd run of quotes at the end means the last one closes the string.
	run := 0
	for i := len(s) - 1; i > 0 && s[i] == s[0]; i-- {
		run++
	}
	return run%2 == 1
}

func partialWord(before string) (prefix, qualifier string) {
	i := len(before)
	for i > 0 && isWordPart(before[i-1]) {
		i--
	}
	prefix = before[i:]
	if i > 0 && before[i-1] == '.' {
		j := i - 1
		for j > 0 && isWordPart(before[j-1]) {
			j--
		}
		qualifier = before[j : i-1]
	}
	return prefix, qualifier
}

// clauseOf tracks the clause at the cursor's own paren depth.
func clauseOf(prior []token) Clause {
	depth := 0
	if len(prior) > 0 {
		depth = parenAfter(prior)
	}
	clause := ClauseStart
	for _, t := range prior {
		if t.kind != tokWord || t.paren != depth {
			continue
		}
		switch t.upper {
		case "FROM", "JOIN", "INTO", "UPDATE", "TABLE":
			clause = ClauseTables
		case "SELECT", "WHERE", "ON", "SET", "GROUP", "ORDER", "HAVING", "BY", "AND", "OR", "VALUES", "RETURNING", "USING", "LIMIT":
			clause = ClauseValues
		case "DELETE", "INSERT":
			if clause == ClauseStart {
				clause = ClauseValues
			}
		}
	}
	// After a table name, the next word is an alias or a keyword, not another table,
	// unless a comma asks for one more.
	if clause == ClauseTables && len(prior) > 0 {
		last := prior[len(prior)-1]
		if last.kind != tokWord || !slices.Contains([]string{"FROM", "JOIN", "INTO", "UPDATE", "TABLE"}, last.upper) {
			if last.comma {
				return ClauseTables
			}
			return ClauseValues
		}
	}
	return clause
}

// parenAfter is the depth just after the last token, which differs from its own after "(".
func parenAfter(toks []token) int {
	last := toks[len(toks)-1]
	if last.open {
		return last.paren + 1
	}
	return last.paren
}

// cteNames finds "WITH a AS (...), b AS (...)".
func cteNames(text string, toks []token) []string {
	var out []string
	for i := 0; i+1 < len(toks); i++ {
		if toks[i].upper == "WITH" && toks[i+1].kind != tokOther {
			next := i + 1
			if toks[next].upper == "RECURSIVE" {
				next++
			}
			for next+1 < len(toks) {
				name := identText(text, toks[next])
				j := next + 1
				if toks[j].kind == tokOther && toks[j].open { // column list
					depth := toks[j].paren
					for j < len(toks) && !(toks[j].close && toks[j].paren == depth) {
						j++
					}
					j++
				}
				if j >= len(toks) || toks[j].upper != "AS" {
					break
				}
				out = append(out, name)
				j++
				for j < len(toks) && toks[j].upper != "" { // NOT MATERIALIZED and similar
					j++
				}
				if j >= len(toks) || !toks[j].open {
					break
				}
				depth := toks[j].paren
				for j < len(toks) && !(toks[j].close && toks[j].paren == depth) {
					j++
				}
				j++
				if j >= len(toks) || !toks[j].comma {
					break
				}
				next = j + 1
			}
			break
		}
	}
	return out
}

func identText(text string, t token) string {
	s := text[t.start:t.end]
	if t.kind == tokIdent && len(s) >= 2 {
		q := s[:1]
		return strings.ReplaceAll(s[1:len(s)-1], q+q, q)
	}
	return s
}

// scope collects tables visible at the cursor: those in its own query block and in every
// block enclosing it, including aliases of subqueries.
func (d Dialect) scope(text string, toks []token, cursor int) []TableRef {
	blocks := enclosingBlocks(toks, cursor)
	var out []TableRef
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t.kind != tokWord || !(t.upper == "FROM" || t.upper == "JOIN" || t.upper == "UPDATE" || t.upper == "INTO") {
			continue
		}
		if !blocks[blockOf(toks, i)] {
			continue
		}
		for j := i + 1; j < len(toks); {
			ref, next := d.tableAt(text, toks, j)
			if next == j {
				break
			}
			out = append(out, ref)
			j = next
			if j >= len(toks) || !toks[j].comma {
				break
			}
			j++
		}
	}
	return out
}

// blockOf identifies a query block by the index of its opening paren, or -1 at the top.
func blockOf(toks []token, at int) int {
	depth := toks[at].paren
	for i := at - 1; i >= 0; i-- {
		if toks[i].open && toks[i].paren == depth-1 {
			return i
		}
	}
	return -1
}

func enclosingBlocks(toks []token, cursor int) map[int]bool {
	blocks := map[int]bool{-1: true}
	var stack []int
	for i, t := range toks {
		if t.start >= cursor {
			break
		}
		if t.open {
			stack = append(stack, i)
		}
		if t.close && len(stack) > 0 {
			stack = stack[:len(stack)-1]
		}
	}
	for _, i := range stack {
		blocks[i] = true
	}
	return blocks
}

// tableAt reads "schema.name [AS] alias" or "(subquery) [AS] alias".
func (d Dialect) tableAt(text string, toks []token, i int) (TableRef, int) {
	var ref TableRef
	if i >= len(toks) {
		return ref, i
	}
	if toks[i].open {
		depth := toks[i].paren
		j := i + 1
		for j < len(toks) && !(toks[j].close && toks[j].paren == depth) {
			j++
		}
		ref.Derived = true
		ref.Columns = selectList(text, toks[i+1:min(j, len(toks))])
		i = j + 1
	} else {
		if !nameToken(toks[i]) {
			return ref, i
		}
		ref.Name = identText(text, toks[i])
		i++
		if i+1 < len(toks) && text[toks[i].start:toks[i].end] == "." && nameToken(toks[i+1]) {
			ref.Schema, ref.Name = ref.Name, identText(text, toks[i+1])
			i += 2
		}
	}
	if i < len(toks) && toks[i].upper == "AS" {
		i++
	}
	if i < len(toks) && nameToken(toks[i]) {
		ref.Alias = identText(text, toks[i])
		i++
	}
	return ref, i
}

func nameToken(t token) bool {
	return t.kind == tokIdent || (t.kind == tokWord && !slices.Contains(stopWords, t.upper))
}

var stopWords = []string{
	"WHERE", "ON", "USING", "JOIN", "LEFT", "RIGHT", "INNER", "OUTER", "FULL", "CROSS", "NATURAL",
	"GROUP", "ORDER", "HAVING", "LIMIT", "OFFSET", "UNION", "EXCEPT", "INTERSECT", "SET", "VALUES",
	"SELECT", "FROM", "AS", "RETURNING", "FOR", "WINDOW", "FETCH", "LATERAL", "ONLY",
}

// selectList returns the output names of "SELECT a, b AS c, t.d FROM ...".
func selectList(text string, toks []token) []string {
	if len(toks) == 0 || toks[0].upper != "SELECT" {
		return nil
	}
	depth := toks[0].paren
	var out []string
	last := ""
	flush := func() {
		if last != "" {
			out = append(out, last)
		}
		last = ""
	}
	for _, t := range toks[1:] {
		if t.paren == depth && t.upper == "FROM" {
			break
		}
		if t.paren == depth && t.comma {
			flush()
			continue
		}
		if t.paren == depth && (t.kind == tokWord || t.kind == tokIdent) {
			last = identText(text, t)
		}
	}
	flush()
	return out
}

// Complete implements tile.Completer.
func (l language) Complete(text string, cursor int, cat tile.Catalog) (string, []tile.Candidate) {
	cur := l.Locate(text, cursor)
	if cur.Quiet {
		return "", nil
	}
	var out []tile.Candidate
	seen := map[string]bool{}
	add := func(s, kind string) {
		if !seen[s] && strings.HasPrefix(strings.ToLower(s), strings.ToLower(cur.Prefix)) && !strings.EqualFold(s, cur.Prefix) {
			seen[s] = true
			out = append(out, tile.Candidate{Text: s, Kind: kind})
		}
	}
	columnsOf := func(r TableRef) []string {
		if r.Derived {
			return r.Columns
		}
		return cat.Columns(tile.ObjectName{Schema: r.Schema, Name: r.Name})
	}

	if cur.Qualifier != "" {
		for _, r := range cur.Tables {
			if strings.EqualFold(r.Alias, cur.Qualifier) || (r.Alias == "" && strings.EqualFold(r.Name, cur.Qualifier)) {
				for _, c := range columnsOf(r) {
					add(c, "column")
				}
			}
		}
		for _, t := range cat.Tables() {
			if strings.EqualFold(t.Schema, cur.Qualifier) {
				add(t.Name, "table")
			}
		}
		return cur.Prefix, out
	}
	addTables := func() {
		for _, c := range cur.CTEs {
			add(c, "table")
		}
		tables := cat.Tables()
		for _, t := range tables {
			add(t.Name, "table")
		}
		for _, t := range tables {
			add(t.Schema, "schema")
		}
	}
	switch cur.Clause {
	case ClauseTables:
		addTables()
	case ClauseValues:
		for _, r := range cur.Tables {
			for _, c := range columnsOf(r) {
				add(c, "column")
			}
		}
		for _, r := range cur.Tables {
			if r.Alias != "" {
				add(r.Alias, "table")
			} else if r.Name != "" {
				add(r.Name, "table")
			}
		}
		for _, f := range commonFunctions {
			add(f, "function")
		}
		for _, k := range valueKeywords {
			add(k, "keyword")
		}
	default:
		for _, k := range startKeywords {
			add(k, "keyword")
		}
	}
	return cur.Prefix, out
}

// shortcut: a static list, until keywords and functions are read from the live server.
var (
	startKeywords = []string{
		"SELECT", "INSERT INTO", "UPDATE", "DELETE FROM", "WITH", "EXPLAIN", "BEGIN", "COMMIT", "ROLLBACK",
		"CREATE", "ALTER", "DROP", "SHOW", "SET",
	}
	valueKeywords = []string{
		"FROM", "WHERE", "AND", "OR", "NOT", "NULL", "IS", "IN", "LIKE", "BETWEEN", "CASE", "WHEN", "THEN", "ELSE", "END",
		"JOIN", "LEFT JOIN", "RIGHT JOIN", "INNER JOIN", "ON", "USING", "AS", "DISTINCT",
		"GROUP BY", "ORDER BY", "HAVING", "LIMIT", "OFFSET", "ASC", "DESC", "UNION", "VALUES", "SET", "RETURNING",
	}
	commonFunctions = []string{"COUNT", "SUM", "AVG", "MIN", "MAX", "COALESCE", "LOWER", "UPPER", "LENGTH", "NOW", "CAST", "ROUND"}
)
