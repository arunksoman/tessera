package server

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/arunksoman/tessera/internal/engine"
	"github.com/arunksoman/tessera/internal/store"
)

var itemsRef = []map[string]string{{"kind": "schema", "name": "app"}, {"kind": "table", "name": "items"}}

func TestStructureAPI(t *testing.T) {
	f := newFixture(t, store.Profile{Name: "dev"})
	f.login()
	var out struct {
		Fields []struct {
			Name, Type, Comment string
			Nullable, Primary   bool
		}
		Indexes []struct {
			Name                string
			Fields              []string
			Primary, Constraint bool
		}
		Triggers []struct {
			Name, ForEach string
			Events        []string
		}
		Relations []any
		Actions   []string
	}
	if code := f.call("POST", "/api/conns/dev/structure", map[string]any{"ref": itemsRef}, &out); code != 200 {
		t.Fatal(code)
	}
	if len(out.Fields) != 2 || !out.Fields[0].Primary || out.Fields[1].Comment != "who" || !out.Fields[1].Nullable {
		t.Fatalf("fields = %+v", out.Fields)
	}
	if len(out.Indexes) != 1 || !out.Indexes[0].Constraint || out.Indexes[0].Fields[0] != "id" {
		t.Fatalf("indexes = %+v", out.Indexes)
	}
	if len(out.Triggers) != 1 || out.Triggers[0].ForEach != "ROW" || out.Relations == nil || len(out.Actions) != 5 {
		t.Fatalf("%+v", out)
	}
	var script struct{ Script string }
	if code := f.call("POST", "/api/conns/dev/script", map[string]any{"ref": itemsRef}, &script); code != 200 || !strings.HasPrefix(script.Script, "CREATE TABLE") {
		t.Fatalf("%d %+v", code, script)
	}
}

type structPlanOut struct {
	ID    string
	Steps []struct {
		Preview, Risk, Reason string
		ExpectRows            int64
	}
	Atomic bool
}

func TestStructurePlanAndApply(t *testing.T) {
	f := newFixture(t, store.Profile{Name: "dev"})
	f.login()
	var p structPlanOut
	changes := []map[string]any{
		{"op": "field.add", "field": map[string]any{"name": "qty", "type": "int", "default": "0"}},
		{"op": "trigger.add", "trigger": map[string]any{"name": "t", "timing": "AFTER", "events": []string{"INSERT"}, "forEach": "ROW", "action": "EXECUTE FUNCTION f()"}},
	}
	if code := f.call("POST", "/api/conns/dev/structure/plan", map[string]any{"ref": itemsRef, "changes": changes}, &p); code != 200 {
		t.Fatal(code)
	}
	if !p.Atomic || len(p.Steps) != 2 || p.Steps[0].Preview != `ALTER TABLE "app"."items" ADD COLUMN "qty" int NOT NULL DEFAULT 0` ||
		p.Steps[0].Risk != "write" || p.Steps[0].ExpectRows != -1 || !strings.HasPrefix(p.Steps[1].Preview, `CREATE TRIGGER "t" AFTER INSERT`) {
		t.Fatalf("plan = %+v", p)
	}
	if f.db.ran("ALTER") {
		t.Fatal("planning ran a statement")
	}
	if code := f.call("POST", "/api/conns/dev/apply", map[string]any{"id": p.ID, "confirm": false}, nil); code != http.StatusNoContent {
		t.Fatalf("apply: %d", code)
	}
	if !f.db.ran(`ADD COLUMN "qty"`) || !f.db.ran("CREATE TRIGGER") {
		t.Fatalf("executed %q", f.db.executed)
	}

	// Dropping a table is destructive: the preview says so and apply needs confirmation.
	if code := f.call("POST", "/api/conns/dev/structure/plan", map[string]any{"ref": itemsRef, "changes": []map[string]any{{"op": "drop"}}}, &p); code != 200 {
		t.Fatal(code)
	}
	if p.Steps[0].Risk != "destructive" || p.Steps[0].Reason == "" {
		t.Fatalf("plan = %+v", p)
	}
	if code := f.call("POST", "/api/conns/dev/apply", map[string]any{"id": p.ID, "confirm": false}, nil); code != http.StatusConflict {
		t.Fatalf("unconfirmed drop: %d", code)
	}
	if f.db.ran("DROP TABLE") {
		t.Fatal("dropped without confirmation")
	}
	if code := f.call("POST", "/api/conns/dev/apply", map[string]any{"id": p.ID, "confirm": true}, nil); code != http.StatusNoContent {
		t.Fatalf("confirmed drop: %d", code)
	}
	if !f.db.ran(`DROP TABLE "app"."items"`) {
		t.Fatalf("executed %q", f.db.executed)
	}
}

