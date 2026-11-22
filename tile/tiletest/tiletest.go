// Package tiletest is the conformance suite every tile must pass.
package tiletest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/arunksoman/tessera/tile"
)

// Harness supplies the database-specific parts of the suite.
//
// The database must hold the fixture tables from test/fixtures.
type Harness struct {
	Open func(t *testing.T) tile.Conn
	// Schema is the tree path of the fixture tables.
	Schema tile.ObjectRef
	// ManyRowsSQL returns at least n rows of 2 columns, slowly enough to cancel.
	ManyRowsSQL func(n int) string
	SleepSQL    string
	// ArgMark is the placeholder for the first bound argument.
	ArgMark string
	// RunningSQL counts running statements containing marker, using the given marker.
	RunningSQL func(marker string) string
	// HostileTable creates a table with a name full of quoting characters and returns it.
	HostileTable func(t *testing.T, s tile.Session) string
	// CreateRoutine creates a function tt_add(a, b) returning their integer sum, in Schema.
	// Nil skips the routine tests, for stores without routines.
	CreateRoutine func(t *testing.T, s tile.Session)
}

// Run runs the conformance suite.
func Run(t *testing.T, h Harness) {
	t.Run("Stream", func(t *testing.T) { testStream(t, h) })
	t.Run("Lossless", func(t *testing.T) { testLossless(t, h) })
	t.Run("BindArgs", func(t *testing.T) { testBindArgs(t, h) })
	t.Run("Summary", func(t *testing.T) { testSummary(t, h) })
	t.Run("SyntaxError", func(t *testing.T) { testSyntaxError(t, h) })
	t.Run("CancelStopsServer", func(t *testing.T) { testCancel(t, h) })
	t.Run("EarlyCloseIsFast", func(t *testing.T) { testEarlyClose(t, h) })
	t.Run("ReadOnlySession", func(t *testing.T) { testReadOnly(t, h) })
	t.Run("SessionState", func(t *testing.T) { testSessionPooled(t, h) })
	t.Run("TreePaging", func(t *testing.T) { testTreePaging(t, h) })
	t.Run("Describe", func(t *testing.T) { testDescribe(t, h) })
	t.Run("TreeRoutines", func(t *testing.T) { testTreeRoutines(t, h) })
	t.Run("Keys", func(t *testing.T) { testKeys(t, h) })
	t.Run("HostileIdentifiers", func(t *testing.T) { testHostile(t, h) })
	t.Run("BrowseLayout", func(t *testing.T) { testBrowseLayout(t, h) })
	t.Run("BrowsePaging", func(t *testing.T) { testBrowsePaging(t, h) })
	t.Run("BrowseFilter", func(t *testing.T) { testBrowseFilter(t, h) })
	t.Run("BrowseKeyless", func(t *testing.T) { testBrowseKeyless(t, h) })
	t.Run("BrowseHostile", func(t *testing.T) { testBrowseHostile(t, h) })
	t.Run("PlanPurity", func(t *testing.T) { testPlanPurity(t, h) })
	t.Run("PlanGuards", func(t *testing.T) { testPlanGuards(t, h) })
	t.Run("Explain", func(t *testing.T) { testExplain(t, h) })
	t.Run("SessionPing", func(t *testing.T) { testSessionPing(t, h) })
}

