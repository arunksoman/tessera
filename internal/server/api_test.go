package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arunksoman/tessera/internal/store"
	"github.com/arunksoman/tessera/tile"
)

func (f *fixture) login() {
	w := get(f.s.Handler(), "/auth?token="+f.s.token)
	f.cookies = w.Result().Cookies()
	if len(f.cookies) != 1 {
		f.t.Fatalf("login: %d", w.Code)
	}
}

func (f *fixture) request(ctx context.Context, method, path string, body any) *http.Request {
	var b []byte
	if body != nil {
		var err error
		if b, err = json.Marshal(body); err != nil {
			f.t.Fatal(err)
		}
	}
	req := httptest.NewRequestWithContext(ctx, method, path, bytes.NewReader(b))
	req.Host = "127.0.0.1:7000"
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, c := range f.cookies {
		req.AddCookie(c)
	}
	return req
}

// call sends a JSON request and decodes a JSON reply into out, if given.
func (f *fixture) call(method, path string, body any, out any) int {
	f.t.Helper()
	w := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(w, f.request(context.Background(), method, path, body))
	if out != nil && w.Body.Len() > 0 {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			f.t.Fatalf("%s %s: %v in %s", method, path, err, w.Body)
		}
	}
	return w.Code
}

type execEvent struct {
	T           string     `json:"t"`
	Stmt        int        `json:"stmt"`
	Fields      []fieldOut `json:"fields"`
	Rows        [][]any    `json:"rows"`
	Affected    int64      `json:"affected"`
	HasAffected bool       `json:"hasAffected"`
	Error       string     `json:"error"`
	InTx        bool       `json:"inTx"`
	DDL         bool       `json:"ddl"`
}

type fieldOut struct{ Name, Kind string }

func (f *fixture) exec(profile, tab, sql string, confirm ...string) (int, []execEvent, map[string]any) {
	f.t.Helper()
	w := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(w, f.request(context.Background(), "POST", "/api/conns/"+profile+"/exec",
		map[string]any{"tab": tab, "sql": sql, "confirm": confirm}))
	if w.Code != http.StatusOK {
		var e map[string]any
		json.Unmarshal(w.Body.Bytes(), &e)
		return w.Code, nil, e
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/x-ndjson" {
		f.t.Fatalf("content type %q", ct)
	}
	var evs []execEvent
	sc := bufio.NewScanner(w.Body)
	sc.Buffer(nil, 1<<24)
	for sc.Scan() {
		var ev execEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			f.t.Fatalf("%v in %s", err, sc.Text())
		}
		evs = append(evs, ev)
	}
	return w.Code, evs, nil
}

func TestAPINeedsSession(t *testing.T) {
	f := newFixture(t, store.Profile{Name: "dev"})
	if code := f.call("GET", "/api/profiles", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("no session: %d", code)
	}
	f.login()
	if code := f.call("GET", "/api/profiles", nil, nil); code != http.StatusOK {
		t.Fatalf("with session: %d", code)
	}
	// A plain form post cannot send JSON, so requiring it also blocks simple cross-site requests.
	req := f.request(context.Background(), "POST", "/api/conns/dev/exec", nil)
	req.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain: %d", w.Code)
	}
}

func TestTilesDescribeForms(t *testing.T) {
	f := newFixture(t)
	f.login()
	var tiles []struct {
		ID, Name string
		Style    struct{ Color, Glyph string }
		Form     []struct{ Name, Label, Type string }
	}
	if code := f.call("GET", "/api/tiles", nil, &tiles); code != 200 {
		t.Fatal(code)
	}
	if len(tiles) != 1 || tiles[0].ID != "fake" || len(tiles[0].Form) != 5 || tiles[0].Form[2].Type != "secret" || tiles[0].Style.Glyph != "F" {
		t.Fatalf("%+v", tiles)
	}
}

type profileOut struct {
	Name      string            `json:"name"`
	Tile      string            `json:"tile"`
	Env       string            `json:"env"`
	ReadOnly  bool              `json:"readOnly"`
	Values    map[string]string `json:"values"`
	Secrets   map[string]bool   `json:"secrets"`
	Warnings  []string          `json:"warnings"`
	Connected bool              `json:"connected"`
	Error     string            `json:"error"`
}

