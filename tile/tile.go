// Package tile is the plugin contract. It imports only the standard library.
//
// The contract is v0: it may change until NoSQL tiles have proven it. It covers relational,
// document, and key-value databases, each of which may be transactional (OLTP) or
// analytical (OLAP); nothing here assumes SQL.
//
// The JSON tags are the plugin wire format (package tile/plugin), so renaming a Go field
// never breaks a plugin. Change a tag only with the API version.
package tile

import (
	"context"
	"net"
)

const APIVersion = 0

// Tile is a plugin entry point.
type Tile interface {
	Manifest() Manifest
	Open(ctx context.Context, cfg ConnConfig, env Env) (Conn, error)
}

type Manifest struct {
	ID         string `json:"id"`   // "postgres"
	Name       string `json:"name"` // "PostgreSQL"
	APIVersion int    `json:"api_version"`
	Style      Style  `json:"style"`
	ConnForm   Form   `json:"conn_form"`
	// Version is a plugin's own semantic version. Built-in tiles leave it empty.
	Version string `json:"version,omitempty"`
	// MinTess and MaxTess bound the tess versions a plugin works with, both inclusive. Empty
	// means no bound. Prefer MinTess alone: a maximum blocks every later tess, including
	// releases that would have worked.
	MinTess string `json:"min_tess,omitempty"`
	MaxTess string `json:"max_tess,omitempty"`
}

type Style struct {
	Color string `json:"color,omitempty"`
	Glyph string `json:"glyph,omitempty"`
}

// Env is what the host lends a plugin. The host owns tunnels and secrets.
type Env struct {
	// Dial goes through the profile's SSH chain and dial policy. Nil means direct.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// Secret returns a named secret such as "password". It is empty if not set.
	Secret func(ctx context.Context, name string) ([]byte, error)
}

// ConnConfig holds validated form values. It never holds secrets.
type ConnConfig struct {
	Values map[string]string `json:"values"`
}

// Conn is a logical connection, usually a pool.
type Conn interface {
	Capabilities() Capabilities
	Ping(ctx context.Context) error
	Close() error
	Session(ctx context.Context, opt SessionOptions) (Session, error)
	Tree() ObjectTree
	Language() Language
}

// Capability names are namespaced strings. Unknown ones are ignored.
//
// A database that is analytical rather than transactional simply leaves out the tx
// capabilities: front-ends then show no transaction state, and edit plans run per item.
type Capability string

const (
	// CapTxMulti means a session can hold a multi-statement transaction.
	CapTxMulti Capability = "tx.multi"
	// CapTxDDL means statements with Classification.Schema run inside a transaction instead
	// of committing it.
	CapTxDDL    Capability = "tx.ddl"
	CapCancel   Capability = "exec.cancel"
	CapStream   Capability = "exec.stream"
	CapReadOnly Capability = "exec.readonly.server"
	// CapTTL means records can expire, as keys do in key-value stores.
	CapTTL Capability = "data.ttl"
)

type Capabilities map[Capability]CapInfo

// CapInfo says why a capability is limited, so the UI can explain rather than hide it.
type CapInfo struct {
	Supported bool   `json:"supported"`
	Note      string `json:"note,omitempty"`
}

// ObjectRef is a path of plugin-defined segments.
type ObjectRef struct {
	Path []Segment `json:"path"`
}

type Segment struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// Child returns a new ref one level below r.
func (r ObjectRef) Child(kind, name string) ObjectRef {
	path := make([]Segment, len(r.Path), len(r.Path)+1)
	copy(path, r.Path)
	return ObjectRef{Path: append(path, Segment{kind, name})}
}

type Node struct {
	Ref         ObjectRef `json:"ref"`
	Kind        string    `json:"kind"`
	Label       string    `json:"label"`
	HasChildren bool      `json:"has_children,omitempty"`
	Browsable   bool      `json:"browsable,omitempty"`
}

// Page requests part of a list. Children are always paginated.
type Page struct {
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit"`
}

type ObjectTree interface {
	// Children returns the next page, and a cursor that is empty on the last page.
	Children(ctx context.Context, parent ObjectRef, p Page) (nodes []Node, next string, err error)
	Describe(ctx context.Context, ref ObjectRef) (ObjectDetail, error)
}

type ObjectDetail struct {
	Sections []DetailSection `json:"sections"`
}

type DetailSection struct {
	Title  string    `json:"title"`
	Fields []Field   `json:"fields,omitempty"`
	Rows   [][]Value `json:"rows,omitempty"`
}

type SessionOptions struct {
	ReadOnly bool `json:"read_only,omitempty"`
}

// Session is one physical connection, so it holds transaction and SET state.
// A Session runs one statement at a time.
type Session interface {
	Execute(ctx context.Context, req Request) (ResultStream, error)
	Close() error
}

// Request carries opaque text in the plugin's language: a SQL statement, a key-value
// command line, or a JSON query document. Args are always bound, never spliced into Text.
type Request struct {
	Text string  `json:"text"`
	Args []Value `json:"args,omitempty"`
}

