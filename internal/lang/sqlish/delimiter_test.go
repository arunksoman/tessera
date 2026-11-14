package sqlish

import (
	"strings"
	"testing"

	"github.com/arunksoman/tessera/tile"
)

// DELIMITER is a mysql client command, not SQL. It follows the client: only at the start of
// a line with no statement pending, its argument is the first word, and the line is never sent.
func TestSplitDelimiter(t *testing.T) {
	proc := "CREATE PROCEDURE p()\nBEGIN\n  SELECT 1;\n  SELECT 2;\nEND"
	cases := []struct {
		name string
		d    Dialect
		in   string
		want []string
	}{
		{"procedure body stays whole", MySQL,
			"DELIMITER $$\n" + proc + "$$\nDELIMITER ;\nSELECT 3;",
			[]string{proc, "SELECT 3"}},
		{"delimiter after a word", MySQL, "DELIMITER $$\nSELECT 1$$SELECT 2$$", []string{"SELECT 1", "SELECT 2"}},
		{"slashes", MySQL, "delimiter //\nSELECT 1; SELECT 2 //\nSELECT 3 //", []string{"SELECT 1; SELECT 2", "SELECT 3"}},
		{"word delimiter", MySQL, "DELIMITER go\nSELECT 1 go\nSELECT 2", []string{"SELECT 1", "SELECT 2"}},
		{"semicolon is plain text after change", MySQL, "DELIMITER |\nSELECT ';'; SELECT 2|", []string{"SELECT ';'; SELECT 2"}},
		{"delimiter in string", MySQL, "DELIMITER $$\nSELECT '$$' $$ SELECT 2", []string{"SELECT '$$'", "SELECT 2"}},
		{"delimiter in backticks", MySQL, "DELIMITER $$\nSELECT `a$$b` $$", []string{"SELECT `a$$b`"}},
		{"delimiter in comments", MySQL, "DELIMITER $$\nSELECT 1 /* $$ */ -- $$\n# $$\n$$", []string{"SELECT 1 /* $$ */ -- $$\n# $$"}},
		{"leading whitespace", MySQL, "  \tDELIMITER $$\nSELECT 1; SELECT 2$$", []string{"SELECT 1; SELECT 2"}},
		{"rest of the line is ignored", MySQL, "DELIMITER $$ DROP TABLE t\nSELECT 1$$", []string{"SELECT 1"}},
		{"quoted argument", MySQL, "DELIMITER '//'\nSELECT 1//", []string{"SELECT 1"}},
		{"unclosed quote stays on its line", MySQL, "DELIMITER '\nSELECT 1'", []string{"SELECT 1'"}},
		{"crlf", MySQL, "DELIMITER $$\r\nSELECT 1;\r\nSELECT 2$$\r\nDELIMITER ;\r\nSELECT 3", []string{"SELECT 1;\r\nSELECT 2", "SELECT 3"}},
		{"trailing statement without delimiter", MySQL, "DELIMITER $$\nSELECT 1; SELECT 2", []string{"SELECT 1; SELECT 2"}},
		{"only a delimiter line", MySQL, "DELIMITER $$\n", nil},
		{"empty statements", MySQL, "DELIMITER $$\n$$ $$\nSELECT 1$$", []string{"SELECT 1"}},

		// The client rejects these, and so does the splitter: the delimiter is unchanged and
		// the line is dropped, so nothing runs that the client would not have run.
		{"no argument", MySQL, "DELIMITER\nSELECT 1; SELECT 2", []string{"SELECT 1", "SELECT 2"}},
		{"backslash argument", MySQL, "DELIMITER \\\nSELECT 1; SELECT 2", []string{"SELECT 1", "SELECT 2"}},

		// Not a command: these go to the server as SQL.
		{"pending statement", MySQL, "SELECT 1,\ndelimiter\nFROM t;", []string{"SELECT 1,\ndelimiter\nFROM t"}},
		{"after a statement on the same line", MySQL, "SELECT 1; DELIMITER $$\nSELECT 2;", []string{"SELECT 1", "DELIMITER $$\nSELECT 2"}},
		{"after a comment on the line", MySQL, "/* x */ DELIMITER $$\nSELECT 1;", []string{"DELIMITER $$\nSELECT 1"}},
		{"longer word", MySQL, "DELIMITERS $$\nSELECT 1;", []string{"DELIMITERS $$\nSELECT 1"}},
		{"inside a string", MySQL, "SELECT '\nDELIMITER $$\n'; SELECT 2", []string{"SELECT '\nDELIMITER $$\n'", "SELECT 2"}},
		{"inside a comment", MySQL, "/*\nDELIMITER $$\n*/ SELECT 1; SELECT 2", []string{"SELECT 1", "SELECT 2"}},
		{"postgres has no such command", Postgres, "DELIMITER //\nSELECT 1; SELECT 2", []string{"DELIMITER //\nSELECT 1", "SELECT 2"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			for _, s := range c.d.Split(c.in) {
				got = append(got, c.in[s.Start:s.End])
			}
			if strings.Join(got, "|") != strings.Join(c.want, "|") {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

// A DELIMITER line must not hide what follows it from the classifier.
func TestClassifyDelimiter(t *testing.T) {
	for _, in := range []string{
		"DELIMITER $$\nDROP TABLE t $$",
		"DELIMITER $$\nSELECT 1 $$ DROP TABLE t $$",
		"DELIMITER //\nDELETE FROM t //",
	} {
		if c := MySQL.Classify(in); c.Risk != tile.RiskDestructive || c.ReadOnly {
			t.Errorf("%q: %+v", in, c)
		}
	}
	// The server would see the DELIMITER line too, so a script holding one is never read-only.
	if c := MySQL.Classify("DELIMITER $$\nSELECT 1 $$\nDELIMITER ;\nSELECT 2;"); c.ReadOnly {
		t.Errorf("script with DELIMITER: %+v", c)
	}
	// A span starting with DELIMITER, because it followed a statement on its line, goes to the
	// server whole, so the ignored rest of that line still counts.
	if c := MySQL.Classify("DELIMITER $$ DROP TABLE t\nSELECT 2"); c.ReadOnly {
		t.Errorf("text after DELIMITER: %+v", c)
	}
	for _, in := range []string{"SELECT 1; SELECT 2", "SHOW TABLES"} {
		if c := MySQL.Classify(in); !c.ReadOnly {
			t.Errorf("%q: %+v", in, c)
		}
	}
	if c := MySQL.Classify("DELIMITER $$\nCREATE PROCEDURE p() BEGIN SELECT 1; END $$"); !c.Schema || c.ReadOnly {
		t.Errorf("procedure: %+v", c)
	}
}

// mysqldump wraps every routine and trigger in DELIMITER lines. Splitting must stay linear,
// or a large dump would hang the editor.
func TestSplitDelimiterDump(t *testing.T) {
	var b strings.Builder
	const n = 20000
	for range n {
		b.WriteString("DELIMITER ;;\nCREATE TRIGGER t BEFORE INSERT ON x FOR EACH ROW BEGIN SET @a = 1; END ;;\nDELIMITER ;\nSELECT 1;\n")
	}
	if got := len(MySQL.Split(b.String())); got != 2*n {
		t.Fatalf("spans = %d, want %d", got, 2*n)
	}
}