func TestProfileLifecycle(t *testing.T) {
	f := newFixture(t)
	f.login()
	body := map[string]any{"tile": "fake", "env": "production", "values": map[string]string{"host": "db", "port": "5432", "mode": "weak"},
		"secrets": map[string]string{"password": "hunter2"}}
	var saved profileOut
	if code := f.call("PUT", "/api/profiles/app", body, &saved); code != 200 {
		t.Fatalf("save: %d %+v", code, saved)
	}
	if !saved.Connected || saved.Error != "" || f.db.password != "hunter2" {
		t.Fatalf("saved %+v, password %q", saved, f.db.password)
	}

	var list []profileOut
	f.call("GET", "/api/profiles", nil, &list)
	if len(list) != 1 || list[0].Name != "app" || list[0].Values["host"] != "db" || !list[0].Secrets["password"] ||
		list[0].Env != "production" || len(list[0].Warnings) != 1 {
		t.Fatalf("%+v", list)
	}
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), "hunter2") {
		t.Fatal("profile list leaks the password")
	}

	// Renaming without retyping the password keeps it.
	body = map[string]any{"from": "app", "tile": "fake", "values": map[string]string{"host": "db"}}
	if code := f.call("PUT", "/api/profiles/app2", body, &saved); code != 200 || !saved.Connected {
		t.Fatalf("rename: %d %+v", code, saved)
	}
	f.call("GET", "/api/profiles", nil, &list)
	if len(list) != 1 || list[0].Name != "app2" || !list[0].Secrets["password"] {
		t.Fatalf("after rename %+v", list)
	}

	// A failed connection still saves, and says why.
	f.db.failOpen = true
	if code := f.call("PUT", "/api/profiles/app2", map[string]any{"tile": "fake", "values": map[string]string{"host": "x"}}, &saved); code != 200 || saved.Connected || saved.Error == "" {
		t.Fatalf("failed connect: %d %+v", code, saved)
	}
	f.db.failOpen = false

	if code := f.call("DELETE", "/api/profiles/app2", nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if ok, _ := f.e.HasSecret("app2", "password"); ok {
		t.Fatal("delete kept the password")
	}
	if code := f.call("DELETE", "/api/profiles/app2", nil, nil); code != http.StatusNotFound {
		t.Fatalf("delete again: %d", code)
	}
}

func TestProfileValidation(t *testing.T) {
	f := newFixture(t)
	f.login()
	cases := []struct {
		name string
		body map[string]any
	}{
		{"unknown tile", map[string]any{"tile": "nope", "values": map[string]string{"host": "h"}}},
		{"field not in form", map[string]any{"tile": "fake", "values": map[string]string{"host": "h", "sslrootcert": "/etc/x"}}},
		{"secret as a value", map[string]any{"tile": "fake", "values": map[string]string{"host": "h", "password": "x"}}},
		{"value as a secret", map[string]any{"tile": "fake", "values": map[string]string{"host": "h"}, "secrets": map[string]string{"host": "x"}}},
		{"required missing", map[string]any{"tile": "fake", "values": map[string]string{}}},
		{"int not a number", map[string]any{"tile": "fake", "values": map[string]string{"host": "h", "port": "5432; x"}}},
		{"enum not an option", map[string]any{"tile": "fake", "values": map[string]string{"host": "h", "mode": "other"}}},
		{"bad env", map[string]any{"tile": "fake", "env": "prod", "values": map[string]string{"host": "h"}}},
		{"unknown key", map[string]any{"tile": "fake", "values": map[string]string{"host": "h"}, "dsn": "postgres://"}},
		{"rename from missing", map[string]any{"from": "ghost", "tile": "fake", "values": map[string]string{"host": "h"}}},
	}
	for _, c := range cases {
		var e map[string]any
		if code := f.call("PUT", "/api/profiles/x", c.body, &e); code != http.StatusBadRequest {
			t.Errorf("%s: %d %v", c.name, code, e)
		}
	}
	if code := f.call("PUT", "/api/profiles/bad%20name", map[string]any{"tile": "fake", "values": map[string]string{"host": "h"}}, nil); code != http.StatusBadRequest {
		t.Errorf("bad name: %d", code)
	}
}