func TestStructurePlanRefusesBadInput(t *testing.T) {
	f := newFixture(t, store.Profile{Name: "dev"})
	f.login()
	for _, changes := range [][]map[string]any{
		nil,
		{{"op": "field.add", "field": map[string]any{"name": "a", "type": "int; DROP TABLE x"}}},
		{{"op": "vacuum"}},
	} {
		var e map[string]any
		if code := f.call("POST", "/api/conns/dev/structure/plan", map[string]any{"ref": itemsRef, "changes": changes}, &e); code != http.StatusBadRequest {
			t.Fatalf("%v: %d %v", changes, code, e)
		}
	}
}

func TestStructureApplyOnReadOnly(t *testing.T) {
	f := newFixture(t, store.Profile{Name: "ro", ReadOnly: true})
	f.login()
	var p structPlanOut
	if code := f.call("POST", "/api/conns/ro/structure/plan", map[string]any{"ref": itemsRef, "changes": []map[string]any{{"op": "truncate"}}}, &p); code != 200 {
		t.Fatal(code)
	}
	if code := f.call("POST", "/api/conns/ro/apply", map[string]any{"id": p.ID, "confirm": true}, nil); code != http.StatusForbidden {
		t.Fatalf("read-only: %d", code)
	}
}

func (f *fixture) raw(method, path, contentType, body string, header ...string) *httptest.ResponseRecorder {
	f.t.Helper()
	req := f.request(context.Background(), method, path, nil)
	req.Body = http.NoBody
	if body != "" {
		req = httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
		req.Host = "127.0.0.1:7000"
		for _, c := range f.cookies {
			req.AddCookie(c)
		}
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(w, req)
	return w
}

func exportPath(profile, table, format string) string {
	ref, _ := json.Marshal([]map[string]string{{"kind": "schema", "name": "app"}, {"kind": "table", "name": table}})
	return "/api/conns/" + profile + "/export?format=" + format + "&ref=" + url.QueryEscape(string(ref))
}

func TestExportFormats(t *testing.T) {
	f := newFixture(t, store.Profile{Name: "dev"})
	f.login()
	w := f.raw("GET", exportPath("dev", "items", "csv"), "", "")
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/csv; charset=utf-8" ||
		!strings.Contains(w.Header().Get("Content-Disposition"), `attachment; filename=items.csv`) {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
	recs, err := csv.NewReader(w.Body).ReadAll()
	if err != nil || len(recs) != 251 || strings.Join(recs[0], ",") != "id,name" || recs[250][1] != "name250" {
		t.Fatalf("%d records, %v", len(recs), err)
	}

	w = f.raw("GET", exportPath("dev", "items", "json"), "", "")
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil || len(rows) != 250 {
		t.Fatalf("json: %d rows, %v", len(rows), err)
	}

	w = f.raw("GET", exportPath("dev", "items", "jsonl"), "", "")
	sc := bufio.NewScanner(w.Body)
	n := 0
	for sc.Scan() {
		var row map[string]any
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		n++
	}
	if n != 250 || w.Header().Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("jsonl: %d lines", n)
	}

	w = f.raw("GET", exportPath("dev", "items", "sql"), "", "")
	body := w.Body.String()
	if strings.Count(body, `INSERT INTO "app"."items" ("id", "name") VALUES`) != 3 || !strings.Contains(body, "(250, 'name250');") {
		t.Fatalf("sql: %.300s", body)
	}

	// A store paged by a server cursor is exported page by page.
	w = f.raw("GET", exportPath("dev", "keys", "jsonl"), "", "")
	if got := strings.Count(w.Body.String(), "\n"); got != 250 {
		t.Fatalf("cursor-paged export: %d lines", got)
	}
}

func TestExportRefuses(t *testing.T) {
	f := newFixture(t, store.Profile{Name: "dev"})
	if w := f.raw("GET", exportPath("dev", "items", "csv"), "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("no session: %d", w.Code)
	}
	f.login()
	if w := f.raw("GET", exportPath("dev", "items", "csv"), "", "", "Sec-Fetch-Site", "cross-site"); w.Code != http.StatusForbidden {
		t.Fatalf("cross-site: %d", w.Code)
	}
	if w := f.raw("GET", exportPath("dev", "items", "xlsx"), "", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("format: %d", w.Code)
	}
	if w := f.raw("GET", "/api/conns/dev/export?format=csv&ref=x", "", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("ref: %d", w.Code)
	}
}

func importPath(profile string, params string) string {
	ref, _ := json.Marshal(itemsRef)
	return "/api/conns/" + profile + "/import?ref=" + url.QueryEscape(string(ref)) + params
}

func TestImportFormats(t *testing.T) {
	cases := []struct{ name, ctype, body string }{
		{"csv", "text/csv", string(rune(0xfeff)) + "id,name\n1,a\n2,\n"},
		{"json", "application/json", `[{"id": 1, "name": "a"}, {"id": 2}]`},
		{"jsonl", "application/x-ndjson", "{\"id\": 1, \"name\": \"a\"}\n{\"id\": 2, \"name\": null}\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, store.Profile{Name: "dev"})
			f.login()
			w := f.raw("POST", importPath("dev", ""), c.ctype, c.body)
			if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"rows":2}` {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			if !f.db.ran(`INSERT INTO "app"."items" ("id", "name") VALUES ($1, $2), ($3, $4)`) || !f.db.ran("COMMIT") {
				t.Fatalf("executed %q", f.db.executed)
			}
			got := []string{}
			for _, v := range f.db.inserted {
				if v.Raw == nil {
					got = append(got, "NULL")
				} else {
					got = append(got, string(v.Raw))
				}
			}
			if strings.Join(got, ",") != "1,a,2,NULL" {
				t.Fatalf("values %v", got)
			}
		})
	}
}

func TestImportRefuses(t *testing.T) {
	f := newFixture(t, store.Profile{Name: "dev"}, store.Profile{Name: "prod", Env: engine.EnvProduction}, store.Profile{Name: "ro", ReadOnly: true})
	f.login()
	cases := []struct {
		name, profile, params, ctype, body string
		code                               int
	}{
		{"unknown field", "dev", "", "text/csv", "id,nope\n1,a\n", http.StatusBadRequest},
		{"repeated field", "dev", "", "text/csv", "id,id\n1,2\n", http.StatusBadRequest},
		{"ragged CSV", "dev", "", "text/csv", "id,name\n1\n", http.StatusBadRequest},
		{"not an array", "dev", "", "application/json", `{"id": 1}`, http.StatusBadRequest},
		{"later unknown key", "dev", "", "application/x-ndjson", "{\"id\": 1}\n{\"id\": 2, \"x\": 3}\n", http.StatusBadRequest},
		{"empty", "dev", "", "text/csv", "", http.StatusBadRequest},
		{"plain text", "dev", "", "text/plain", "id\n1\n", http.StatusUnsupportedMediaType},
		{"production needs confirmation", "prod", "", "text/csv", "id,name\n1,a\n", http.StatusConflict},
		{"read-only", "ro", "&confirm=1", "text/csv", "id,name\n1,a\n", http.StatusForbidden},
	}
	for _, c := range cases {
		w := f.raw("POST", importPath(c.profile, c.params), c.ctype, c.body)
		if w.Code != c.code {
			t.Errorf("%s: %d %s", c.name, w.Code, w.Body)
		}
	}
	if f.db.ran("INSERT") {
		t.Fatalf("a refused import ran: %q", f.db.executed)
	}
	if w := f.raw("POST", importPath("prod", "&confirm=1"), "text/csv", "id,name\n1,a\n"); w.Code != 200 {
		t.Fatalf("confirmed production import: %d %s", w.Code, w.Body)
	}
	// A failure part way through adds nothing, since all batches share one transaction.
	f.db.executed = nil
	if w := f.raw("POST", importPath("dev", ""), "text/csv", "id,name\n1,a\n2\n"); w.Code != http.StatusBadRequest {
		t.Fatalf("ragged: %d", w.Code)
	}
	if f.db.ran("COMMIT") {
		t.Fatalf("executed %q", f.db.executed)
	}
}
