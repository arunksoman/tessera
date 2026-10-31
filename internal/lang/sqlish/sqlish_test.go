package sqlish

import (
	"strings"
	"testing"

	"github.com/arunksoman/tessera/tile"
)

func TestSplit(t *testing.T) {
	cases := []struct {
		name string
		d    Dialect
		in   string
		want []string
	}{
		{"simple", Postgres, "SELECT 1; SELECT 2", []string{"SELECT 1", "SELECT 2"}},
		{"trailing semicolon", Postgres, "SELECT 1;  \n", []string{"SELECT 1"}},
		{"semicolon in string", Postgres, "SELECT ';'; SELECT 2", []string{"SELECT ';'", "SELECT 2"}},
		{"doubled quote", Postgres, "SELECT 'a'';b'; SELECT 2", []string{"SELECT 'a'';b'", "SELECT 2"}},
		{"line comment", Postgres, "SELECT 1 -- ; no\n; SELECT 2", []string{"SELECT 1 -- ; no", "SELECT 2"}},
		{"block comment", Postgres, "SELECT /* ; */ 1; SELECT 2", []string{"SELECT /* ; */ 1", "SELECT 2"}},
		{"pg nested comment", Postgres, "SELECT /* a /* ; */ ; */ 1; SELECT 2", []string{"SELECT /* a /* ; */ ; */ 1", "SELECT 2"}},
		{"dollar quote", Postgres, "DO $$ BEGIN; END $$; SELECT 2", []string{"DO $$ BEGIN; END $$", "SELECT 2"}},
		{"tagged dollar quote", Postgres, "DO $a$ ; $$ ; $a$; SELECT 2", []string{"DO $a$ ; $$ ; $a$", "SELECT 2"}},
		{"dollar param is not a quote", Postgres, "SELECT $1; SELECT $2", []string{"SELECT $1", "SELECT $2"}},
		{"pg E string backslash", Postgres, `SELECT E'a\';b'; SELECT 2`, []string{`SELECT E'a\';b'`, "SELECT 2"}},
		{"pg plain string has no backslash escape", Postgres, `SELECT 'a\'; SELECT 2`, []string{`SELECT 'a\'`, "SELECT 2"}},
		{"pg quoted identifier", Postgres, `SELECT ";"; SELECT 2`, []string{`SELECT ";"`, "SELECT 2"}},
		{"mysql backslash escape", MySQL, `SELECT 'a\';b'; SELECT 2`, []string{`SELECT 'a\';b'`, "SELECT 2"}},
		{"mysql backtick", MySQL, "SELECT `;`; SELECT 2", []string{"SELECT `;`", "SELECT 2"}},
		{"mysql hash comment", MySQL, "SELECT 1 # ;\n; SELECT 2", []string{"SELECT 1 # ;", "SELECT 2"}},
		{"mysql comments do not nest", MySQL, "SELECT /* /* */ 1; SELECT 2", []string{"SELECT /* /* */ 1", "SELECT 2"}},
		{"mysql double dash needs space", MySQL, "SELECT 1--2; SELECT 2", []string{"SELECT 1--2", "SELECT 2"}},
		{"mysql executable comment stays whole", MySQL, "/*!50000 DROP TABLE t */; SELECT 2", []string{"/*!50000 DROP TABLE t */", "SELECT 2"}},
		{"unterminated string", Postgres, "SELECT 'oops; SELECT 2", []string{"SELECT 'oops; SELECT 2"}},
		{"empty", Postgres, " ; ;\n", nil},
		{"comment only", Postgres, "-- nothing", nil},
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

func TestClassify(t *testing.T) {
	cases := []struct {
		name     string
		d        Dialect
		in       string
		verb     string
		risk     tile.Risk
		readOnly bool
	}{
		{"select", Postgres, "select * from t", "SELECT", tile.RiskNone, true},
		{"show", MySQL, "SHOW TABLES", "SHOW", tile.RiskNone, true},
		{"describe", MySQL, "DESC t", "DESC", tile.RiskNone, true},
		{"explain", Postgres, "EXPLAIN SELECT 1", "EXPLAIN", tile.RiskNone, true},
		{"leading comment", Postgres, "/* x */ -- y\n SELECT 1", "SELECT", tile.RiskNone, true},
		{"leading paren", Postgres, "(SELECT 1)", "SELECT", tile.RiskNone, true},
		{"read-only cte", Postgres, "WITH a AS (SELECT 1) SELECT * FROM a", "WITH", tile.RiskNone, true},

		{"insert", Postgres, "INSERT INTO t VALUES (1)", "INSERT", tile.RiskWrite, false},
		{"update with where", Postgres, "UPDATE t SET a=1 WHERE id=2", "UPDATE", tile.RiskWrite, false},
		{"delete with where", MySQL, "DELETE FROM t WHERE id=2", "DELETE", tile.RiskWrite, false},
		{"unknown verb is not read-only", Postgres, "FROBNICATE t", "FROBNICATE", tile.RiskWrite, false},
		{"do block", Postgres, "DO $$ BEGIN DROP TABLE t; END $$", "DO", tile.RiskWrite, false},
		{"call", MySQL, "CALL p()", "CALL", tile.RiskWrite, false},
		{"select into creates a table", Postgres, "SELECT * INTO n FROM t", "SELECT", tile.RiskWrite, false},
		{"into outfile", MySQL, "SELECT * FROM t INTO OUTFILE '/x'", "SELECT", tile.RiskWrite, false},
		{"for update locks", Postgres, "SELECT * FROM t FOR UPDATE", "SELECT", tile.RiskWrite, false},
		{"grant", MySQL, "GRANT ALL ON *.* TO u", "GRANT", tile.RiskWrite, false},

		{"drop", Postgres, "drop table t", "DROP", tile.RiskDestructive, false},
		{"truncate", MySQL, "TRUNCATE t", "TRUNCATE", tile.RiskDestructive, false},
		{"delete no where", Postgres, "DELETE FROM t", "DELETE", tile.RiskDestructive, false},
		{"update no where", MySQL, "UPDATE t SET a=1", "UPDATE", tile.RiskDestructive, false},
		{"where only in subquery", Postgres, "DELETE FROM t USING (SELECT 1 FROM u WHERE x) s", "DELETE", tile.RiskDestructive, false},
		{"alter drop", Postgres, "ALTER TABLE t DROP COLUMN a", "ALTER", tile.RiskDestructive, false},
		{"alter add", Postgres, "ALTER TABLE t ADD COLUMN a int", "ALTER", tile.RiskWrite, false},

		{"explain analyze runs it", Postgres, "EXPLAIN ANALYZE DELETE FROM t", "EXPLAIN", tile.RiskDestructive, false},
		{"explain paren analyze", Postgres, "EXPLAIN (ANALYZE, BUFFERS) UPDATE t SET a=1 WHERE b=2", "EXPLAIN", tile.RiskWrite, false},
		{"writing cte", Postgres, "WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d", "WITH", tile.RiskDestructive, false},
		{"writing cte with where", Postgres, "WITH d AS (UPDATE t SET a=1 WHERE b=2 RETURNING *) SELECT * FROM d", "WITH", tile.RiskWrite, false},

		{"mysql executable comment", MySQL, "/*!50000 DROP TABLE t */", "DROP", tile.RiskDestructive, false},
		{"mysql executable comment no version", MySQL, "/*! DROP TABLE t */", "DROP", tile.RiskDestructive, false},
		{"mariadb executable comment", MySQL, "/*M!100100 TRUNCATE t */", "TRUNCATE", tile.RiskDestructive, false},
		{"mysql executable comment inside select", MySQL, "SELECT 1 /*! INTO OUTFILE '/x' */", "SELECT", tile.RiskWrite, false},
		{"pg treats that as a comment", Postgres, "SELECT 1 /*! DROP TABLE t */", "SELECT", tile.RiskNone, true},

		{"keyword in string is ignored", Postgres, "SELECT 'DROP TABLE t'", "SELECT", tile.RiskNone, true},
		{"keyword in identifier is ignored", Postgres, `SELECT "delete" FROM t`, "SELECT", tile.RiskNone, true},
		{"second statement counts", Postgres, "SELECT 1; DROP TABLE t", "DROP", tile.RiskDestructive, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.d.Classify(c.in)
			if got.Verb != c.verb || got.Risk != c.risk || got.ReadOnly != c.readOnly {
				t.Fatalf("got %+v, want verb=%s risk=%d readOnly=%v", got, c.verb, c.risk, c.readOnly)
			}
			if got.Risk != tile.RiskNone && got.Reason == "" {
				t.Fatal("a risky classification needs a reason")
			}
		})
	}
}

func TestRedact(t *testing.T) {
	cases := []struct {
		name string
		d    Dialect
		in   string
		want string
	}{
		{"pg role", Postgres, "CREATE ROLE a PASSWORD 'hunter2'", "CREATE ROLE a PASSWORD '***'"},
		{"pg encrypted", Postgres, "ALTER ROLE a ENCRYPTED PASSWORD 'hunter2' LOGIN", "ALTER ROLE a ENCRYPTED PASSWORD '***' LOGIN"},
		{"mysql identified by", MySQL, "CREATE USER a IDENTIFIED BY 'hunter2'", "CREATE USER a IDENTIFIED BY '***'"},
		{"mysql identified with", MySQL, "ALTER USER a IDENTIFIED WITH caching_sha2_password BY 'hunter2'", "ALTER USER a IDENTIFIED WITH caching_sha2_password BY '***'"},
		{"mysql set password", MySQL, "SET PASSWORD = 'hunter2'", "SET PASSWORD = '***'"},
		{"mysql password function", MySQL, "SET PASSWORD FOR a = PASSWORD('hunter2')", "SET PASSWORD FOR a = PASSWORD('***')"},
		{"pg server option", Postgres, "CREATE USER MAPPING FOR a SERVER s OPTIONS (password 'hunter2')", "CREATE USER MAPPING FOR a SERVER s OPTIONS (password '***')"},
		{"quote in password", Postgres, "CREATE ROLE a PASSWORD 'hun''ter2'", "CREATE ROLE a PASSWORD '***'"},
		{"order by string is untouched", Postgres, "SELECT 'x' FROM t ORDER BY 'y'", "SELECT 'x' FROM t ORDER BY 'y'"},
		{"column named password untouched", Postgres, "SELECT password FROM t WHERE a = 'keep'", "SELECT password FROM t WHERE a = 'keep'"},
		{"ordinary statement unchanged", Postgres, "SELECT 'hunter2'", "SELECT 'hunter2'"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.d.Redact(c.in); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func FuzzDialects(f *testing.F) {
	for _, s := range []string{
		"SELECT 1; DROP TABLE t", "/*! DROP */", "$a$ $$ $a$", "E'\\''", "'", "\"", "`", "/*", "--", "#",
		"$", "$$", "$a", "/*M!", "WITH (", ")))", "EXPLAIN (", "PASSWORD", "IDENTIFIED BY '", "\x00\xff",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		for _, d := range []Dialect{Postgres, MySQL} {
			last := 0
			for _, s := range d.Split(in) {
				if s.Start < last || s.End < s.Start || s.End > len(in) {
					t.Fatalf("bad span %+v after %d for %q", s, last, in)
				}
				last = s.End
			}
			c := d.Classify(in)
			if c.ReadOnly && c.Risk != tile.RiskNone {
				t.Fatalf("read-only and risky: %+v for %q", c, in)
			}
			_ = d.Redact(in)
		}
	})
}
