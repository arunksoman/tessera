//go:build integration

package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/arunksoman/tessera/internal/engine"
	"github.com/arunksoman/tessera/internal/secrets"
	"github.com/arunksoman/tessera/internal/store"
	"github.com/arunksoman/tessera/internal/tiles/mysql"
	"github.com/arunksoman/tessera/internal/tiles/postgres"
	"github.com/arunksoman/tessera/tile"
)

var servers = []struct {
	name, tile, port, tlsKey, tlsMode, schema, sleep string
}{
	{"postgres", "postgres", "55418", "sslmode", "disable", "public", "SELECT pg_sleep(30)"},
	{"mysql", "mysql", "53384", "tlsmode", "DISABLED", "tessera_test", "SELECT SLEEP(30)"},
	{"mariadb", "mysql", "53114", "tlsmode", "DISABLED", "tessera_test", "SELECT SLEEP(30)"},
}

// each runs a flow on every server, with a real engine and a real Bubble Tea program.
func each(t *testing.T, f func(t *testing.T, a *app, schema, sleep string, sql func(string) error)) {
	for _, s := range servers {
		t.Run(s.name, func(t *testing.T) {
			dir := t.TempDir()
			st, err := store.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			vault, err := secrets.OpenVault(dir, make([]byte, secrets.KeySize))
			if err != nil {
				t.Fatal(err)
			}
			p := store.Profile{Name: "dev", Tile: s.tile, Values: map[string]string{
				"host": "127.0.0.1", "port": s.port, "user": "tessera", "database": "tessera_test", s.tlsKey: s.tlsMode,
			}}
			if err := st.SaveProfile(p); err != nil {
				t.Fatal(err)
			}
			if err := vault.Set(engine.SecretName("dev", "password"), []byte("tessera_dev")); err != nil {
				t.Fatal(err)
			}
			e := engine.New(st, vault, "tester", postgres.Tile{}, mysql.Tile{})
			// A second connection stands in for another user of the database.
			other, err := e.Open(context.Background(), "dev")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { other.Close() })
			sql := func(text string) error {
				return other.Run(context.Background(), text, engine.RunOptions{Confirm: func(engine.Prompt) bool { return true }},
					func(int, tile.Frame) error { return nil })
			}
			if err := sql(`DROP TABLE IF EXISTS tui_items`); err != nil {
				t.Fatal(err)
			}
			if err := sql(`CREATE TABLE tui_items (id BIGINT PRIMARY KEY, name VARCHAR(50), price DECIMAL(20,4))`); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { sql(`DROP TABLE IF EXISTS tui_items`) })
			var vals []string
			for i := 1; i <= 1200; i++ {
				vals = append(vals, "("+itoa(i)+", 'item"+itoa(i)+"', 12345678901234.5678)")
			}
			if err := sql(`INSERT INTO tui_items (id, name, price) VALUES ` + strings.Join(vals, ",")); err != nil {
				t.Fatal(err)
			}
			a := startWith(t, e, st, setup{start: "dev"})
			a.see("▸ "+s.schema, "Editor")
			f(t, a, s.schema, s.sleep, sql)
		})
	}
}

func itoa(i int) string {
	var b strings.Builder
	if i >= 10 {
		b.WriteString(itoa(i / 10))
	}
	b.WriteByte(byte('0' + i%10))
	return b.String()
}

func TestRunAndLosslessValues(t *testing.T) {
	each(t, func(t *testing.T, a *app, _, _ string, _ func(string) error) {
		a.typeText("SELECT id, price FROM tui_items WHERE id = 7")
		a.keys("f5")
		a.see("row 1 of 1", "12345678901234.5678")
	})
}

