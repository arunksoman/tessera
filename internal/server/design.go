package server

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/arunksoman/tessera/internal/engine"
	"github.com/arunksoman/tessera/internal/ioformat"
	"github.com/arunksoman/tessera/tile"
)

const (
	// maxImport bounds an upload. Imports stream, so this guards the database, not memory.
	maxImport = 4 << 30
	// sqlExportRows is how many records each INSERT statement of an SQL export holds.
	sqlExportRows = 100
)

// --- JSON shapes for structure, in the API's camelCase ---

type fieldDefJSON struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Nullable  bool   `json:"nullable"`
	Default   string `json:"default"`
	Comment   string `json:"comment"`
	Primary   bool   `json:"primary"`
	Collation string `json:"collation"`
	Extra     string `json:"extra"`
}

type indexDefJSON struct {
	Name       string   `json:"name"`
	Fields     []string `json:"fields"`
	Unique     bool     `json:"unique"`
	Primary    bool     `json:"primary"`
	Constraint bool     `json:"constraint"`
	Method     string   `json:"method"`
	Definition string   `json:"definition"`
}

type relationJSON struct {
	Name         string   `json:"name"`
	Fields       []string `json:"fields"`
	Target       refJSON  `json:"target"`
	TargetFields []string `json:"targetFields"`
	OnUpdate     string   `json:"onUpdate"`
	OnDelete     string   `json:"onDelete"`
}

type triggerDefJSON struct {
	Name       string   `json:"name"`
	Timing     string   `json:"timing"`
	Events     []string `json:"events"`
	ForEach    string   `json:"forEach"`
	Condition  string   `json:"condition"`
	Action     string   `json:"action"`
	Definition string   `json:"definition"`
}

type structureJSON struct {
	Fields    []fieldDefJSON   `json:"fields"`
	Indexes   []indexDefJSON   `json:"indexes"`
	Relations []relationJSON   `json:"relations"`
	Triggers  []triggerDefJSON `json:"triggers"`
	// Actions are the relation actions the API accepts.
	Actions []string `json:"actions"`
}

type changeDefJSON struct {
	Op       string          `json:"op"`
	Name     string          `json:"name"`
	Field    *fieldDefJSON   `json:"field"`
	From     *fieldDefJSON   `json:"from"`
	Index    *indexDefJSON   `json:"index"`
	Relation *relationJSON   `json:"relation"`
	Trigger  *triggerDefJSON `json:"trigger"`
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func encodeStructure(s tile.Structure) structureJSON {
	out := structureJSON{Fields: []fieldDefJSON{}, Indexes: []indexDefJSON{}, Relations: []relationJSON{}, Triggers: []triggerDefJSON{},
		Actions: tile.RelationActions}
	for _, f := range s.Fields {
		out.Fields = append(out.Fields, fieldDefJSON(f))
	}
	for _, i := range s.Indexes {
		out.Indexes = append(out.Indexes, indexDefJSON{i.Name, orEmpty(i.Fields), i.Unique, i.Primary, i.Constraint, i.Method, i.Definition})
	}
	for _, r := range s.Relations {
		out.Relations = append(out.Relations, relationJSON{r.Name, orEmpty(r.Fields), encodeRef(r.Target), orEmpty(r.TargetFields), r.OnUpdate, r.OnDelete})
	}
	for _, t := range s.Triggers {
		out.Triggers = append(out.Triggers, triggerDefJSON{t.Name, t.Timing, orEmpty(t.Events), t.ForEach, t.Condition, t.Action, t.Definition})
	}
	return out
}

func (c changeDefJSON) change() tile.StructureChange {
	out := tile.StructureChange{Op: tile.StructureOp(c.Op), Name: c.Name}
	if c.Field != nil {
		f := tile.FieldDef(*c.Field)
		out.Field = &f
	}
	if c.From != nil {
		f := tile.FieldDef(*c.From)
		out.From = &f
	}
	if i := c.Index; i != nil {
		out.Index = &tile.IndexDef{Name: i.Name, Fields: i.Fields, Unique: i.Unique, Primary: i.Primary, Constraint: i.Constraint, Method: i.Method}
	}
	if r := c.Relation; r != nil {
		out.Relation = &tile.Relation{ForeignKey: tile.ForeignKey{Name: r.Name, Fields: r.Fields, Target: r.Target.ref(), TargetFields: r.TargetFields},
			OnUpdate: r.OnUpdate, OnDelete: r.OnDelete}
	}
	if t := c.Trigger; t != nil {
		out.Trigger = &tile.TriggerDef{Name: t.Name, Timing: t.Timing, Events: t.Events, ForEach: t.ForEach, Condition: t.Condition, Action: t.Action}
	}
	return out
}

// --- structure ---

func (s *Server) structure(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Ref refJSON `json:"ref"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	conn, release, err := s.api.acquire(r)
	if err != nil {
		fail(w, err)
		return
	}
	defer release()
	st, err := conn.Structure(r.Context(), in.Ref.ref())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, encodeStructure(st))
}

func (s *Server) createScript(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Ref refJSON `json:"ref"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	conn, release, err := s.api.acquire(r)
	if err != nil {
		fail(w, err)
		return
	}
	defer release()
	script, err := conn.CreateScript(r.Context(), in.Ref.ref())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"script": script})
}

