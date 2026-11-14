// Package sqlish is a tolerant SQL lexer shared by the SQL tiles. It never fails on
// invalid input, because statements being typed are almost always invalid.
//
// Its classifier is a guardrail, not a security boundary. See tile.Classification.
package sqlish

import (
	"slices"
	"strings"

	"github.com/arunksoman/tessera/tile"
)

// Dialect holds the lexing rules that differ between SQL families.
type Dialect struct {
	id              string
	backslashString bool // 'a\'b' is one string
	hashComment     bool
	backtickIdent   bool
	dollarQuote     bool
	nestedComments  bool
	dashNeedsSpace  bool // "--" starts a comment only before whitespace
	execComments    bool // /*! ... */ runs as code
	delimiterCmd    bool // the mysql client's DELIMITER command
}

var (
	Postgres = Dialect{id: "sql-pg", dollarQuote: true, nestedComments: true}
	MySQL    = Dialect{id: "sql-mysql", backslashString: true, hashComment: true, backtickIdent: true, dashNeedsSpace: true, execComments: true, delimiterCmd: true}
)

// Language returns the dialect as a tile.Language.
func (d Dialect) Language() tile.Language { return language{d} }

type language struct{ Dialect }

func (l language) ID() string { return l.id }

type tokenKind uint8

const (
	tokWord tokenKind = iota
	tokString
	tokIdent // quoted identifier
	tokOther
	tokSemi
)

type token struct {
	kind       tokenKind
	start, end int
	upper      string // tokWord only
	paren      int    // depth of the enclosing parens
	open       bool   // "("
	close      bool   // ")"
	comma      bool
}

// lex scans text. Comments produce no tokens, except executable comments, whose bodies
// are scanned as code.
func (d Dialect) lex(s string) []token { return d.lexUntil(s, nil) }

// lexUntil lexes like lex but stops after the first token stop accepts. Between tokens the
// lexer holds no state except paren depth, so lexing again from that token's end continues
// exactly where it stopped.
func (d Dialect) lexUntil(s string, stop func(token) bool) []token {
	var toks []token
	depth := 0
	done := false
	emit := func(k tokenKind, start, end int) {
		t := token{kind: k, start: start, end: end, paren: depth}
		if k == tokWord {
			t.upper = strings.ToUpper(s[start:end])
		}
		toks = append(toks, t)
		done = stop != nil && stop(t)
	}
	for i := 0; i < len(s) && !done; {
		c := s[i]
		switch {
		case isSpace(c):
			i++
		case c == '-' && strings.HasPrefix(s[i:], "--") && (!d.dashNeedsSpace || i+2 >= len(s) || isSpace(s[i+2])):
			i = lineEnd(s, i)
		case c == '#' && d.hashComment:
			i = lineEnd(s, i)
		case c == '/' && strings.HasPrefix(s[i:], "/*"):
			if body, ok := d.execBody(s, i); ok {
				emit(tokOther, i, body) // keeps the wrapper inside the statement's span
				i = body
				continue
			}
			i = d.commentEnd(s, i)
		case c == '*' && d.execComments && strings.HasPrefix(s[i:], "*/"):
			emit(tokOther, i, i+2)
			i += 2
		case c == '\'':
			end := quoted(s, i, '\'', d.backslashString)
			emit(tokString, i, end)
			i = end
		case c == '"':
			end := quoted(s, i, '"', d.backslashString)
			if d.backslashString {
				emit(tokString, i, end) // MySQL treats "x" as a string by default
			} else {
				emit(tokIdent, i, end)
			}
			i = end
		case c == '`' && d.backtickIdent:
			end := quoted(s, i, '`', false)
			emit(tokIdent, i, end)
			i = end
		case c == '$' && d.dollarQuote:
			if end, ok := dollarEnd(s, i); ok {
				emit(tokString, i, end)
				i = end
			} else {
				emit(tokOther, i, i+1)
				i++
			}
		case isWordStart(c):
			end := i + 1
			for end < len(s) && isWordPart(s[end]) {
				end++
			}
			// Postgres E'...' strings honour backslashes.
			if d.dollarQuote && end == i+1 && (c == 'e' || c == 'E') && end < len(s) && s[end] == '\'' {
				end = quoted(s, end, '\'', true)
				emit(tokString, i, end)
			} else {
				emit(tokWord, i, end)
			}
			i = end
		case c == ';':
			emit(tokSemi, i, i+1)
			i++
		case c == '(':
			emit(tokOther, i, i+1)
			toks[len(toks)-1].open = true
			depth++
			i++
		case c == ')':
			if depth > 0 {
				depth--
			}
			emit(tokOther, i, i+1)
			toks[len(toks)-1].close = true
			i++
		case c == ',':
			emit(tokOther, i, i+1)
			toks[len(toks)-1].comma = true
			i++
		default:
			emit(tokOther, i, i+1)
			i++
		}
	}
	return toks
}

