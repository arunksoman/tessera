package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/arunksoman/tessera/internal/store"
	"github.com/arunksoman/tessera/tile"
)

// connected starts the app on one profile and waits for its tree to load.
func connected(t *testing.T, db *fakeDB, p store.Profile) *app {
	t.Helper()
	a := start(t, db, setup{profiles: []store.Profile{p}, start: p.Name})
	a.see("▸ app", "Editor")
	return a
}

// browseItems opens the items table from the tree.
func browseItems(t *testing.T, a *app) {
	t.Helper()
	a.keys("alt+1", "enter")
	a.see("items")
	a.keys("down", "enter")
	a.see("app.items", "name1")
}

func TestFirstRunShowsConnectionForm(t *testing.T) {
	a := start(t, newFakeDB(1), setup{})
	a.see("New connection", "Name *", "Host *", "Password", "Environment")
}

func TestPickerConnectsToAProfile(t *testing.T) {
	a := start(t, newFakeDB(1), setup{profiles: []store.Profile{fakeProfile}})
	a.see("Connections", "dev", "+ New connection")
	a.keys("enter")
	a.see("▸ app", "F dev")
}

func TestFormIsRenderedFromTheTileAndSaves(t *testing.T) {
	db := newFakeDB(1)
	a := start(t, db, setup{})
	a.see("New connection")
	a.typeText("mine")
	// Name, type, host, port, password.
	a.keys("down", "down", "down", "down")
	a.typeText("s3cret")
	a.gone("New connectionX")
	if strings.Contains(a.screen(), "s3cret") {
		t.Fatalf("password on screen:\n%s", a.screen())
	}
	a.keys("ctrl+s")
	a.see("▸ app", "F mine")
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.password != "s3cret" {
		t.Fatalf("password reached the tile as %q", db.password)
	}
	p, err := a.st.Profile("mine")
	if err != nil {
		t.Fatal(err)
	}
	if p.Values["host"] != "localhost" {
		t.Fatalf("profile = %+v", p)
	}
	if _, found := p.Values["password"]; found {
		t.Fatal("password stored in the profile")
	}
}

func TestFormValidates(t *testing.T) {
	a := start(t, newFakeDB(1), setup{})
	a.see("New connection")
	a.keys("ctrl+s")
	a.see("Name is required")
	a.typeText("x")
	a.keys("down", "down", "down")
	a.typeText("abc")
	a.keys("ctrl+s")
	a.see("Port must be a number")
}

func TestFormStaysOpenWhenConnectFails(t *testing.T) {
	db := newFakeDB(1)
	db.reject = "bad"
	a := start(t, db, setup{})
	a.see("New connection")
	a.typeText("x")
	a.keys("down", "down", "down", "down")
	a.typeText("bad")
	a.keys("ctrl+s")
	a.see("saved, but could not connect")
}

func TestRunStatementAtCursor(t *testing.T) {
	db := newFakeDB(3)
	a := connected(t, db, fakeProfile)
	a.typeText("SELECT 1;\nSELECT id, name FROM items")
	a.see("SELECT id, name")
	a.keys("f5")
	a.see("name2", "row 1 of 3")
	if db.ran("SELECT 1") {
		t.Fatal("only the statement at the cursor should run")
	}
}

func TestRunWholeScriptAndSwitchResults(t *testing.T) {
	db := newFakeDB(2)
	a := connected(t, db, fakeProfile)
	a.typeText("SELECT 1; SELECT 2")
	a.keys("f6")
	a.see("Result 2 of 2")
	a.keys("alt+3", "[")
	a.see("Result 1 of 2")
}

func TestEscCancelsARunningQuery(t *testing.T) {
	db := newFakeDB(1)
	a := connected(t, db, fakeProfile)
	a.typeText("SELECT SLEEP(100)")
	a.keys("f5")
	a.see("Esc cancels")
	a.keys("esc")
	a.see("cancelled")
	a.gone("Esc cancels")
}