type nodeOut struct {
	Ref         []map[string]string `json:"ref"`
	Kind        string              `json:"kind"`
	Label       string              `json:"label"`
	HasChildren bool                `json:"hasChildren"`
	Browsable   bool                `json:"browsable"`
}

type treeOut struct {
	Nodes []nodeOut `json:"nodes"`
	Next  string    `json:"next"`
}

func TestTreePages(t *testing.T) {
	f := newFixture(t, store.Profile{Name: "dev"})
	f.login()
	var root treeOut
	if code := f.call("POST", "/api/conns/dev/tree", map[string]any{"ref": []any{}}, &root); code != 200 || len(root.Nodes) != 1 || !root.Nodes[0].HasChildren {
		t.Fatalf("%d %+v", code, root)
	}
	var page treeOut
	f.call("POST", "/api/conns/dev/tree", map[string]any{"ref": root.Nodes[0].Ref}, &page)
	if len(page.Nodes) != 1 || page.Nodes[0].Label != "items" || page.Next != "page2" || len(page.Nodes[0].Ref) != 2 {
		t.Fatalf("%+v", page)
	}
	page = treeOut{}
	f.call("POST", "/api/conns/dev/tree", map[string]any{"ref": root.Nodes[0].Ref, "cursor": "page2"}, &page)
	if len(page.Nodes) != 1 || page.Nodes[0].Label != "report" || page.Next != "" {
		t.Fatalf("%+v", page)
	}
	if code := f.call("POST", "/api/conns/nope/tree", map[string]any{"ref": []any{}}, nil); code != http.StatusNotFound {
		t.Fatalf("unknown profile: %d", code)
	}
}

var items = []map[string]string{{"kind": "schema", "name": "app"}, {"kind": "table", "name": "items"}}

type browseOut struct {
	Layout struct {
		Fields   []fieldOut `json:"fields"`
		Key      []string   `json:"key"`
		Note     string     `json:"note"`
		Estimate int64      `json:"estimate"`
	} `json:"layout"`
	Fields []fieldOut `json:"fields"`
	Rows   [][]any    `json:"rows"`
	Error  string     `json:"error"`
}

func TestBrowsePages(t *testing.T) {
	f := newFixture(t, store.Profile{Name: "dev"})
	f.login()
	var b browseOut
	if code := f.call("POST", "/api/conns/dev/browse", map[string]any{"ref": items, "limit": 100}, &b); code != 200 {
		t.Fatalf("%d %+v", code, b)
	}
	if len(b.Rows) != 100 || b.Rows[0][0] != "1" || b.Layout.Key[0] != "id" || b.Layout.Estimate != 1000 || b.Fields[0].Kind != "int" {
		t.Fatalf("%+v", b.Layout)
	}
	after := b.Rows[99]
	b = browseOut{}
	if code := f.call("POST", "/api/conns/dev/browse", map[string]any{"ref": items, "limit": 100, "after": after}, &b); code != 200 || b.Rows[0][0] != "101" {
		t.Fatalf("page 2: %d %v", code, b.Rows[:1])
	}
	if !f.db.ran("LIMIT 100") {
		t.Fatal("limit not passed")
	}
	if code := f.call("POST", "/api/conns/dev/browse", map[string]any{"ref": items, "limit": 100,
		"sort":   []map[string]any{{"field": "name", "desc": true}},
		"filter": []map[string]any{{"field": "name", "op": "~", "value": "1"}}}, &b); code != 200 {
		t.Fatalf("sort and filter: %d %s", code, b.Error)
	}
	if !f.db.ran(`"name" DESC`) || !f.db.ran("LIKE") {
		t.Fatalf("executed %q", f.db.executed)
	}
	if code := f.call("POST", "/api/conns/dev/browse", map[string]any{"ref": items, "sort": []map[string]any{{"field": "x; DROP"}}}, nil); code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", code)
	}
	if code := f.call("POST", "/api/conns/dev/browse", map[string]any{"ref": items, "filter": []map[string]any{{"field": "name", "op": "LIKE"}}}, nil); code != http.StatusBadRequest {
		t.Fatalf("unknown op: %d", code)
	}
	b = browseOut{}
	if code := f.call("POST", "/api/conns/dev/browse", map[string]any{"ref": items, "limit": 1_000_000}, &b); code != 200 || len(b.Rows) != 250 || !f.db.ran("LIMIT 1000") {
		t.Fatalf("limit cap: %d %d", code, len(b.Rows))
	}
}

