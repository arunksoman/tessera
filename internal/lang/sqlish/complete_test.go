package sqlish

import (
	"slices"
	"strings"
	"testing"

	"github.com/arunksoman/tessera/tile"
)

type fakeCatalog map[string][]string

func (c fakeCatalog) Tables() []tile.ObjectName {
	var out []tile.ObjectName
	for name := range c {
		out = append(out, tile.ObjectName{Schema: "app", Name: name})
	}
	slices.SortFunc(out, func(a, b tile.ObjectName) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func (c fakeCatalog) Columns(n tile.ObjectName) []string { return c[n.Name] }

var catalog = fakeCatalog{
	"orders":    {"id", "customer_id", "total"},
	"customers": {"id", "email", "name"},
}

// complete takes text with | marking the cursor.
func complete(d Dialect, text string) (string, []string) {
	cursor := strings.IndexByte(text, '|')
	text = text[:cursor] + text[cursor+1:]
	prefix, got := d.Language().(tile.Completer).Complete(text, cursor, catalog)
	names := make([]string, len(got))
	for i, c := range got {
		names[i] = c.Text
	}
	return prefix, names
}

func TestComplete(t *testing.T) {
	cases := []struct {
		name   string
		d      Dialect
		in     string
		prefix string
		first  []string // candidates that must lead, in order
		absent []string
	}{
		{"start offers verbs", Postgres, "SEL|", "SEL", []string{"SELECT"}, nil},
		{"tables after FROM", Postgres, "SELECT * FROM o|", "o", []string{"orders"}, []string{"id"}},
		{"tables after JOIN", Postgres, "SELECT * FROM orders JOIN c|", "c", []string{"customers"}, nil},
		{"tables after a comma", Postgres, "SELECT * FROM orders, c|", "c", []string{"customers"}, nil},
		{"columns come first in WHERE", Postgres, "SELECT * FROM orders WHERE |", "", []string{"id", "customer_id", "total"}, nil},
		{"columns in select list with FROM after", Postgres, "SELECT t| FROM orders", "t", []string{"total"}, nil},
		{"alias qualifier", Postgres, "SELECT a.| FROM orders a", "", []string{"id", "customer_id", "total"}, []string{"email"}},
		{"alias qualifier filters", Postgres, "SELECT c.e| FROM orders o JOIN customers c ON o.customer_id = c.id", "e", []string{"email"}, nil},
		{"table name qualifier", Postgres, "SELECT orders.| FROM orders", "", []string{"id"}, nil},
		{"schema qualifier lists tables", Postgres, "SELECT * FROM app.o|", "o", []string{"orders"}, nil},
		{"schema qualified table has columns", Postgres, "SELECT x.| FROM app.orders x", "", []string{"id"}, nil},
		{"AS alias", MySQL, "SELECT x.| FROM `orders` AS x", "", []string{"id"}, nil},
		{"cte name", Postgres, "WITH recent AS (SELECT id FROM orders) SELECT * FROM re|", "re", []string{"recent"}, nil},
		{"subquery alias columns", Postgres, "SELECT s.| FROM (SELECT id, total AS amount FROM orders) s", "", []string{"id", "amount"}, nil},
		{"inner block sees its own tables", Postgres, "SELECT * FROM customers WHERE id IN (SELECT t| FROM orders)", "t", []string{"total"}, nil},
		{"inner block sees enclosing tables", Postgres, "SELECT * FROM customers c WHERE id IN (SELECT 1 FROM orders WHERE e|)", "e", []string{"email"}, nil},
		{"outer block does not see inner tables", Postgres, "SELECT t| FROM customers WHERE id IN (SELECT id FROM orders)", "t", nil, []string{"total"}},
		{"only the cursor's statement counts", Postgres, "SELECT * FROM orders; SELECT * FROM customers WHERE e|", "e", []string{"email"}, nil},
		{"update set", Postgres, "UPDATE customers SET e|", "e", []string{"email"}, nil},
		{"keyword after a table", Postgres, "SELECT * FROM orders WH|", "WH", []string{"WHERE"}, nil},
		{"matching ignores case", Postgres, "select * from O|", "O", []string{"orders"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			prefix, got := complete(c.d, c.in)
			if prefix != c.prefix {
				t.Fatalf("prefix = %q, want %q", prefix, c.prefix)
			}
			if len(got) < len(c.first) || !slices.Equal(got[:len(c.first)], c.first) {
				t.Fatalf("got %v, want it to start with %v", got, c.first)
			}
			for _, a := range c.absent {
				if slices.Contains(got, a) {
					t.Fatalf("%q should not be offered: %v", a, got)
				}
			}
		})
	}
}

func TestCompleteIsQuietInStringsAndComments(t *testing.T) {
	for _, in := range []string{
		"SELECT 'o|",
		"SELECT 'it''s o|",
		"SELECT * FROM t -- o|",
		"SELECT /* o|",
		`SELECT "o|`,
	} {
		if _, got := complete(Postgres, in); len(got) != 0 {
			t.Errorf("%q offered %v", in, got)
		}
	}
	if _, got := complete(Postgres, "SELECT 'x' FROM o|"); !slices.Contains(got, "orders") {
		t.Errorf("after a closed string: %v", got)
	}
	if _, got := complete(Postgres, "SELECT /* x */ * FROM o|"); !slices.Contains(got, "orders") {
		t.Errorf("after a closed comment: %v", got)
	}
	if _, got := complete(Postgres, "-- x\nSEL|"); !slices.Contains(got, "SELECT") {
		t.Errorf("after a line comment: %v", got)
	}
}

// Tolerance is what makes completion possible, so nothing here may panic.
func FuzzLocate(f *testing.F) {
	for _, s := range []string{"", "SELECT a. FROM", "WITH x AS (", "((((", "))))", "'", "\"", "$$", "/*", "--", "a.b.c.", ",,,", "FROM (", "FROM (SELECT"} {
		f.Add(s, 3)
	}
	f.Fuzz(func(t *testing.T, s string, cursor int) {
		for _, d := range []Dialect{Postgres, MySQL} {
			d.Language().(tile.Completer).Complete(s, cursor, catalog)
		}
	})
}