func TestCancelStopsTheServer(t *testing.T) {
	each(t, func(t *testing.T, a *app, _, sleep string, _ func(string) error) {
		a.typeText(sleep)
		a.keys("f5")
		a.see("Esc cancels")
		a.keys("esc")
		a.see("cancelled")
		a.gone("Esc cancels")
		// The pinned session survives its cancel.
		a.keys("alt+t")
		a.see("dev 2")
		a.typeText("SELECT 41 + 1 AS answer")
		a.keys("f5")
		a.see("answer", "42")
	})
}

func TestTransactionSpansRuns(t *testing.T) {
	each(t, func(t *testing.T, a *app, _, _ string, _ func(string) error) {
		a.typeText("BEGIN; UPDATE tui_items SET name = 'tx' WHERE id = 1; ROLLBACK")
		a.keys("f6")
		a.gone("in transaction")
		a.keys("alt+t")
		a.see("dev 2")
		a.typeText("SELECT name FROM tui_items WHERE id = 1")
		a.keys("f5")
		a.see("item1")
	})
}

func TestCompletionUsesTheLiveCatalog(t *testing.T) {
	each(t, func(t *testing.T, a *app, _, _ string, _ func(string) error) {
		a.typeText("SELECT pri FROM tui_it")
		a.keys("tab")
		a.see("tui_items  table")
		a.keys("enter")
		a.see("FROM tui_items")
		// Back to the select list: the column comes from the catalog.
		a.keys("alt+t")
		a.see("dev 2")
		a.typeText("SELECT * FROM tui_items WHERE pri")
		a.keys("tab")
		a.see("price  column")
	})
}

func TestBrowseEditApplyAndConflict(t *testing.T) {
	each(t, func(t *testing.T, a *app, schema, _ string, sql func(string) error) {
		openItems(a, schema)
		a.see(schema+".tui_items", "item1", "of 500+")

		a.keys("G")
		a.see("of 1000+")
		a.keys("G")
		a.see("of 1200")

		a.keys("g", "right", "enter")
		a.see("Edit name")
		a.keys("end")
		a.typeText("-edited")
		a.keys("enter")
		a.see("item1-edited*", "1 pending")
		a.keys("ctrl+s")
		a.see("Apply 1 change?", "item1-edited")
		a.keys("y")
		a.see("applied 1 change", "item1-edited")
		a.gone("pending")

		// Another user changes the same record while an edit is pending.
		a.keys("enter")
		a.see("Edit name")
		a.typeText("!")
		a.keys("enter")
		a.see("1 pending")
		if err := sql(`UPDATE tui_items SET name = 'theirs' WHERE id = 1`); err != nil {
			t.Fatal(err)
		}
		a.keys("ctrl+s")
		a.see("Apply 1")
		a.keys("y")
		a.see("conflict", "1 pending")
		a.keys("u")
		a.gone("pending")
	})
}

func TestSortAndFilter(t *testing.T) {
	each(t, func(t *testing.T, a *app, schema, _ string, _ func(string) error) {
		openItems(a, schema)
		a.see("item1")
		a.keys("s")
		a.see("id ▲")
		a.keys("s")
		a.see("id ▼", "1200")
		a.keys("s")
		a.keys("right", "f")
		a.see("Filter name")
		a.typeText("ITEM119")
		a.keys("enter")
		a.see("where name ~ ITEM119", "item1199", "of 11")
	})
}

// openItems opens the table directly, since the tree may list other tables above it.
func openItems(a *app, schema string) {
	ref := tile.ObjectRef{}.Child("schema", schema).Child("table", "tui_items")
	a.p.Send(func(m *Model) tea.Cmd {
		return m.openBrowse(m.activeConn(), tile.Node{Ref: ref, Kind: "table", Label: "tui_items", Browsable: true})
	})
}

func TestTreeShowsTheTable(t *testing.T) {
	each(t, func(t *testing.T, a *app, _, _ string, _ func(string) error) {
		a.keys("alt+1", "enter")
		a.see("tui_items")
		a.keys("r")
		a.see("tui_items") // refreshing keeps the schema open
	})
}