// Shape says how a result is best shown. Every shape still arrives as Fields and Batches, so
// any front-end can fall back to a table.
type Shape uint8

const (
	// ShapeTabular is rows of the declared fields.
	ShapeTabular Shape = iota
	// ShapeDocument is one record per document. Fields usually hold the document's key, then
	// the whole document as KindJSON, so documents with differing fields need no shared
	// schema.
	ShapeDocument
	ShapeTree
	// ShapeKeyValue is one record per key, with fields such as key, type, ttl, and value.
	// Structured values (hashes, lists, sets) are KindJSON.
	ShapeKeyValue
	ShapeScalar
)

// ResultStream is pull-based: not calling Next is the backpressure.
// Closing a stream early must stop the server's work promptly.
type ResultStream interface {
	Next(ctx context.Context) (Frame, error) // io.EOF after the last frame
	Close() error
}

// Frame has exactly one field set. Order: Schema, then Batches, then Done. A result with no
// records, such as a write, may send Done alone.
type Frame struct {
	Schema *ResultSchema  `json:"schema,omitempty"`
	Batch  *Batch         `json:"batch,omitempty"`
	Done   *ResultSummary `json:"done,omitempty"`
}

type ResultSchema struct {
	Shape  Shape   `json:"shape"`
	Fields []Field `json:"fields"`
}

type Field struct {
	Name       string    `json:"name"`
	NativeType string    `json:"native_type,omitempty"`
	Kind       ValueKind `json:"kind"`
}

// Batch has len(Fields)*Rows cells. Raw bytes are valid only until the next call to Next.
type Batch struct {
	Rows  int     `json:"rows"`
	Cells []Value `json:"cells"`
}

type ValueKind uint8

const (
	KindNull ValueKind = iota
	KindBool
	KindInt     // canonical decimal text, never a float: int64 must survive JavaScript
	KindDecimal // canonical decimal text
	KindFloat
	KindText
	KindBytes
	KindTime // RFC 3339 text, with as many fractional digits as the database keeps
	// KindJSON is JSON text. Documents, arrays, maps, and structured key-value values use
	// it; types JSON lacks, such as object IDs or 128-bit decimals, use canonical Extended
	// JSON so they stay lossless.
	KindJSON
	KindOther
)

// Value is canonical bytes, so the common path needs no decode and no re-encode.
type Value struct {
	Kind ValueKind `json:"kind"`
	Raw  []byte    `json:"raw,omitempty"`
}

// Text returns a Value holding s.
func Text(s string) Value { return Value{Kind: KindText, Raw: []byte(s)} }

type ResultSummary struct {
	RowsAffected int64  `json:"rows_affected,omitempty"`
	HasAffected  bool   `json:"has_affected,omitempty"`
	Tag          string `json:"tag,omitempty"`
	// Cursor continues a cursor-paged browse (Layout.Paging). Empty means the last page.
	Cursor string `json:"cursor,omitempty"`
}

type Risk uint8

const (
	RiskNone Risk = iota
	RiskWrite
	RiskDestructive
)

// Language handles a query language without running anything.
type Language interface {
	ID() string
	Split(text string) []Span
	Classify(stmt string) Classification
	Redact(stmt string) string
}

type Span struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// TxEffect says how a statement changes the session's transaction, so the engine can track
// it without knowing the language.
type TxEffect uint8

const (
	TxNone TxEffect = iota
	// TxBegin opens a transaction, or ends one and opens the next, as COMMIT AND CHAIN does.
	TxBegin
	// TxEnd commits or rolls back the open transaction, as COMMIT, ROLLBACK, EXEC, or
	// DISCARD do. Rolling back to a savepoint is TxNone.
	TxEnd
)

// Classification is a guardrail, not a security boundary: ReadOnly means no write was
// visible in the text, while a called function can still write. Only server enforcement
// or a read-only database role can guarantee it.
type Classification struct {
	Verb     string `json:"verb"`
	Risk     Risk   `json:"risk"`
	ReadOnly bool   `json:"read_only,omitempty"`
	Reason   string `json:"reason,omitempty"`
	// Tx is the statement's effect on an open transaction.
	Tx TxEffect `json:"tx,omitempty"`
	// Schema marks a statement that defines objects rather than data, such as DDL or
	// creating an index or collection. It refreshes the object tree and completion, and where
	// CapTxDDL is unsupported it ends an open transaction.
	Schema bool `json:"schema,omitempty"`
}

// Pinger is an optional Session facet: long-lived sessions check they are still alive.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Browser is an optional Conn facet for paging through one object's records.
type Browser interface {
	Layout(ctx context.Context, ref ObjectRef) (Layout, error)
	// BrowseRequest builds the request for one page. It does no I/O.
	BrowseRequest(ref ObjectRef, l Layout, q BrowseQuery) (Request, error)
}

// Paging says how a front-end asks for the next page of a browse.
type Paging uint8

