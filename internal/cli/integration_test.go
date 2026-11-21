//go:build integration

package cli

import (
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"
)

var servers = []struct{ name, tile, port, tlsMode string }{
	{"postgres", "postgres", "55418", "disable"},
	{"mysql", "mysql", "53384", "DISABLED"},
	{"mariadb", "mysql", "53114", "DISABLED"},
}

func each(t *testing.T, extra []string, f func(t *testing.T)) {
	eachIn(t, "tessera_test", extra, f)
}

// eachIn runs f against every server with a "dev" profile on the given database.
func eachIn(t *testing.T, database string, extra []string, f func(t *testing.T)) {
	for _, s := range servers {
		t.Run(s.name, func(t *testing.T) {
			env(t)
			args := append([]string{"profile", "add", "dev", "--tile", s.tile, "--host", "127.0.0.1", "--port", s.port,
				"--user", "tessera", "--database", database, "--tls", s.tlsMode, "--password-stdin"}, extra...)
			if r := tess("tessera_dev\n", args...); r.code != 0 {
				t.Fatalf("%+v", r)
			}
			f(t)
		})
	}
}

func TestEndToEndConnect(t *testing.T) {
	each(t, nil, func(t *testing.T) {
		if r := tess("", "connect", "dev"); r.code != 0 || !strings.Contains(r.stdout, "connected to dev") {
			t.Fatalf("%+v", r)
		}
	})
}

func TestEndToEndFormats(t *testing.T) {
	each(t, nil, func(t *testing.T) {
		const q = "SELECT email, name FROM customers ORDER BY id"
		r := tess("", "query", "-c", "dev", q)
		if r.code != 0 || !strings.Contains(r.stdout, "ada@example.com") || !strings.Contains(r.stdout, "NULL") || !strings.Contains(r.stdout, "(2 rows)") {
			t.Fatalf("%+v", r)
		}
		r = tess("", "query", "-c", "dev", "-f", "csv", q)
		recs, err := csv.NewReader(strings.NewReader(r.stdout)).ReadAll()
		if err != nil || len(recs) != 3 || recs[1][0] != "ada@example.com" {
			t.Fatalf("%v %+v", err, r)
		}
		r = tess("", "query", "-c", "dev", "-f", "json", "SELECT total FROM orders")
		dec := json.NewDecoder(strings.NewReader(r.stdout))
		dec.UseNumber()
		var out []map[string]any
		if err := dec.Decode(&out); err != nil {
			t.Fatalf("%v %+v", err, r)
		}
		if out[0]["total"].(json.Number).String() != "12345678901234.5678" {
			t.Fatalf("decimal changed: %v", out[0])
		}
	})
}

// The compose databases also load the Sakila sample database, a realistic schema with
// views, enums, and thousands of rows, for trying tess by hand.
func TestEndToEndSakila(t *testing.T) {
	eachIn(t, "sakila", nil, func(t *testing.T) {
		const q = `SELECT
  (SELECT count(*) FROM film) AS films,
  (SELECT count(*) FROM actor) AS actors,
  (SELECT count(*) FROM rental) AS rentals,
  (SELECT count(*) FROM film_actor fa JOIN actor a ON a.actor_id = fa.actor_id
     WHERE a.first_name = 'PENELOPE' AND a.last_name = 'GUINESS') AS penelope,
  (SELECT count(*) FROM film_list) AS listed`
		r := tess("", "query", "-c", "dev", "-f", "csv", q)
		recs, err := csv.NewReader(strings.NewReader(r.stdout)).ReadAll()
		if err != nil || r.code != 0 || len(recs) != 2 {
			t.Fatalf("%v %+v", err, r)
		}
		if got := strings.Join(recs[1][:4], ","); got != "1000,200,16044,19" {
			t.Fatalf("films,actors,rentals,penelope = %s", got)
		}
		if recs[1][4] == "0" {
			t.Fatal("film_list view is empty")
		}
	})
}

func TestEndToEndStdinAndScript(t *testing.T) {
	each(t, nil, func(t *testing.T) {
		r := tess("SELECT 1 AS a; SELECT 2 AS b;", "query", "-c", "dev")
		if r.code != 0 || !strings.Contains(r.stdout, " a") || !strings.Contains(r.stdout, " b") {
			t.Fatalf("%+v", r)
		}
	})
}

