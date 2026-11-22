//go:build integration

package postgres

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/arunksoman/tessera/tile"
	"github.com/arunksoman/tessera/tile/tiletest"
)

func TestMain(m *testing.M) {
	tiletest.ServeIfChild(Tile{})
	os.Exit(m.Run())
}

// TestConformanceOutOfProcess runs the same suite with the tile behind the plugin protocol.
func TestConformanceOutOfProcess(t *testing.T) {
	tl := tiletest.OutOfProcess(t)
	for name, port := range map[string]string{"pg18": "55418"} {
		t.Run(name, func(t *testing.T) { tiletest.Run(t, harnessFor(tl, port)) })
	}
}

func TestConformance(t *testing.T) {
	servers := map[string]string{"pg18": "55418"}
	if os.Getenv("TESSERA_TEST_MATRIX") != "" {
		servers["pg14"] = "55414"
	}
	for name, port := range servers {
		t.Run(name, func(t *testing.T) { tiletest.Run(t, harness(port)) })
	}
}

func harness(port string) tiletest.Harness { return harnessFor(Tile{}, port) }

func harnessFor(tl tile.Tile, port string) tiletest.Harness {
	return tiletest.Harness{
		Open: func(t *testing.T) tile.Conn {
			t.Helper()
			env := tile.Env{Secret: func(context.Context, string) ([]byte, error) { return []byte("tessera_dev"), nil }}
			c, err := tl.Open(context.Background(), tile.ConnConfig{Values: map[string]string{
				"host": "127.0.0.1", "port": port, "user": "tessera", "database": "tessera_test", "sslmode": "disable",
			}}, env)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { c.Close() })
			return c
		},
		Schema:  tile.ObjectRef{}.Child("schema", "public"),
		ArgMark: "$1",
		ManyRowsSQL: func(n int) string {
			return fmt.Sprintf(`SELECT g, md5(g::text) FROM generate_series(1, %d) g`, n)
		},
		SleepSQL: `SELECT pg_sleep(30)`,
		RunningSQL: func(marker string) string {
			return fmt.Sprintf(`SELECT count(*) FROM pg_stat_activity WHERE state = 'active' AND query LIKE '%s%%' AND pid <> pg_backend_pid()`, marker)
		},
		Design: &tiletest.Design{
			IntType:       "integer",
			TriggerAction: "EXECUTE FUNCTION tt_upper_name()",
			Create: func(t *testing.T, s tile.Session) {
				drop := func() {
					exec(t, s, `DROP TABLE IF EXISTS tt_design, tt_design_renamed, tt_design_ref`)
					exec(t, s, `DROP FUNCTION IF EXISTS tt_upper_name()`)
				}
				drop()
				exec(t, s, `CREATE FUNCTION tt_upper_name() RETURNS trigger LANGUAGE plpgsql AS 'BEGIN NEW.name := upper(NEW.name); RETURN NEW; END'`)
				exec(t, s, `CREATE TABLE tt_design_ref (id integer PRIMARY KEY)`)
				exec(t, s, `CREATE TABLE tt_design (id integer PRIMARY KEY, name varchar(50))`)
				t.Cleanup(drop)
			},
		},
		CreateRoutine: func(t *testing.T, s tile.Session) {
			exec(t, s, `CREATE OR REPLACE FUNCTION tt_add(a int, b int) RETURNS int LANGUAGE sql AS 'SELECT a + b'`)
			t.Cleanup(func() { exec(t, s, `DROP FUNCTION IF EXISTS tt_add(int, int)`) })
		},
		HostileTable: func(t *testing.T, s tile.Session) string {
			name := "we\"ird`; DROP TABLE orders;--'x"
			quoted := `"we""ird` + "`" + `; DROP TABLE orders;--'x"`
			exec(t, s, `CREATE TABLE `+quoted+` (id int)`)
			t.Cleanup(func() { exec(t, s, `DROP TABLE IF EXISTS `+quoted) })
			return name
		},
	}
}

func exec(t *testing.T, s tile.Session, text string) {
	t.Helper()
	ctx := context.Background()
	st, err := s.Execute(ctx, tile.Request{Text: text})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for {
		f, err := st.Next(ctx)
		if err != nil {
			t.Fatalf("%s: %v", text, err)
		}
		if f.Done != nil {
			return
		}
	}
}