func session(t *testing.T, c tile.Conn, opt tile.SessionOptions) tile.Session {
	t.Helper()
	s, err := c.Session(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

type collected struct {
	schema *tile.ResultSchema
	rows   [][]tile.Value
	done   *tile.ResultSummary
}

// collect copies a whole result, checking frame order.
func collect(ctx context.Context, s tile.Session, req tile.Request) (collected, error) {
	var out collected
	st, err := s.Execute(ctx, req)
	if err != nil {
		return out, err
	}
	defer st.Close()
	for {
		f, err := st.Next(ctx)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		switch {
		case f.Schema != nil:
			out.schema = f.Schema
		case f.Batch != nil:
			if out.schema == nil {
				return out, errors.New("batch before schema")
			}
			w := len(out.schema.Fields)
			for r := range f.Batch.Rows {
				row := make([]tile.Value, w)
				for c := range w {
					v := f.Batch.Cells[r*w+c]
					row[c] = tile.Value{Kind: v.Kind, Raw: append([]byte(nil), v.Raw...)}
				}
				out.rows = append(out.rows, row)
			}
		case f.Done != nil:
			out.done = f.Done
		}
	}
}

func mustCollect(t *testing.T, s tile.Session, text string, args ...tile.Value) collected {
	t.Helper()
	c, err := collect(context.Background(), s, tile.Request{Text: text, Args: args})
	if err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	return c
}

func testStream(t *testing.T, h Harness) {
	s := session(t, h.Open(t), tile.SessionOptions{})
	const n = 25_000
	st, err := s.Execute(context.Background(), tile.Request{Text: h.ManyRowsSQL(n)})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var batches, rows int
	var sawSchema, sawDone bool
	for {
		f, err := st.Next(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		set := 0
		if f.Schema != nil {
			set++
			sawSchema = true
			if len(f.Schema.Fields) != 2 {
				t.Fatalf("fields = %+v", f.Schema.Fields)
			}
		}
		if f.Batch != nil {
			set++
			if !sawSchema || sawDone {
				t.Fatal("batch out of order")
			}
			if len(f.Batch.Cells) != f.Batch.Rows*2 {
				t.Fatalf("cells = %d for %d rows", len(f.Batch.Cells), f.Batch.Rows)
			}
			batches++
			rows += f.Batch.Rows
		}
		if f.Done != nil {
			set++
			sawDone = true
		}
		if set != 1 {
			t.Fatalf("frame has %d fields set", set)
		}
	}
	if rows < n || !sawDone {
		t.Fatalf("rows=%d done=%v", rows, sawDone)
	}
	if batches < 2 {
		t.Fatalf("%d rows arrived in %d batch: result was not streamed in batches", rows, batches)
	}
}

func testLossless(t *testing.T, h Harness) {
	s := session(t, h.Open(t), tile.SessionOptions{})
	c := mustCollect(t, s, `SELECT total FROM orders`)
	if got := string(c.rows[0][0].Raw); got != "12345678901234.5678" {
		t.Fatalf("decimal changed: %q", got)
	}
	if c.rows[0][0].Kind != tile.KindDecimal {
		t.Fatalf("kind = %d, want decimal", c.rows[0][0].Kind)
	}
	if c.schema.Fields[0].Name != "total" || c.schema.Fields[0].NativeType == "" {
		t.Fatalf("field = %+v", c.schema.Fields[0])
	}

	c = mustCollect(t, s, `SELECT id, name FROM customers ORDER BY id`)
	if c.rows[0][0].Kind != tile.KindInt || string(c.rows[0][0].Raw) != "1" {
		t.Fatalf("id = %+v", c.rows[0][0])
	}
	if c.rows[0][1].Kind != tile.KindText || string(c.rows[0][1].Raw) != "Ada" {
		t.Fatalf("name = %+v", c.rows[0][1])
	}
	if c.rows[1][1].Kind != tile.KindNull || c.rows[1][1].Raw != nil {
		t.Fatalf("NULL = %+v", c.rows[1][1])
	}
}

func testBindArgs(t *testing.T, h Harness) {
	s := session(t, h.Open(t), tile.SessionOptions{})
	hostile := tile.Text(`x' OR '1'='1`)
	c := mustCollect(t, s, "SELECT email FROM customers WHERE email = "+h.ArgMark, hostile)
	if len(c.rows) != 0 {
		t.Fatalf("bound value was treated as SQL: %d rows", len(c.rows))
	}
	c = mustCollect(t, s, "SELECT email FROM customers WHERE email = "+h.ArgMark, tile.Text("ada@example.com"))
	if len(c.rows) != 1 {
		t.Fatalf("rows = %d", len(c.rows))
	}
}

func testSummary(t *testing.T, h Harness) {
	s := session(t, h.Open(t), tile.SessionOptions{})
	c := mustCollect(t, s, `UPDATE customers SET name = name WHERE email = 'nobody@example.com'`)
	if c.done == nil || !c.done.HasAffected || c.done.RowsAffected != 0 {
		t.Fatalf("done = %+v", c.done)
	}
}

func testSyntaxError(t *testing.T, h Harness) {
	s := session(t, h.Open(t), tile.SessionOptions{})
	if _, err := collect(context.Background(), s, tile.Request{Text: `SELEC nonsense`}); err == nil {
		t.Fatal("want an error")
	}
	if c := mustCollect(t, s, `SELECT 1`); len(c.rows) != 1 {
		t.Fatal("session should survive a syntax error")
	}
}

func testCancel(t *testing.T, h Harness) {
	c := h.Open(t)
	s := session(t, c, tile.SessionOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := collect(ctx, s, tile.Request{Text: h.SleepSQL})
	if err == nil {
		t.Fatal("want an error from cancelling")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want it to wrap context.DeadlineExceeded", err)
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Fatalf("cancel took %v", el)
	}
	waitNotRunning(t, c, h, h.SleepSQL)
	if res := mustCollect(t, s, `SELECT 1`); len(res.rows) != 1 {
		t.Fatal("session should survive a cancel")
	}
}

func waitNotRunning(t *testing.T, c tile.Conn, h Harness, stmt string) {
	t.Helper()
	probe := session(t, c, tile.SessionOptions{})
	marker := stmt
	if len(marker) > 12 {
		marker = marker[:12]
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		res := mustCollect(t, probe, h.RunningSQL(marker))
		if string(res.rows[0][0].Raw) == "0" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%q is still running on the server", stmt)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func testEarlyClose(t *testing.T, h Harness) {
	s := session(t, h.Open(t), tile.SessionOptions{})
	ctx := context.Background()
	st, err := s.Execute(ctx, tile.Request{Text: h.ManyRowsSQL(1_000_000)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Next(ctx); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("closing early took %v, so it drained the result", el)
	}
	if res := mustCollect(t, s, `SELECT 1`); len(res.rows) != 1 {
		t.Fatal("session should survive an early close")
	}
}

func testReadOnly(t *testing.T, h Harness) {
	c := h.Open(t)
	if !c.Capabilities()[tile.CapReadOnly].Supported {
		t.Skip("no server-side read-only")
	}
	s := session(t, c, tile.SessionOptions{ReadOnly: true})
	if len(mustCollect(t, s, `SELECT 1`).rows) != 1 {
		t.Fatal("reads must work")
	}
	_, err := collect(context.Background(), s, tile.Request{Text: `INSERT INTO customers (email) VALUES ('ro@example.com')`})
	if err == nil {
		t.Fatal("the server accepted a write on a read-only session")
	}
}

// testSessionPooled checks read-only state cannot leak to the next session from the pool.
func testSessionPooled(t *testing.T, h Harness) {
	c := h.Open(t)
	for range 3 {
		ro, err := c.Session(context.Background(), tile.SessionOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		mustCollect(t, ro, `SELECT 1`)
		ro.Close()

		rw, err := c.Session(context.Background(), tile.SessionOptions{})
		if err != nil {
			t.Fatal(err)
		}
		mustCollect(t, rw, `UPDATE customers SET name = name WHERE email = 'nobody@example.com'`)
		rw.Close()
	}
}

func testTreePaging(t *testing.T, h Harness) {
	ctx := context.Background()
	tree := h.Open(t).Tree()

	roots, _, err := tree.Children(ctx, tile.ObjectRef{}, tile.Page{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	want := h.Schema.Path[0].Name
	found, system := false, ""
	for _, n := range roots {
		if n.Label == want && n.HasChildren {
			found = true
			if system != "" {
				t.Errorf("user schema %q listed after system schema %q", want, system)
			}
		}
		if system == "" && slices.Contains([]string{"information_schema", "pg_catalog", "mysql", "performance_schema", "sys"}, n.Label) {
			system = n.Label
		}
	}
	if !found {
		t.Fatalf("schema %q not in %+v", want, roots)
	}

	// One schema per page must still reach every schema exactly once, across the system ones.
	byOne := allChildren(t, tree, tile.ObjectRef{}, 1)
	if len(byOne) != len(allChildren(t, tree, tile.ObjectRef{}, 100)) {
		t.Fatalf("paging by one listed %d schemas", len(byOne))
	}

	// One table per page must still reach every table exactly once.
	seen := map[string]int{}
	cursor := ""
	for range 1000 {
		nodes, next, err := tree.Children(ctx, h.Schema, tile.Page{Cursor: cursor, Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(nodes) > 1 {
			t.Fatalf("limit ignored: %d nodes", len(nodes))
		}
		for _, n := range nodes {
			seen[n.Label]++
		}
		if next == "" {
			break
		}
		cursor = next
	}
	for _, name := range []string{"customers", "orders"} {
		if seen[name] != 1 {
			t.Fatalf("%s seen %d times in %v", name, seen[name], seen)
		}
	}
}

// allChildren reads every page of parent's children, failing on a repeated node.
func allChildren(t *testing.T, tree tile.ObjectTree, parent tile.ObjectRef, limit int) []tile.Node {
	t.Helper()
	var out []tile.Node
	seen := map[string]bool{}
	cursor := ""
	for range 100_000 {
		nodes, next, err := tree.Children(context.Background(), parent, tile.Page{Cursor: cursor, Limit: limit})
		if err != nil {
			t.Fatal(err)
		}
		if len(nodes) > limit {
			t.Fatalf("limit %d ignored: %d nodes", limit, len(nodes))
		}
		for _, n := range nodes {
			id := fmt.Sprint(n.Ref.Path)
			if seen[id] {
				t.Fatalf("%s listed twice", id)
			}
			seen[id] = true
			out = append(out, n)
		}
		if next == "" {
			return out
		}
		cursor = next
	}
	t.Fatal("paging never ended")
	return nil
}

func testTreeRoutines(t *testing.T, h Harness) {
	if h.CreateRoutine == nil {
		t.Skip("no routines")
	}
	c := h.Open(t)
	h.CreateRoutine(t, session(t, c, tile.SessionOptions{}))
	tree := c.Tree()

	var fn *tile.Node
	tables := 0
	for _, n := range allChildren(t, tree, h.Schema, 1) {
		switch {
		case n.Label == "tt_add":
			if fn != nil {
				t.Fatal("tt_add listed twice")
			}
			fn = &n
		case n.Label == "customers" || n.Label == "orders":
			if fn != nil {
				t.Errorf("table %s listed after routines", n.Label)
			}
			tables++
		}
	}
	if fn == nil || tables != 2 {
		t.Fatalf("routine found: %v, tables: %d", fn != nil, tables)
	}
	if fn.Kind != "function" || fn.Browsable || !fn.HasChildren {
		t.Fatalf("routine node = %+v", fn)
	}
	params := allChildren(t, tree, fn.Ref, 100)
	var got []string
	for _, p := range params {
		if p.Detail == "" || p.HasChildren || p.Browsable {
			t.Errorf("parameter node = %+v", p)
		}
		got = append(got, p.Kind+" "+p.Label)
	}
	if strings.Join(got, ",") != "parameter a,parameter b,return returns" {
		t.Fatalf("routine children = %v", got)
	}
}

func testKeys(t *testing.T, h Harness) {
	kd, ok := h.Open(t).Tree().(tile.KeyDescriber)
	if !ok {
		t.Skip("no KeyDescriber facet")
	}
	ctx := context.Background()
	k, err := kd.DescribeKeys(ctx, h.Schema.Child("table", "orders"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(k.Primary, []string{"id"}) || len(k.Foreign) != 1 {
		t.Fatalf("orders keys = %+v", k)
	}
	fk := k.Foreign[0]
	if fk.Name == "" || !slices.Equal(fk.Fields, []string{"customer_id"}) || !slices.Equal(fk.TargetFields, []string{"id"}) ||
		fmt.Sprint(fk.Target.Path) != fmt.Sprint(h.Schema.Child("table", "customers").Path) {
		t.Fatalf("orders foreign key = %+v", fk)
	}
	// The target must be browsable as the tree names it.
	if _, ok := h.Open(t).(tile.Browser); ok {
		layoutOf(t, h.Open(t), fk.Target)
	}
	k, err = kd.DescribeKeys(ctx, h.Schema.Child("table", "customers"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(k.Primary, []string{"id"}) || len(k.Foreign) != 0 {
		t.Fatalf("customers keys = %+v", k)
	}
}

func testDescribe(t *testing.T, h Harness) {
	d, err := h.Open(t).Tree().Describe(context.Background(), h.Schema.Child("table", "orders"))
	if err != nil {
		t.Fatal(err)
	}
	var cols *tile.DetailSection
	for i := range d.Sections {
		if d.Sections[i].Title == "Columns" {
			cols = &d.Sections[i]
		}
	}
	if cols == nil {
		t.Fatalf("no Columns section in %+v", d.Sections)
	}
	var names []string
	for _, r := range cols.Rows {
		names = append(names, string(r[0].Raw))
	}
	if strings.Join(names, ",") != "id,customer_id,total,placed_at" {
		t.Fatalf("columns = %v", names)
	}
}

func testHostile(t *testing.T, h Harness) {
	c := h.Open(t)
	name := h.HostileTable(t, session(t, c, tile.SessionOptions{}))
	tree := c.Tree()
	ctx := context.Background()
	cursor := ""
	found := false
	for range 1000 {
		nodes, next, err := tree.Children(ctx, h.Schema, tile.Page{Cursor: cursor, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range nodes {
			found = found || n.Label == name
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if !found {
		t.Fatalf("hostile table %q not listed", name)
	}
	d, err := tree.Describe(ctx, h.Schema.Child("table", name))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Sections) == 0 || len(d.Sections[0].Rows) != 1 {
		t.Fatalf("describe = %+v", d)
	}
}