// execBody returns where the code inside /*! ... */ or /*M! ... */ begins.
func (d Dialect) execBody(s string, i int) (int, bool) {
	if !d.execComments {
		return 0, false
	}
	j := i + 2
	if strings.HasPrefix(s[j:], "M!") {
		j += 2
	} else if strings.HasPrefix(s[j:], "!") {
		j++
	} else {
		return 0, false
	}
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	return j, true
}

func (d Dialect) commentEnd(s string, i int) int {
	depth := 1
	for j := i + 2; j < len(s); {
		switch {
		case strings.HasPrefix(s[j:], "*/"):
			depth--
			j += 2
			if depth == 0 {
				return j
			}
		case d.nestedComments && strings.HasPrefix(s[j:], "/*"):
			depth++
			j += 2
		default:
			j++
		}
	}
	return len(s)
}

func lineEnd(s string, i int) int {
	if n := strings.IndexByte(s[i:], '\n'); n >= 0 {
		return i + n
	}
	return len(s)
}

// quoted returns the index after the closing quote, or len(s) when unterminated.
func quoted(s string, i int, q byte, backslash bool) int {
	for j := i + 1; j < len(s); j++ {
		switch {
		case backslash && s[j] == '\\':
			j++
		case s[j] == q:
			if j+1 < len(s) && s[j+1] == q {
				j++
				continue
			}
			return j + 1
		}
	}
	return len(s)
}