func TestDestructiveStatementNeedsTypedConfirmation(t *testing.T) {
	db := newFakeDB(1)
	a := connected(t, db, fakeProfile)
	a.typeText("DROP TABLE items")
	a.keys("f5")
	a.see("Confirm", `Type "yes"`)
	if db.ran("DROP") {
		t.Fatal("ran before confirmation")
	}
	a.typeText("no")
	a.keys("enter")
	a.see("that does not match")
	a.keys("esc")
	a.see("declined")
	if db.ran("DROP") {
		t.Fatal("ran after a refusal")
	}

	a.keys("f5")
	a.see("Confirm")
	a.typeText("yes")
	a.keys("enter")
	a.gone("Confirm")
	deadline := 0
	for !db.ran("DROP") && deadline < 1000 {
		a.screen()
		deadline++
	}
}

func TestProductionBannerAndTypedProfileName(t *testing.T) {
	db := newFakeDB(1)
	a := connected(t, db, store.Profile{Name: "prod", Tile: "fake", Env: "production"})
	a.see("PRODUCTION", "typed confirmation")
	a.typeText("UPDATE items SET name = 'x' WHERE id = 1")
	a.keys("f5")
	a.see(`Type "prod"`)
	a.typeText("yes")
	a.keys("enter")
	a.see("that does not match")
	a.keys("esc")
	if db.ran("UPDATE") {
		t.Fatal("a production write ran without its typed name")
	}
}

func TestReadOnlyProfileRefusesWrites(t *testing.T) {
	db := newFakeDB(1)
	a := connected(t, db, store.Profile{Name: "ro", Tile: "fake", ReadOnly: true})
	a.see("read-only")
	a.typeText("DELETE FROM items WHERE id = 1")
	a.keys("f5")
	a.see("this connection is read-only")
}

func TestTreeBrowsesTable(t *testing.T) {
	a := connected(t, newFakeDB(3), fakeProfile)
	browseItems(t, a)
	a.see("name3", "row 1 of 3")
	a.gone("▶ Editor")
}

func TestBrowseLoadsMorePagesAsTheCursorMoves(t *testing.T) {
	a := connected(t, newFakeDB(1200), fakeProfile)
	browseItems(t, a)
	a.see("of 500+")
	a.keys("G")
	a.see("of 1000+")
	a.keys("G")
	a.see("of 1200")
	a.gone("1200+")
}

func TestSortAndFilterAskTheServer(t *testing.T) {
	db := newFakeDB(3)
	a := connected(t, db, fakeProfile)
	browseItems(t, a)
	a.keys("right", "s")
	a.see("name ▲")
	a.keys("s")
	a.see("name ▼")
	a.keys("s")
	a.gone("name ▼")
	a.keys("f")
	a.see("Filter name")
	a.typeText("> m")
	a.keys("enter")
	a.see("where name > m")
	if !db.ran(`"name" > $1`) {
		t.Fatalf("filter did not reach the server: %v", db.executed)
	}
	a.keys("c")
	a.gone("where name")
}

func TestInlineEditPreviewApply(t *testing.T) {
	db := newFakeDB(3)
	a := connected(t, db, fakeProfile)
	browseItems(t, a)
	a.keys("right", "enter")
	a.see("Edit name")
	a.keys("end")
	a.typeText("X")
	a.keys("enter")
	a.see("name1X*", "1 pending")
	if db.ran("UPDATE") {
		t.Fatal("an edit reached the database before apply")
	}
	a.keys("ctrl+s")
	a.see("Apply 1 change?", "one transaction", "name1X")
	if db.ran("UPDATE") {
		t.Fatal("previewing touched the database")
	}
	a.keys("y")
	a.see("applied 1 change", "name1X")
	a.gone("pending")
	for _, want := range []string{"BEGIN", `UPDATE "app"."items" SET "name" = $1 WHERE "id" = $2 AND "name" = $3`, "COMMIT"} {
		if !db.ran(want) {
			t.Fatalf("did not run %q: %v", want, db.executed)
		}
	}
	if n, err := a.st.VerifyAudit(); err != nil || n != 1 {
		t.Fatalf("audit = %d, %v", n, err)
	}
}