func TestExecStreams(t *testing.T) {
	f := newFixture(t, store.Profile{Name: "dev"})
	f.login()
	code, evs, _ := f.exec("dev", "t1", "SELECT * FROM items; SELECT 2")
	if code != 200 {
		t.Fatal(code)
	}
	var rows int
	var kinds []string
	for _, ev := range evs {
		kinds = append(kinds, ev.T)
		if ev.T == "rows" && ev.Stmt == 0 {
			rows += len(ev.Rows)
		}
	}
	got := strings.Join(kinds, " ")
	if !strings.HasPrefix(got, "schema rows rows rows done schema") || !strings.HasSuffix(got, "done end") || rows != 250 {
		t.Fatalf("events %s, rows %d", got, rows)
	}
	if evs[0].Fields[0].Name != "id" || evs[0].Fields[0].Kind != "int" || evs[1].Rows[0][0] != "1" {
		t.Fatalf("%+v %+v", evs[0], evs[1])
	}
}

func TestExecCapsRows(t *testing.T) {
	f := newFixture(t, store.Profile{Name: "dev"})
	f.s.maxRows = 150
	f.login()
	_, evs, _ := f.exec("dev", "t1", "SELECT * FROM items; SELECT 2")
	rows, truncated := 0, 0
	for _, ev := range evs {
		if ev.T == "rows" && ev.Stmt == 0 {
			rows += len(ev.Rows)
		}
		if ev.T == "truncated" {
			truncated++
		}
	}
	if rows != 150 || truncated != 2 {
		t.Fatalf("rows %d, truncated %d", rows, truncated)
	}
}

func TestExecBytesAreLossless(t *testing.T) {
	f := newFixture(t, store.Profile{Name: "dev"})
	f.login()
	_, evs, _ := f.exec("dev", "t1", "BYTES")
	if len(evs) < 2 || evs[1].T != "rows" {
		t.Fatalf("%+v", evs)
	}
	cell, ok := evs[1].Rows[0][0].(map[string]any)
	if !ok || cell["b64"] != "/wA=" || evs[1].Rows[1][0] != nil {
		t.Fatalf("%#v", evs[1].Rows)
	}
}

func TestExecConfirmation(t *testing.T) {
	f := newFixture(t, store.Profile{Name: "dev"}, store.Profile{Name: "ro", ReadOnly: true})
	f.login()
	code, _, e := f.exec("dev", "t1", "SELECT 1; DROP TABLE items")
	if code != http.StatusConflict {
		t.Fatalf("drop: %d", code)
	}
	prompts, _ := e["prompts"].([]any)
	if len(prompts) != 1 || prompts[0].(map[string]any)["statement"] != "DROP TABLE items" {
		t.Fatalf("%v", e)
	}
	if f.db.ran("SELECT 1") {
		t.Fatal("ran part of a refused script")
	}
	if code, _, _ := f.exec("dev", "t1", "SELECT 1; DROP TABLE items", "DROP TABLE other"); code != http.StatusConflict {
		t.Fatalf("confirming another statement: %d", code)
	}
	if code, _, _ := f.exec("dev", "t1", "SELECT 1; DROP TABLE items", "DROP TABLE items"); code != 200 || !f.db.ran("DROP TABLE items") {
		t.Fatalf("confirmed: %d", code)
	}
	if code, _, e := f.exec("ro", "t1", "DROP TABLE items", "DROP TABLE items"); code != http.StatusForbidden {
		t.Fatalf("read-only: %d %v", code, e)
	}
}