// A procedure body holds semicolons, so MySQL scripts change the terminator with DELIMITER,
// which tess applies itself and never sends.
func TestEndToEndDelimiter(t *testing.T) {
	each(t, nil, func(t *testing.T) {
		if strings.HasPrefix(t.Name(), "TestEndToEndDelimiter/postgres") {
			t.Skip("DELIMITER is a mysql client command")
		}
		script := `DROP PROCEDURE IF EXISTS cli_delim;
DELIMITER $$
CREATE PROCEDURE cli_delim()
BEGIN
  SELECT 41 AS a;
  SELECT 42 AS b;
END$$
DELIMITER ;
CALL cli_delim();
DROP PROCEDURE cli_delim;
`
		r := tess(script, "query", "-c", "dev", "--yes")
		if r.code != 0 || !strings.Contains(r.stdout, "41") {
			t.Fatalf("%+v", r)
		}
	})
}

func TestEndToEndSafety(t *testing.T) {
	each(t, nil, func(t *testing.T) {
		exec := func(args ...string) result { return tess("", append([]string{"query", "-c", "dev"}, args...)...) }
		if r := exec("CREATE TABLE cli_safety (id INT)"); r.code != 0 {
			t.Fatalf("%+v", r)
		}
		r := exec("DROP TABLE cli_safety")
		if r.code != exitRefused || !strings.Contains(r.err, "needs confirmation") {
			t.Fatalf("%+v", r)
		}
		if r := exec("SELECT COUNT(*) FROM cli_safety"); r.code != 0 {
			t.Fatalf("table should still exist: %+v", r)
		}
		if r := exec("--yes", "DROP TABLE cli_safety"); r.code != 0 {
			t.Fatalf("%+v", r)
		}
		if r := exec("SELECT COUNT(*) FROM cli_safety"); r.code != exitError {
			t.Fatalf("table should be gone: %+v", r)
		}
		if r := tess("", "audit", "verify"); r.code != 0 || !strings.Contains(r.stdout, "2 entries") {
			t.Fatalf("%+v", r)
		}
	})
}

func TestEndToEndReadOnlyProfile(t *testing.T) {
	each(t, []string{"--read-only"}, func(t *testing.T) {
		if r := tess("", "query", "-c", "dev", "SELECT email FROM customers"); r.code != 0 {
			t.Fatalf("%+v", r)
		}
		r := tess("", "query", "-c", "dev", "--yes", "DELETE FROM customers WHERE id = 1")
		if r.code != exitRefused || !strings.Contains(r.err, "read-only") {
			t.Fatalf("%+v", r)
		}
	})
}

func TestEndToEndTimeoutCancels(t *testing.T) {
	for _, s := range servers {
		t.Run(s.name, func(t *testing.T) {
			env(t)
			if r := tess("tessera_dev\n", "profile", "add", "dev", "--tile", s.tile, "--host", "127.0.0.1", "--port", s.port,
				"--user", "tessera", "--database", "tessera_test", "--tls", s.tlsMode, "--password-stdin"); r.code != 0 {
				t.Fatalf("%+v", r)
			}
			sleep := "SELECT SLEEP(30)"
			if s.tile == "postgres" {
				sleep = "SELECT pg_sleep(30)"
			}
			r := tess("", "query", "-c", "dev", "--timeout", "300ms", sleep)
			if r.code != exitError || !strings.Contains(r.err, "deadline") {
				t.Fatalf("%+v", r)
			}
		})
	}
}

func TestEndToEndWrongPassword(t *testing.T) {
	for _, s := range servers {
		t.Run(s.name, func(t *testing.T) {
			env(t)
			if r := tess("not-the-password\n", "profile", "add", "dev", "--tile", s.tile, "--host", "127.0.0.1", "--port", s.port,
				"--user", "tessera", "--database", "tessera_test", "--tls", s.tlsMode, "--password-stdin"); r.code != 0 {
				t.Fatalf("%+v", r)
			}
			r := tess("", "connect", "dev")
			if r.code != exitError {
				t.Fatalf("%+v", r)
			}
			if strings.Contains(r.err, "not-the-password") {
				t.Fatalf("password in error output: %s", r.err)
			}
		})
	}
}

func TestEndToEndHistoryRedacts(t *testing.T) {
	each(t, nil, func(t *testing.T) {
		// Fails on the server for lack of privileges or syntax, which is fine: it must still be redacted.
		tess("", "query", "-c", "dev", "ALTER USER nobody IDENTIFIED BY 'hunter2'")
		tess("", "query", "-c", "dev", "CREATE ROLE nobody PASSWORD 'hunter2'")
		r := tess("", "history", "-c", "dev")
		if r.code != 0 || strings.Contains(r.stdout, "hunter2") || !strings.Contains(r.stdout, "***") {
			t.Fatalf("%+v", r)
		}
	})
}