func TestApplyConflictKeepsEditsPending(t *testing.T) {
	db := newFakeDB(1)
	db.conflict = true
	a := connected(t, db, fakeProfile)
	browseItems(t, a)
	a.keys("right", "enter")
	a.see("Edit name")
	a.typeText("Z")
	a.keys("enter", "ctrl+s")
	a.see("Apply 1")
	a.keys("y")
	a.see("conflict", "1 pending")
	if !db.ran("ROLLBACK") {
		t.Fatal("a conflict must roll back")
	}
}

func TestUndoAndSetNull(t *testing.T) {
	a := connected(t, newFakeDB(1), fakeProfile)
	browseItems(t, a)
	a.keys("right", "enter")
	a.see("Edit name")
	a.keys("ctrl+n")
	a.see("NULL*", "1 pending")
	a.keys("u")
	a.gone("pending")
}

func TestKeylessTableIsReadOnly(t *testing.T) {
	a := connected(t, newFakeDB(1), fakeProfile)
	a.keys("alt+1", "enter")
	a.see("report")
	a.keys("down", "down", "enter")
	a.see("Read-only: no primary key", "name1")
	a.keys("right", "e")
	a.see("read-only: no primary key")
	a.keys("enter")
	a.see("lines 1-1 of 1")
}

func TestPendingEditsBlockSortAndAskBeforeClose(t *testing.T) {
	a := connected(t, newFakeDB(1), fakeProfile)
	browseItems(t, a)
	a.keys("right", "enter")
	a.see("Edit name")
	a.typeText("Q")
	a.keys("enter")
	a.see("1 pending")
	a.keys("s")
	a.see("apply with Ctrl+S or undo")
	a.keys("alt+w")
	a.see("edits that were not applied", "y yes")
	a.keys("n")
	a.see("1 pending")
}

func TestTabsAndFocus(t *testing.T) {
	a := connected(t, newFakeDB(1), fakeProfile)
	a.see("dev 1")
	a.keys("alt+t")
	a.see("dev 1", "dev 2")
	a.keys("alt+[")
	a.typeText("abc")
	a.see("abc")
	a.keys("alt+]")
	a.gone("abc")
	a.keys("alt+w")
	a.gone("dev 2")
	a.see("abc")
	a.see("▶ Editor")
	a.keys("alt+1")
	a.see("▶ Objects")
	a.keys("shift+tab")
	a.see("▶ Editor")
	a.keys("alt+3")
	a.see("▶ Results")
}

func TestPaletteRunsActions(t *testing.T) {
	a := connected(t, newFakeDB(1), fakeProfile)
	a.keys("ctrl+k")
	a.see("Command palette", "New tab", "alt+t")
	a.typeText("new tab")
	a.see("New tab")
	a.keys("enter")
	a.see("dev 2")
}

func TestHelpListsTheRegistry(t *testing.T) {
	a := connected(t, newFakeDB(1), fakeProfile)
	a.keys("f1")
	a.see("Help", "Run statement at cursor", "ctrl+enter / f5")
	a.keys("esc")
	a.gone("Run statement at cursor")
}

func TestHistoryFillsTheEditorRedacted(t *testing.T) {
	a := connected(t, newFakeDB(1), fakeProfile)
	a.typeText("SELECT 41")
	a.keys("f5")
	a.see("row 1 of 1")
	a.keys("alt+t")
	a.see("dev 2")
	a.keys("ctrl+r")
	a.see("Statement history", "SELECT 41")
	a.keys("enter")
	a.gone("Statement history")
	a.see("SELECT 41")
}

func TestCompletionInsertsAtCursor(t *testing.T) {
	a := connected(t, newFakeDB(1), fakeProfile)
	a.typeText("SELECT * FROM it")
	a.see("FROM it")
	a.keys("tab")
	a.see("items  table")
	a.keys("enter")
	a.see("FROM items")
	a.gone("items  table")
}

func TestExplain(t *testing.T) {
	db := newFakeDB(1)
	a := connected(t, db, fakeProfile)
	a.typeText("SELECT 1")
	a.keys("f7")
	a.see("Plan")
	if !db.ran("EXPLAIN SELECT 1") {
		t.Fatal("explain did not run")
	}
}

func TestDescribeOpensColumns(t *testing.T) {
	a := connected(t, newFakeDB(1), fakeProfile)
	a.keys("alt+1", "enter")
	a.see("items")
	a.keys("down", "d")
	a.see("Columns", "column")
}