func TestExecConsoleKeepsTransaction(t *testing.T) {
	f := newFixture(t, store.Profile{Name: "dev"})
	f.login()
	_, evs, _ := f.exec("dev", "t1", "BEGIN")
	if last := evs[len(evs)-1]; last.T != "end" || !last.InTx {
		t.Fatalf("%+v", last)
	}
	_, evs, _ = f.exec("dev", "t2", "SELECT 1")
	if evs[len(evs)-1].InTx {
		t.Fatal("another tab shares the transaction")
	}
	_, evs, _ = f.exec("dev", "t1", "COMMIT")
	if evs[len(evs)-1].InTx {
		t.Fatal("commit left the transaction open")
	}
	if code := f.call("DELETE", "/api/conns/dev/tabs/t1", nil, nil); code != http.StatusNoContent {
		t.Fatalf("close tab: %d", code)
	}
	_, evs, _ = f.exec("dev", "t1", "CREATE TABLE x (a int)")
	if !evs[len(evs)-1].DDL {
		t.Fatal("DDL not reported, so the tree would not refresh")
	}
}

func TestExecConnectFailure(t *testing.T) {
	f := newFixture(t, store.Profile{Name: "dev"})
	f.login()
	f.db.failOpen = true
	code, _, e := f.exec("dev", "t1", "SELECT 1")
	if code != http.StatusBadGateway || e["error"] == "" {
		t.Fatalf("%d %v", code, e)
	}
}

func TestExecCancelStopsTheServer(t *testing.T) {
	f := newFixture(t, store.Profile{Name: "dev"})
	f.login()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		f.s.Handler().ServeHTTP(httptest.NewRecorder(), f.request(ctx, "POST", "/api/conns/dev/exec", map[string]any{"tab": "t1", "sql": "SELECT SLEEP(100)"}))
		close(done)
	}()
	for !f.db.ran("SLEEP") {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("exec did not stop")
	}
	f.db.mu.Lock()
	defer f.db.mu.Unlock()
	if !f.db.stopped {
		t.Fatal("the statement kept running")
	}
}

func TestExecBusyTab(t *testing.T) {
	f := newFixture(t, store.Profile{Name: "dev"})
	f.login()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		f.s.Handler().ServeHTTP(httptest.NewRecorder(), f.request(ctx, "POST", "/api/conns/dev/exec", map[string]any{"tab": "t1", "sql": "SELECT SLEEP(100)"}))
		close(done)
	}()
	for !f.db.ran("SLEEP") {
		time.Sleep(5 * time.Millisecond)
	}
	if code, _, _ := f.exec("dev", "t1", "SELECT 1"); code != http.StatusConflict {
		t.Fatalf("busy tab: %d", code)
	}
	cancel()
	<-done
}

type planOut struct {
	ID    string `json:"id"`
	Steps []struct {
		Preview    string `json:"preview"`
		ExpectRows int64  `json:"expectRows"`
	} `json:"steps"`
	Atomic bool   `json:"atomic"`
	Error  string `json:"error"`
	Code   string `json:"code"`
}

func editItems(name string) map[string]any {
	return map[string]any{"ref": items, "changes": []map[string]any{{
		"key": []any{"1"}, "old": map[string]any{"name": "name1"}, "set": map[string]any{"name": name},
	}}}
}

func TestPlanAndApply(t *testing.T) {
	f := newFixture(t, store.Profile{Name: "dev"})
	f.login()
	var p planOut
	if code := f.call("POST", "/api/conns/dev/plan", editItems("renamed"), &p); code != 200 || len(p.Steps) != 1 || p.ID == "" || !p.Atomic {
		t.Fatalf("%d %+v", code, p)
	}
	if !strings.Contains(p.Steps[0].Preview, "'renamed'") || p.Steps[0].ExpectRows != 1 {
		t.Fatalf("preview %+v", p.Steps)
	}
	if f.db.ran("UPDATE") {
		t.Fatal("planning ran the update")
	}
	if code := f.call("POST", "/api/conns/dev/apply", map[string]any{"id": p.ID}, nil); code != http.StatusNoContent {
		t.Fatalf("apply: %d", code)
	}
	if f.db.rows[0][1] != "renamed" {
		t.Fatalf("row %v", f.db.rows[0])
	}
	if code := f.call("POST", "/api/conns/dev/apply", map[string]any{"id": p.ID}, nil); code != http.StatusNotFound {
		t.Fatalf("applied twice: %d", code)
	}
}

