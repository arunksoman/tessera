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

func TestConformance(t *testing.T) {
	servers := map[string]string{"pg18": "55418"}
	if os.Getenv("TESSERA_TEST_MATRIX") != "" {
		servers["pg14"] = "55414"
	}
	for name, port := range servers {
		t.Run(name, func(t *testing.T) { tiletest.Run(t, harness(port)) })
	}
}

func harness(port string) tiletest.Harness {
	return tiletest.Harness{
		Open: func(t *testing.T) tile.Conn {
			t.Helper()
			env := tile.Env{Secret: func(context.Context, string) ([]byte, error) { return []byte("tessera_dev"), nil }}
			c, err := Tile{}.Open(context.Background(), tile.ConnConfig{Values: map[string]string{
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