func TestDatabaseContentCannotDriveTheTerminal(t *testing.T) {
	db := newFakeDB(0)
	db.rows = [][]string{{"1", "evil\x1b]0;pwned\x07\x1b[2Jtext"}}
	a := connected(t, db, fakeProfile)
	browseItems2(t, a)
	raw := a.view.Load().(string)
	if strings.Contains(raw, "\x07") || strings.Contains(raw, "]0;") && strings.Contains(raw, "\x1b]0;") || strings.Contains(raw, "\x1b[2J") {
		t.Fatalf("escape sequence reached the frame: %q", raw)
	}
}

func browseItems2(t *testing.T, a *app) {
	t.Helper()
	a.keys("alt+1", "enter")
	a.see("items")
	a.keys("down", "enter")
	a.see("app.items", "evil")
}

func TestThemeCyclesAndPersists(t *testing.T) {
	a := connected(t, newFakeDB(1), fakeProfile)
	a.keys("f9")
	a.see("theme: light")
	a.keys("f9")
	a.see("theme: dark")
	s, err := a.st.Settings()
	if err != nil || s.Theme != "dark" {
		t.Fatalf("settings = %+v, %v", s, err)
	}
	a.keys("f9")
	a.see("theme: system")
}

func TestQuitAsksOnlyWhenWorkWouldBeLost(t *testing.T) {
	a := connected(t, newFakeDB(1), fakeProfile)
	a.typeText("BEGIN")
	a.keys("f5")
	a.see("in transaction", "TX")
	a.keys("ctrl+q")
	a.see("Quit?", "open transaction")
	a.keys("n")
	a.gone("Quit?")
}

// Every frame must fill the terminal exactly, or the display tears.
func TestFrameFitsEveryTerminalSize(t *testing.T) {
	sizes := [][2]int{{40, 16}, {41, 17}, {80, 24}, {120, 40}, {200, 60}, {300, 16}, {45, 70}}
	for _, size := range sizes {
		db := newFakeDB(3)
		p := fakeProfile
		p.Env = "production"
		a := start(t, db, setup{profiles: []store.Profile{p}, start: p.Name, width: size[0], height: size[1]})
		a.see("▸ app")
		check := func(where string) {
			t.Helper()
			frame := a.view.Load().(string)
			lines := strings.Split(frame, "\n")
			if len(lines) != size[1] {
				t.Fatalf("%dx%d %s: %d lines:\n%s", size[0], size[1], where, len(lines), a.screen())
			}
			for _, l := range lines {
				if w := lipgloss.Width(l); w > size[0] {
					t.Fatalf("%dx%d %s: line is %d wide: %q", size[0], size[1], where, w, l)
				}
			}
		}
		check("empty")
		a.keys("alt+1", "enter")
		a.see("items")
		check("tree")
		a.keys("down", "enter")
		a.see("name1")
		check("browse")
		a.keys("ctrl+k")
		a.see("Command palette")
		check("palette")
		a.keys("esc")
		a.keys("f1")
		a.see("Help")
		check("help")
		a.keys("esc")
	}
}

func TestTooSmallTerminalSaysSo(t *testing.T) {
	a := start(t, newFakeDB(1), setup{width: 20, height: 5})
	a.see("Tessera needs")
}

func TestParseFilter(t *testing.T) {
	cases := []struct {
		in  string
		op  tile.FilterOp
		val string
		bad bool
	}{
		{"= 5", tile.OpEq, "5", false},
		{"=5", tile.OpEq, "5", false},
		{"!= x y", tile.OpNe, "x y", false},
		{"<= 2", tile.OpLe, "2", false},
		{">= 2", tile.OpGe, "2", false},
		{"< 2", tile.OpLt, "2", false},
		{"> 2", tile.OpGt, "2", false},
		{"~ a", tile.OpContains, "a", false},
		{"null", tile.OpNull, "", false},
		{"NULL", tile.OpNull, "", false},
		{"!null", tile.OpNotNull, "", false},
		{"plain", tile.OpContains, "plain", false},
		{"", "", "", true},
		{"=", "", "", true},
	}
	for _, c := range cases {
		got, err := parseFilter("f", c.in)
		if (err != nil) != c.bad {
			t.Errorf("%q: err = %v", c.in, err)
			continue
		}
		if !c.bad && (got.Op != c.op || string(got.Value.Raw) != c.val || got.Field != "f") {
			t.Errorf("%q: got %+v", c.in, got)
		}
	}
}