func TestApplyConflict(t *testing.T) {
	f := newFixture(t, store.Profile{Name: "dev"})
	f.login()
	f.db.conflict = true
	var p planOut
	f.call("POST", "/api/conns/dev/plan", editItems("x"), &p)
	var e planOut
	if code := f.call("POST", "/api/conns/dev/apply", map[string]any{"id": p.ID}, &e); code != http.StatusConflict || e.Code != "conflict" {
		t.Fatalf("%d %+v", code, e)
	}
	if !f.db.ran("ROLLBACK") {
		t.Fatal("no rollback")
	}
}

func TestApplyOnProductionNeedsConfirm(t *testing.T) {
	f := newFixture(t, store.Profile{Name: "prod", Env: "production"})
	f.login()
	var p planOut
	f.call("POST", "/api/conns/prod/plan", editItems("x"), &p)
	var e planOut
	if code := f.call("POST", "/api/conns/prod/apply", map[string]any{"id": p.ID}, &e); code != http.StatusConflict || e.Code != "confirm" {
		t.Fatalf("%d %+v", code, e)
	}
	if code := f.call("POST", "/api/conns/prod/apply", map[string]any{"id": p.ID, "confirm": true}, nil); code != http.StatusNoContent {
		t.Fatalf("confirmed: %d", code)
	}
}

func TestPlanBelongsToItsProfile(t *testing.T) {
	f := newFixture(t, store.Profile{Name: "dev"}, store.Profile{Name: "other"})
	f.login()
	var p planOut
	f.call("POST", "/api/conns/dev/plan", editItems("x"), &p)
	if code := f.call("POST", "/api/conns/other/apply", map[string]any{"id": p.ID}, nil); code != http.StatusNotFound {
		t.Fatalf("%d", code)
	}
}

func TestPlanRefusesTableWithoutKey(t *testing.T) {
	f := newFixture(t, store.Profile{Name: "dev"})
	f.login()
	body := editItems("x")
	body["ref"] = []map[string]string{{"kind": "schema", "name": "app"}, {"kind": "view", "name": "report"}}
	if code := f.call("POST", "/api/conns/dev/plan", body, nil); code != http.StatusBadRequest {
		t.Fatalf("%d", code)
	}
}

func TestFileFieldsOnlyOnLoopback(t *testing.T) {
	body := map[string]any{"tile": "fake", "values": map[string]string{"host": "h", "ca": "/etc/ssl/ca.pem"}}
	local := newFixture(t)
	local.login()
	if code := local.call("PUT", "/api/profiles/x", body, nil); code != 200 {
		t.Fatalf("loopback: %d", code)
	}
	remote := newFixtureAt(t, Config{Addr: "0.0.0.0:0", BehindProxy: true, AllowedHosts: []string{"127.0.0.1"}})
	remote.login()
	var e map[string]any
	if code := remote.call("PUT", "/api/profiles/x", body, &e); code != http.StatusBadRequest {
		t.Fatalf("remote: %d %v", code, e)
	}
}

// A layout may leave field kinds unset, as the SQL tiles do; values must still be values.
func TestCellValueWithoutKindIsNotNull(t *testing.T) {
	var unset tile.ValueKind
	if v := (Cell{Text: "2"}).value(unset); v.Kind == tile.KindNull || string(v.Raw) != "2" {
		t.Fatalf("%+v", v)
	}
	if v := (Cell{Text: ""}).value(unset); v.Kind == tile.KindNull {
		t.Fatal("empty string became NULL")
	}
	if v := (Cell{Null: true}).value(tile.KindText); v.Kind != tile.KindNull {
		t.Fatal("null lost")
	}
}
