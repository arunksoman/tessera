//go:build integration

package mysql

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/arunksoman/tessera/tile"
	"github.com/arunksoman/tessera/tile/tiletest"
)

func TestConformance(t *testing.T) {
	servers := map[string]string{"mysql84": "53384", "mariadb114": "53114"}
	if os.Getenv("TESSERA_TEST_MATRIX") != "" {
		servers["mysql80"] = "53380"
		servers["mariadb1011"] = "53111"
	}
	for name, port := range servers {
		t.Run(name, func(t *testing.T) { tiletest.Run(t, harness(port)) })
	}
}

// MySQL generates a self-signed certificate at first boot, which makes the modes observable.
func TestTLSModesAgainstSelfSignedServer(t *testing.T) {
	env := tile.Env{Secret: func(context.Context, string) ([]byte, error) { return []byte("tessera_dev"), nil }}
	for mode, wantOK := range map[string]bool{
		"DISABLED": true, "PREFERRED": true, "REQUIRED": true, "VERIFY_CA": false, "VERIFY_IDENTITY": false,
	} {
		t.Run(mode, func(t *testing.T) {
			c, err := Tile{}.Open(context.Background(), tile.ConnConfig{Values: map[string]string{
				"host": "127.0.0.1", "port": "53384", "user": "tessera", "database": "tessera_test", "tlsmode": mode,
			}}, env)
			if (err == nil) != wantOK {
				t.Fatalf("err = %v, want ok = %v", err, wantOK)
			}
			if err != nil {
				return
			}
			defer c.Close()
			s, err := c.Session(context.Background(), tile.SessionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			st, err := s.Execute(context.Background(), tile.Request{Text: "SHOW SESSION STATUS LIKE 'Ssl_cipher'"})
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			var cipher string
			for {
				f, err := st.Next(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if f.Batch != nil {
					cipher = string(f.Batch.Cells[1].Raw)
				}
				if f.Done != nil {
					break
				}
			}
			if (cipher != "") != (mode != "DISABLED") {
				t.Fatalf("mode %s gave cipher %q", mode, cipher)
			}
		})
	}
}

func harness(port string) tiletest.Harness {
	return tiletest.Harness{
		Open: func(t *testing.T) tile.Conn {
			t.Helper()
			env := tile.Env{Secret: func(context.Context, string) ([]byte, error) { return []byte("tessera_dev"), nil }}
			c, err := Tile{}.Open(context.Background(), tile.ConnConfig{Values: map[string]string{
				"host": "127.0.0.1", "port": port, "user": "tessera", "database": "tessera_test", "tlsmode": "DISABLED",
			}}, env)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { c.Close() })
			return c
		},
		Schema:  tile.ObjectRef{}.Child("schema", "tessera_test"),
		ArgMark: "?",
		ManyRowsSQL: func(n int) string {
			return fmt.Sprintf(`WITH RECURSIVE d(n) AS (SELECT 0 UNION ALL SELECT n+1 FROM d WHERE n<9),
				g AS (SELECT a.n + b.n*10 + c.n*100 + e.n*1000 + f.n*10000 + h.n*100000 AS n FROM d a, d b, d c, d e, d f, d h)
				SELECT n, MD5(n) FROM g LIMIT %d`, n)
		},
		SleepSQL: `SELECT SLEEP(30)`,
		RunningSQL: func(marker string) string {
			return fmt.Sprintf(`SELECT COUNT(*) FROM information_schema.PROCESSLIST WHERE INFO LIKE '%s%%' AND ID <> CONNECTION_ID()`, marker)
		},
		HostileTable: func(t *testing.T, s tile.Session) string {
			name := "we\"ird`; DROP TABLE orders;--'x"
			quoted := "`we\"ird``; DROP TABLE orders;--'x`"
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