// dollarEnd handles $$...$$ and $tag$...$tag$. Parameters like $1 are not quotes.
func dollarEnd(s string, i int) (int, bool) {
	j := i + 1
	if j < len(s) && s[j] >= '0' && s[j] <= '9' {
		return 0, false
	}
	for j < len(s) && s[j] != '$' && isWordPart(s[j]) {
		j++
	}
	if j >= len(s) || s[j] != '$' {
		return 0, false
	}
	tag := s[i : j+1]
	if n := strings.Index(s[j+1:], tag); n >= 0 {
		return j + 1 + n + len(tag), true
	}
	return len(s), true
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' }

func isWordStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

func isWordPart(c byte) bool { return isWordStart(c) || (c >= '0' && c <= '9') || c == '$' }

// Split returns one span per statement, trimmed of comments and whitespace at the ends. A
// comment just before a statement's terminator stays in its span, so the text sent to the
// server matches what the user wrote.
//
// In MySQL, a DELIMITER line changes the terminator as the mysql client does, which lets a
// script hold procedure bodies with semicolons inside. The line itself is never sent. Like
// the client, it counts only at the start of a line with no statement pending, takes the
// first word as the new terminator, ignores the rest of the line, and is dropped without
// effect when that word is missing or holds a backslash.
func (d Dialect) Split(text string) []tile.Span {
	var spans []tile.Span
	delim := ";"
	start, last := -1, 0 // the pending statement, and the end of its last token
	finish := func(stop int) {
		if start < 0 {
			return
		}
		end := last
		if stop >= 0 {
			end = start + len(strings.TrimRight(text[start:stop], " \t\r\n\f"))
		}
		spans = append(spans, tile.Span{Start: start, End: end})
		start = -1
	}
	// A word that may be a DELIMITER command stops the lexer, so text after a changed
	// delimiter is lexed once, not again for every DELIMITER line in a long dump.
	base := 0
	command := func(t token) bool {
		ts, te := base+t.start, base+t.end
		return d.delimiterCmd && t.kind == tokWord && t.upper == "DELIMITER" &&
			atLineStart(text, ts) && (te == len(text) || isSpace(text[te]))
	}
	for base <= len(text) {
		toks := d.lexUntil(text[base:], command)
		next := -1 // where to lex again
		if n := len(toks); n > 0 && command(toks[n-1]) {
			next = base + toks[n-1].end
		}
		for i := 0; i < len(toks); {
			t := toks[i]
			ts, te := base+t.start, base+t.end
			if t.kind == tokString || t.kind == tokIdent {
				if start < 0 {
					start = ts
				}
				last = te
				i++
				continue
			}
			if start < 0 && command(t) {
				eol := lineEnd(text, te)
				if arg, ok := delimiterArg(text[te:eol]); ok {
					delim = arg
				}
				next = eol
				break
			}
			// The terminator is matched in text outside strings, identifiers, and comments, and
			// may span tokens or sit inside one, as in END$$.
			j := i + 1
			for j < len(toks) && isCode(toks[j]) && toks[j].start == toks[j-1].end {
				j++
			}
			runEnd := base + toks[j-1].end
			for p := ts; p < runEnd; {
				k := strings.Index(text[p:runEnd], delim)
				if k < 0 {
					if start < 0 {
						start = p
					}
					last = runEnd
					break
				}
				if k > 0 {
					if start < 0 {
						start = p
					}
					last = p + k
				}
				finish(p + k)
				p += k + len(delim)
			}
			i = j
		}
		if next < 0 {
			break
		}
		base = next
	}
	finish(-1)
	return spans
}

func isCode(t token) bool { return t.kind != tokString && t.kind != tokIdent }

// atLineStart reports whether only spaces and tabs come before i on its line.
func atLineStart(s string, i int) bool {
	for i > 0 && (s[i-1] == ' ' || s[i-1] == '\t') {
		i--
	}
	return i == 0 || s[i-1] == '\n'
}

// delimiterArg reads DELIMITER's argument as the mysql client does: the first word, or a
// quoted string. It refuses an empty one or one with a backslash, as the client does.
func delimiterArg(rest string) (string, bool) {
	rest = strings.TrimLeft(rest, " \t")
	var arg string
	if rest != "" && (rest[0] == '\'' || rest[0] == '"' || rest[0] == '`') {
		arg = rest[1:]
		if n := strings.IndexByte(arg, rest[0]); n >= 0 {
			arg = arg[:n]
		} else {
			arg = strings.TrimRight(arg, " \t\r")
		}
	} else {
		n := strings.IndexFunc(rest, func(r rune) bool { return r < 0x80 && isSpace(byte(r)) })
		if n < 0 {
			n = len(rest)
		}
		arg = rest[:n]
	}
	if arg == "" || strings.Contains(arg, "\\") {
		return "", false
	}
	return arg, true
}

var readVerbs = []string{"SELECT", "SHOW", "DESCRIBE", "DESC", "EXPLAIN", "VALUES", "TABLE", "WITH"}

// Classify returns the worst classification across all statements in the text.
// Anything not recognised as a read counts as a write.
//
// It reads the text exactly as the server would, so a DELIMITER line is an unknown statement.
// In MySQL it also reads the text as Split would, with DELIMITER lines applied, and returns
// the worse of the two, so neither reading can hide a statement from the other.
func (d Dialect) Classify(stmt string) tile.Classification {
	worst := d.classifyPlain(stmt)
	if !d.delimiterCmd {
		return worst
	}
	for _, s := range d.Split(stmt) {
		c := d.classifyPlain(stmt[s.Start:s.End])
		schema := worst.Schema || c.Schema
		if c.Risk > worst.Risk || (c.Risk == worst.Risk && worst.ReadOnly && !c.ReadOnly) {
			worst = c
		}
		worst.Schema = schema
	}
	return worst
}

func (d Dialect) classifyPlain(stmt string) tile.Classification {
	toks := d.lex(stmt)
	worst := tile.Classification{ReadOnly: true}
	first := true
	for len(toks) > 0 {
		n := slices.IndexFunc(toks, func(t token) bool { return t.kind == tokSemi })
		cur := toks
		if n >= 0 {
			cur, toks = toks[:n], toks[n+1:]
		} else {
			toks = nil
		}
		if len(cur) == 0 {
			continue
		}
		c := classifyOne(cur)
		schema := worst.Schema || c.Schema
		if first || c.Risk > worst.Risk {
			worst = c
		}
		worst.Schema = schema
		first = false
	}
	return worst
}

// schemaVerbs define objects rather than data. TRUNCATE is among them because, like DDL, it
// commits implicitly where DDL is not transactional.
var schemaVerbs = []string{"CREATE", "ALTER", "DROP", "RENAME", "TRUNCATE"}

func classifyOne(toks []token) tile.Classification {
	verbAt := slices.IndexFunc(toks, func(t token) bool { return t.kind == tokWord })
	if verbAt < 0 {
		return tile.Classification{Verb: "", Risk: tile.RiskWrite, Reason: "unrecognised statement"}
	}
	verb := toks[verbAt].upper
	c := tile.Classification{Verb: verb, Schema: slices.Contains(schemaVerbs, verb)}
	if isTxControl(toks) {
		c.Tx = txEffect(toks)
	}

	switch verb {
	case "DROP", "TRUNCATE":
		c.Risk, c.Reason = tile.RiskDestructive, verb+" removes data"
		return c
	case "ALTER":
		if hasWord(toks, "DROP") {
			c.Risk, c.Reason = tile.RiskDestructive, "ALTER drops an object"
			return c
		}
	}
	if !slices.Contains(readVerbs, verb) && !isTxControl(toks) {
		c.Risk, c.Reason = tile.RiskWrite, verb+" is not known to be read-only"
	}

	// Writes can sit inside CTEs and EXPLAIN ANALYZE, so scan every word.
	for i, t := range toks {
		if t.kind != tokWord {
			continue
		}
		switch t.upper {
		case "DELETE", "UPDATE":
			if t.upper == "UPDATE" && i > 0 && toks[i-1].upper == "FOR" {
				raise(&c, tile.RiskWrite, "FOR UPDATE takes locks")
				continue
			}
			// Under EXPLAIN without ANALYZE the statement is only planned.
			if (verb == "EXPLAIN") && !hasWord(toks, "ANALYZE") {
				continue
			}
			if !hasWhereAtDepth(toks[i+1:], t.paren) {
				raise(&c, tile.RiskDestructive, t.upper+" without WHERE")
			} else {
				raise(&c, tile.RiskWrite, t.upper+" changes data")
			}
		case "INSERT", "MERGE", "REPLACE":
			if verb == "SELECT" && t.upper == "REPLACE" {
				continue // the string function
			}
			if verb == "EXPLAIN" && !hasWord(toks, "ANALYZE") {
				continue
			}
			raise(&c, tile.RiskWrite, t.upper+" changes data")
		case "INTO":
			if verb == "SELECT" || verb == "WITH" {
				raise(&c, tile.RiskWrite, "SELECT INTO writes")
			}
		case "DROP", "TRUNCATE":
			if verb == "EXPLAIN" && hasWord(toks, "ANALYZE") {
				raise(&c, tile.RiskDestructive, t.upper+" removes data")
			}
		}
	}
	c.ReadOnly = c.Risk == tile.RiskNone
	return c
}

// isTxControl recognises only the plain forms, by allowing no other word. MariaDB's
// BEGIN NOT ATOMIC and MySQL's START REPLICA begin with the same verbs and do real work.
func isTxControl(toks []token) bool {
	if len(toks) == 0 || toks[0].kind != tokWord {
		return false
	}
	switch toks[0].upper {
	case "BEGIN", "COMMIT", "END", "ROLLBACK":
	case "START":
		if len(toks) < 2 || toks[1].upper != "TRANSACTION" {
			return false
		}
	default:
		return false
	}
	return !slices.ContainsFunc(toks[1:], func(t token) bool {
		return t.kind != tokWord || !slices.Contains(txWords, t.upper)
	})
}

var txWords = []string{"TRANSACTION", "WORK", "AND", "NO", "CHAIN"}

// txEffect reads a plain transaction-control statement. AND CHAIN ends one transaction and
// opens the next.
func txEffect(toks []token) tile.TxEffect {
	switch toks[0].upper {
	case "BEGIN", "START":
		return tile.TxBegin
	}
	if hasWord(toks, "CHAIN") && !hasWord(toks, "NO") {
		return tile.TxBegin
	}
	return tile.TxEnd
}

func raise(c *tile.Classification, r tile.Risk, reason string) {
	if r > c.Risk || c.Reason == "" {
		c.Risk, c.Reason = max(r, c.Risk), reason
	}
}

func hasWord(toks []token, w string) bool {
	return slices.ContainsFunc(toks, func(t token) bool { return t.kind == tokWord && t.upper == w })
}

// hasWhereAtDepth looks for WHERE in the same query block, stopping when the block closes.
func hasWhereAtDepth(toks []token, depth int) bool {
	for _, t := range toks {
		if t.paren < depth {
			return false
		}
		if t.kind == tokWord && t.paren == depth && t.upper == "WHERE" {
			return true
		}
	}
	return false
}

const redacted = "'***'"

// Redact hides string literals that follow password keywords, for history and audit.
func (d Dialect) Redact(stmt string) string {
	toks := d.lex(stmt)
	var b strings.Builder
	last := 0
	identified := false
	for i, t := range toks {
		if t.kind == tokSemi {
			identified = false
		}
		if t.kind == tokWord && t.upper == "IDENTIFIED" {
			identified = true
		}
		if t.kind != tokString || !afterSecretKeyword(toks[:i], identified) {
			continue
		}
		b.WriteString(stmt[last:t.start])
		b.WriteString(redacted)
		last = t.end
	}
	b.WriteString(stmt[last:])
	return b.String()
}

// afterSecretKeyword reports whether the tokens before a string put it in a secret position:
// PASSWORD 'x', PASSWORD = 'x', PASSWORD('x'), or BY 'x' after IDENTIFIED.
func afterSecretKeyword(before []token, identified bool) bool {
	i := len(before) - 1
	if i >= 0 && before[i].kind == tokOther {
		i--
	}
	if i < 0 || before[i].kind != tokWord {
		return false
	}
	switch before[i].upper {
	case "PASSWORD":
		return true
	case "BY", "AS":
		return identified
	}
	return false
}