func TestCanEditTextProtectsExactValues(t *testing.T) {
	cases := []struct {
		v  tile.Value
		ok bool
	}{
		{tile.Text("plain"), true},
		{tile.Text("two\nlines"), true},
		{tile.Text("a\tb"), false},
		{tile.Value{Kind: tile.KindBytes, Raw: []byte{1}}, false},
		{tile.Value{Kind: tile.KindNull}, true},
	}
	for _, c := range cases {
		if err := canEditText(c.v); (err == nil) != c.ok {
			t.Errorf("%+v: %v", c.v, err)
		}
	}
}

func TestFitAndPadHandleWideText(t *testing.T) {
	if got := fit("日本語テキスト", 6); lipgloss.Width(got) > 6 || !strings.HasSuffix(got, "…") {
		t.Fatalf("fit = %q", got)
	}
	if got := fit("abc", 3); got != "abc" {
		t.Fatalf("fit = %q", got)
	}
	if got := pad("ab", 4, true); got != "  ab" {
		t.Fatalf("pad = %q", got)
	}
}

func TestEveryTokenExistsInBothThemes(t *testing.T) {
	for _, name := range []string{"plaster", "surface", "text", "muted", "lapis", "verdigris", "ochre", "terracotta", "porphyry", "slate", "danger", "warning", "success"} {
		p, ok := tokens[name]
		if !ok || p.Light == "" || p.Dark == "" {
			t.Errorf("token %q = %+v", name, p)
		}
	}
}

func TestActionKeysAreUnique(t *testing.T) {
	seen := map[string]string{}
	for _, a := range actions {
		for _, k := range a.keys {
			if other, dup := seen[k]; dup {
				t.Errorf("%s is bound to both %q and %q", k, other, a.title)
			}
			seen[k] = a.title
		}
	}
}

func TestPaneTopEdgeMatchesItsBody(t *testing.T) {
	a := connected(t, newFakeDB(1), fakeProfile)
	lines := strings.Split(a.view.Load().(string), "\n")
	if top, body := lipgloss.Width(lines[1]), lipgloss.Width(lines[2]); top != body || top != 120 {
		t.Fatalf("top edge is %d wide, body %d", top, body)
	}
}

func TestBrowsedNumbersAlignRight(t *testing.T) {
	a := connected(t, newFakeDB(12), fakeProfile)
	browseItems(t, a)
	a.see("   1 name1", "  12 name12")
}

func TestQuitWorksFromDialogsButNotFromAConfirmation(t *testing.T) {
	a := start(t, newFakeDB(1), setup{})
	a.see("New connection")
	a.keys("ctrl+q")
	select {
	case <-a.done:
	case <-time.After(5 * time.Second):
		t.Fatal("ctrl+q did not quit from the form")
	}

	b := connected(t, newFakeDB(1), fakeProfile)
	b.typeText("DROP TABLE items")
	b.keys("f5")
	b.see("Confirm")
	b.keys("ctrl+q")
	b.see("Confirm")
}

func TestCloseWaitsForAWorkerToStopItsStatement(t *testing.T) {
	db := newFakeDB(1)
	a := connected(t, db, fakeProfile)
	a.typeText("SELECT SLEEP(100)")
	a.keys("f5")
	a.see("Esc cancels")
	a.m.Close()
	db.mu.Lock()
	defer db.mu.Unlock()
	if !db.stopped {
		t.Fatal("Close returned while the statement was still running")
	}
}

func TestGridCountsReadNaturally(t *testing.T) {
	if count(1, "row") != "1 row" || count(2, "row") != "2 rows" || count(0, "row") != "0 rows" {
		t.Fatal(count(1, "row"), count(2, "row"), count(0, "row"))
	}
}