const (
	// PagingKeyset sends the last record as BrowseQuery.After when Layout.Key is set, and
	// otherwise counts records read in BrowseQuery.Offset.
	PagingKeyset Paging = iota
	// PagingCursor sends ResultSummary.Cursor from the previous page as BrowseQuery.Cursor,
	// for stores that page with a server token, such as a key-value SCAN. A page may hold
	// fewer records than asked for while more remain; only an empty cursor ends the browse.
	PagingCursor
)

// Layout describes a browsable object.
type Layout struct {
	Fields []Field `json:"fields"`
	// Key names the fields that identify one record. Empty means records cannot be edited,
	// and Note says why.
	Key      []string `json:"key,omitempty"`
	Note     string   `json:"note,omitempty"`
	Estimate int64    `json:"estimate"` // approximate record count, negative if unknown
	Paging   Paging   `json:"paging,omitempty"`
	// Shape is how records are best shown, as in ResultSchema.
	Shape Shape `json:"shape,omitempty"`
}

type SortKey struct {
	Field string `json:"field"`
	Desc  bool   `json:"desc,omitempty"`
}

type FilterOp string

const (
	OpEq       FilterOp = "="
	OpNe       FilterOp = "!="
	OpLt       FilterOp = "<"
	OpLe       FilterOp = "<="
	OpGt       FilterOp = ">"
	OpGe       FilterOp = ">="
	OpContains FilterOp = "~"
	OpNull     FilterOp = "null"
	OpNotNull  FilterOp = "!null"
)

// Condition is one term of a typed filter. Conditions are ANDed. A tile that cannot apply
// an operator to a field returns an error from BrowseRequest rather than ignoring it.
type Condition struct {
	Field string   `json:"field"`
	Op    FilterOp `json:"op"`
	Value Value    `json:"value"`
}

// BrowseQuery selects one page. After is the last record of the previous page, aligned with
// Layout.Fields. Offset is the number of records already read, used only without a Key.
// Cursor is the previous page's ResultSummary.Cursor under PagingCursor.
type BrowseQuery struct {
	Sort   []SortKey   `json:"sort,omitempty"`
	Filter []Condition `json:"filter,omitempty"`
	After  []Value     `json:"after,omitempty"`
	Offset int64       `json:"offset,omitempty"`
	Cursor string      `json:"cursor,omitempty"`
	Limit  int         `json:"limit"`
}

// Editor is an optional Conn facet that turns intended edits into exact steps.
type Editor interface {
	// Plan has no side effects.
	Plan(ref ObjectRef, l Layout, cs ChangeSet) (Plan, error)
}

// Change sets fields on the record whose Key values match. Old holds the values the user saw
// for each field in Set, so a record changed by someone else is a conflict, not an overwrite.
// A document tile may treat a field name as a path into the document.
type Change struct {
	Key []Value          `json:"key"` // aligned with Layout.Key
	Old map[string]Value `json:"old,omitempty"`
	Set map[string]Value `json:"set"`
}

type ChangeSet struct {
	Changes []Change `json:"changes"`
}

type Atomicity uint8

const (
	AtomicAll Atomicity = iota
	AtomicPerItem
)

// Plan is what will run. Begin, Commit, and Rollback are empty when not supported, and then
// Atomicity is AtomicPerItem.
type Plan struct {
	Begin     Request    `json:"begin"`
	Commit    Request    `json:"commit"`
	Rollback  Request    `json:"rollback"`
	Steps     []PlanStep `json:"steps"`
	Atomicity Atomicity  `json:"atomicity"`
}

type PlanStep struct {
	Request    Request `json:"request"`
	Preview    string  `json:"preview"` // for reading only: values rendered as literals
	ExpectRows int64   `json:"expect_rows"`
}

// Explainer is an optional Conn facet. It builds a request that plans stmt without running it.
type Explainer interface {
	ExplainRequest(stmt string, format ExplainFormat) Request
}

type ExplainFormat uint8

const (
	ExplainText ExplainFormat = iota
	ExplainJSON
)

// ConfigWarner is an optional Tile facet that names weak settings in a connection config.
type ConfigWarner interface {
	ConfigWarnings(cfg ConnConfig) []string
}

// Completer is an optional Language facet.
type Completer interface {
	// Complete returns ranked candidates for the cursor, a byte offset into text. Prefix is
	// the text before the cursor that a chosen candidate replaces.
	Complete(text string, cursor int, cat Catalog) (prefix string, out []Candidate)
}

// Catalog is what a Completer may ask about the connected server.
type Catalog interface {
	// Tables lists tables and views visible to the user, each as a schema and a name.
	Tables() []ObjectName
	// Columns lists a table's columns. An empty schema means any schema.
	Columns(name ObjectName) []string
}

type ObjectName struct{ Schema, Name string }

type Candidate struct {
	Text string
	Kind string // column, table, schema, keyword, function
}

// Form describes a data-driven form rendered by every front-end.
type Form struct {
	Fields []FormField `json:"fields"`
}

type FormField struct {
	Name     string   `json:"name"`
	Label    string   `json:"label"`
	Type     string   `json:"type"` // text, int, bool, enum, secret, file
	Required bool     `json:"required,omitempty"`
	Options  []string `json:"options,omitempty"`
	Default  string   `json:"default,omitempty"`
}