type planStepJSON struct {
	Preview    string `json:"preview"`
	ExpectRows int64  `json:"expectRows"`
	// Risk and Reason are the safety gate's view of the step, so the preview can warn.
	Risk   string `json:"risk"`
	Reason string `json:"reason,omitempty"`
}

type planJSON struct {
	ID     string         `json:"id"`
	Steps  []planStepJSON `json:"steps"`
	Atomic bool           `json:"atomic"`
}

// keepPlan stores a plan for apply and describes it.
func (a *api) keepPlan(r *http.Request, conn *engine.Conn, p *engine.Pending) planJSON {
	id := randomString()
	a.mu.Lock()
	a.plans[id] = &pendingPlan{session: sessionOf(r), profile: r.PathValue("name"), p: p, created: time.Now()}
	a.mu.Unlock()
	out := planJSON{ID: id, Steps: []planStepJSON{}, Atomic: p.Plan.Atomicity == tile.AtomicAll}
	for _, st := range p.Plan.Steps {
		c := conn.Classify(st.Request.Text)
		out.Steps = append(out.Steps, planStepJSON{st.Preview, st.ExpectRows, riskNames[c.Risk], c.Reason})
	}
	return out
}

// planStructure previews structure changes. The browser applies the plan with /apply, like
// edits, so the text previewed is the text run.
func (s *Server) planStructure(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Ref     refJSON         `json:"ref"`
		Changes []changeDefJSON `json:"changes"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if len(in.Changes) == 0 {
		fail(w, badf("no changes"))
		return
	}
	conn, release, err := s.api.acquire(r)
	if err != nil {
		fail(w, err)
		return
	}
	defer release()
	changes := make([]tile.StructureChange, len(in.Changes))
	for i, c := range in.Changes {
		changes[i] = c.change()
	}
	p, err := conn.PlanStructure(in.Ref.ref(), changes)
	if err != nil {
		if !errors.Is(err, engine.ErrUnsupported) {
			err = badRequest{err}
		}
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.api.keepPlan(r, conn, p))
}

// --- export ---

var exportTypes = map[string]struct{ ext, mime string }{
	"csv":   {"csv", "text/csv; charset=utf-8"},
	"json":  {"json", "application/json"},
	"jsonl": {"jsonl", "application/x-ndjson"},
	"sql":   {"sql", "application/sql; charset=utf-8"},
}

// attachment names a download, with an ASCII fallback for old clients.
func attachment(name string) string {
	ascii := strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' || r == '/' {
			return '_'
		}
		return r
	}, name)
	return mime.FormatMediaType("attachment", map[string]string{"filename": ascii}) + "; filename*=UTF-8''" + url.PathEscape(name)
}

// export streams every record of an object as a download. It is a GET so the browser saves
// it in the background with its own progress, and reads only, on a read-only session. A
// cross-site request never carries the SameSite=Strict cookie; Sec-Fetch-Site is checked too.
func (s *Server) export(w http.ResponseWriter, r *http.Request) {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		writeError(w, http.StatusForbidden, "forbidden", "cross-site export refused")
		return
	}
	q := r.URL.Query()
	var ref refJSON
	if err := json.Unmarshal([]byte(q.Get("ref")), &ref); err != nil || len(ref) == 0 {
		fail(w, badf("ref must be a JSON object path"))
		return
	}
	format := q.Get("format")
	typ, ok := exportTypes[format]
	if !ok {
		fail(w, badf("format must be csv, json, jsonl, or sql"))
		return
	}
	conn, release, err := s.api.acquire(r)
	if err != nil {
		fail(w, err)
		return
	}
	defer release()
	if format == "sql" && !conn.CanInsertScript() {
		fail(w, engine.ErrUnsupported)
		return
	}
	target := ref.ref()
	lw := &lazyWriter{w: w, start: func() {
		h := w.Header()
		h.Set("Content-Type", typ.mime)
		h.Set("Content-Disposition", attachment(ref[len(ref)-1].Name+"."+typ.ext))
		h.Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
	}}
	bw := bufio.NewWriterSize(lw, 64<<10)
	var out ioformat.Writer
	switch format {
	case "csv":
		out = ioformat.NewCSV(bw, q.Get("neutralize") != "0")
	case "json":
		out = ioformat.NewJSON(bw)
	case "jsonl":
		out = ioformat.NewJSONL(bw)
	case "sql":
		out = ioformat.NewSQL(bw, func(fields []string, rows [][]tile.Value) (string, error) {
			return conn.InsertScript(target, fields, rows)
		}, sqlExportRows)
	}
	width := 0
	err = conn.Export(r.Context(), target, func(f tile.Frame) error {
		switch {
		case f.Schema != nil:
			width = len(f.Schema.Fields)
			return out.Begin(f.Schema.Fields)
		case f.Batch != nil && width > 0:
			for i := range f.Batch.Rows {
				if err := out.Row(f.Batch.Cells[i*width : (i+1)*width]); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err == nil && width == 0 {
		err = out.Begin(nil) // an object with no fields still makes a valid empty file
	}
	if err == nil {
		err = out.End()
	}
	if err == nil {
		err = bw.Flush()
	}
	if err == nil {
		lw.ensure()
		return
	}
	if !lw.started {
		fail(w, err)
		return
	}
	// The status is sent, so abort the response: a cut-off download must not look complete.
	panic(http.ErrAbortHandler)
}

// lazyWriter sends headers on the first write, so an error before any data still gets a
// proper status.
type lazyWriter struct {
	w       io.Writer
	start   func()
	started bool
}

func (l *lazyWriter) ensure() {
	if !l.started {
		l.started = true
		l.start()
	}
}

func (l *lazyWriter) Write(p []byte) (int, error) {
	l.ensure()
	return l.w.Write(p)
}

// --- import ---

var importTypes = map[string]string{"text/csv": "csv", "application/json": "json", "application/x-ndjson": "jsonl"}

// importRecords adds records from the request body, which is the file itself: CSV with a
// header row, a JSON array of objects, or JSON Lines. Every field must exist in the object.
// The browser confirms first, so the gate's confirmation comes from the confirm parameter.
func (s *Server) importRecords(w http.ResponseWriter, r *http.Request) {
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	format, ok := importTypes[mt]
	if !ok {
		writeError(w, http.StatusUnsupportedMediaType, "bad_request", "send text/csv, application/json, or application/x-ndjson")
		return
	}
	q := r.URL.Query()
	var ref refJSON
	if err := json.Unmarshal([]byte(q.Get("ref")), &ref); err != nil || len(ref) == 0 {
		fail(w, badf("ref must be a JSON object path"))
		return
	}
	conn, release, err := s.api.acquire(r)
	if err != nil {
		fail(w, err)
		return
	}
	defer release()
	target := ref.ref()
	l, err := conn.Layout(r.Context(), target)
	if err != nil {
		fail(w, err)
		return
	}
	body := http.MaxBytesReader(w, r.Body, maxImport)
	var rd recordReader
	if format == "csv" {
		rd, err = newCSVReader(body, l, q.Get("emptyNull") != "0")
	} else {
		rd, err = newJSONReader(body, l, format == "jsonl")
	}
	if err != nil {
		fail(w, err)
		return
	}
	confirmed := q.Get("confirm") == "1"
	n, err := conn.Load(r.Context(), target, rd.fields(), rd.next, engine.RunOptions{Confirm: func(engine.Prompt) bool { return confirmed }})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"rows": n})
}

type recordReader interface {
	fields() []string
	next() ([]tile.Value, error)
}

// checkFields resolves names against the layout, refusing unknown and repeated ones.
func checkFields(l tile.Layout, names []string) ([]tile.ValueKind, error) {
	if len(names) == 0 {
		return nil, badf("the file names no fields")
	}
	kinds := make([]tile.ValueKind, len(names))
	for i, n := range names {
		kind, ok := kindOf(l, n)
		if !ok {
			return nil, badf("%q is not a field of this object", n)
		}
		if slices.Contains(names[:i], n) {
			return nil, badf("field %q appears twice", n)
		}
		if kind == tile.KindNull {
			kind = tile.KindText
		}
		kinds[i] = kind
	}
	return kinds, nil
}

type csvReader struct {
	r         *csv.Reader
	names     []string
	kinds     []tile.ValueKind
	emptyNull bool
	line      int
}

func newCSVReader(body io.Reader, l tile.Layout, emptyNull bool) (*csvReader, error) {
	r := csv.NewReader(body)
	header, err := r.Read()
	if errors.Is(err, io.EOF) {
		return nil, badf("the file is empty")
	}
	if err != nil {
		return nil, badf("reading the header: %v", err)
	}
	if len(header) > 0 {
		header[0] = strings.TrimPrefix(header[0], "\ufeff") // a byte order mark from spreadsheets
	}
	kinds, err := checkFields(l, header)
	if err != nil {
		return nil, err
	}
	return &csvReader{r: r, names: header, kinds: kinds, emptyNull: emptyNull, line: 1}, nil
}

func (c *csvReader) fields() []string { return c.names }

func (c *csvReader) next() ([]tile.Value, error) {
	rec, err := c.r.Read()
	if errors.Is(err, io.EOF) {
		return nil, io.EOF
	}
	c.line++
	if err != nil {
		return nil, badf("CSV: %v", err)
	}
	out := make([]tile.Value, len(rec))
	for i, v := range rec {
		switch {
		case v == "" && (c.emptyNull || c.kinds[i] != tile.KindText):
			out[i] = tile.Value{Kind: tile.KindNull}
		case !utf8.ValidString(v):
			return nil, badf("CSV record %d: field %q is not UTF-8", c.line, c.names[i])
		default:
			out[i] = tile.Value{Kind: c.kinds[i], Raw: []byte(v)}
		}
	}
	return out, nil
}

// jsonReader reads objects from a JSON array, or one after another for JSON Lines. Fields
// are the keys of the first object, in order; a later object may leave keys out, which then
// load as NULL.
type jsonReader struct {
	dec    *json.Decoder
	l      tile.Layout
	names  []string
	kinds  []tile.ValueKind
	array  bool
	first  []tile.Value
	record int
}

func newJSONReader(body io.Reader, l tile.Layout, lines bool) (*jsonReader, error) {
	dec := json.NewDecoder(body)
	dec.UseNumber()
	j := &jsonReader{dec: dec, l: l, array: !lines}
	if j.array {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil, badf("the file is empty")
		}
		if d, ok := tok.(json.Delim); err != nil || !ok || d != '[' {
			return nil, badf("a JSON import is an array of objects")
		}
	}
	keys, vals, err := j.object()
	if errors.Is(err, io.EOF) {
		return nil, badf("the file holds no records")
	}
	if err != nil {
		return nil, err
	}
	if j.kinds, err = checkFields(l, keys); err != nil {
		return nil, err
	}
	j.names = keys
	j.first = make([]tile.Value, len(keys))
	for i := range keys {
		j.first[i] = j.value(i, vals[i])
	}
	return j, nil
}

func (j *jsonReader) fields() []string { return j.names }

// object reads the next object's keys and raw values in order.
func (j *jsonReader) object() ([]string, []json.RawMessage, error) {
	if j.array && !j.dec.More() {
		if _, err := j.dec.Token(); err != nil { // the closing bracket
			return nil, nil, badf("JSON: %v", err)
		}
		return nil, nil, io.EOF
	}
	tok, err := j.dec.Token()
	if errors.Is(err, io.EOF) {
		if j.array {
			return nil, nil, badf("JSON: the array is not closed")
		}
		return nil, nil, io.EOF
	}
	j.record++
	if d, ok := tok.(json.Delim); err != nil || !ok || d != '{' {
		return nil, nil, badf("JSON record %d is not an object", j.record)
	}
	var keys []string
	var vals []json.RawMessage
	for j.dec.More() {
		tok, err := j.dec.Token()
		if err != nil {
			return nil, nil, badf("JSON record %d: %v", j.record, err)
		}
		var raw json.RawMessage
		if err := j.dec.Decode(&raw); err != nil {
			return nil, nil, badf("JSON record %d: %v", j.record, err)
		}
		keys = append(keys, tok.(string))
		vals = append(vals, raw)
	}
	if _, err := j.dec.Token(); err != nil {
		return nil, nil, badf("JSON record %d: %v", j.record, err)
	}
	return keys, vals, nil
}

// value turns a JSON value into a field value: strings as their text, numbers exactly as
// written, objects and arrays as JSON text.
func (j *jsonReader) value(i int, raw json.RawMessage) tile.Value {
	raw = bytes.TrimSpace(raw)
	switch {
	case string(raw) == "null":
		return tile.Value{Kind: tile.KindNull}
	case len(raw) > 0 && raw[0] == '"':
		var s string
		json.Unmarshal(raw, &s)
		return tile.Value{Kind: j.kinds[i], Raw: []byte(s)}
	}
	return tile.Value{Kind: j.kinds[i], Raw: append([]byte(nil), raw...)}
}

func (j *jsonReader) next() ([]tile.Value, error) {
	if j.first != nil {
		out := j.first
		j.first = nil
		return out, nil
	}
	keys, vals, err := j.object()
	if err != nil {
		return nil, err
	}
	out := make([]tile.Value, len(j.names))
	for i := range out {
		out[i] = tile.Value{Kind: tile.KindNull}
	}
	for k, key := range keys {
		i := slices.Index(j.names, key)
		if i < 0 {
			if _, known := kindOf(j.l, key); known {
				return nil, badf("JSON record %d has field %q, which the first record lacks", j.record, key)
			}
			return nil, badf("JSON record %d: %q is not a field of this object", j.record, key)
		}
		out[i] = j.value(i, vals[k])
	}
	return out, nil
}
