// Package tile is the plugin contract. It imports only the standard library.
//
// The contract is v0: it may change until NoSQL tiles have proven it.
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
	ID         string // "postgres"
	Name       string // "PostgreSQL"
	APIVersion int
	Style      Style
	ConnForm   Form
}

type Style struct{ Color, Glyph string }

// Env is what the host lends a plugin. The host owns tunnels and secrets.
type Env struct {
	// Dial goes through the profile's SSH chain and dial policy. Nil means direct.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// Secret returns a named secret such as "password". It is empty if not set.
	Secret func(ctx context.Context, name string) ([]byte, error)
}

// ConnConfig holds validated form values. It never holds secrets.
type ConnConfig struct {
	Values map[string]string
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
type Capability string

const (
	CapTxMulti  Capability = "tx.multi"
	CapTxDDL    Capability = "tx.ddl"
	CapCancel   Capability = "exec.cancel"
	CapStream   Capability = "exec.stream"
	CapReadOnly Capability = "exec.readonly.server"
)

type Capabilities map[Capability]CapInfo

// CapInfo says why a capability is limited, so the UI can explain rather than hide it.
type CapInfo struct {
	Supported bool
	Note      string
}

// ObjectRef is a path of plugin-defined segments.
type ObjectRef struct{ Path []Segment }

type Segment struct{ Kind, Name string }

// Child returns a new ref one level below r.
func (r ObjectRef) Child(kind, name string) ObjectRef {
	path := make([]Segment, len(r.Path), len(r.Path)+1)
	copy(path, r.Path)
	return ObjectRef{Path: append(path, Segment{kind, name})}
}

type Node struct {
	Ref         ObjectRef
	Kind, Label string
	HasChildren bool
	Browsable   bool
}

// Page requests part of a list. Children are always paginated.
type Page struct {
	Cursor string
	Limit  int
}

type ObjectTree interface {
	// Children returns the next page, and a cursor that is empty on the last page.
	Children(ctx context.Context, parent ObjectRef, p Page) (nodes []Node, next string, err error)
	Describe(ctx context.Context, ref ObjectRef) (ObjectDetail, error)
}

type ObjectDetail struct {
	Sections []DetailSection
}

type DetailSection struct {
	Title  string
	Fields []Field
	Rows   [][]Value
}

type SessionOptions struct {
	ReadOnly bool
}

// Session is one physical connection, so it holds transaction and SET state.
// A Session runs one statement at a time.
type Session interface {
	Execute(ctx context.Context, req Request) (ResultStream, error)
	Close() error
}

// Request carries opaque text in the plugin's language. Args are always bound.
type Request struct {
	Text string
	Args []Value
}

type Shape uint8

const (
	ShapeTabular Shape = iota
	ShapeDocument
	ShapeTree
	ShapeKeyValue
	ShapeScalar
)

// ResultStream is pull-based: not calling Next is the backpressure.
// Closing a stream early must stop the server's work promptly.
type ResultStream interface {
	Next(ctx context.Context) (Frame, error) // io.EOF after the last frame
	Close() error
}

// Frame has exactly one field set. Order: Schema, then Batches, then Done.
type Frame struct {
	Schema *ResultSchema
	Batch  *Batch
	Done   *ResultSummary
}

type ResultSchema struct {
	Shape  Shape
	Fields []Field
}

type Field struct {
	Name, NativeType string
	Kind             ValueKind
}

// Batch has len(Fields)*Rows cells. Raw bytes are valid only until the next call to Next.
type Batch struct {
	Rows  int
	Cells []Value
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
	KindTime
	KindJSON
	KindOther
)

// Value is canonical bytes, so the common path needs no decode and no re-encode.
type Value struct {
	Kind ValueKind
	Raw  []byte
}

// Text returns a Value holding s.
func Text(s string) Value { return Value{Kind: KindText, Raw: []byte(s)} }

type ResultSummary struct {
	RowsAffected int64
	HasAffected  bool
	Tag          string
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

type Span struct{ Start, End int }

// Classification is a guardrail, not a security boundary: ReadOnly means no write was
// visible in the text, while a called function can still write. Only server enforcement
// or a read-only database role can guarantee it.
type Classification struct {
	Verb     string
	Risk     Risk
	ReadOnly bool
	Reason   string
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

// Layout describes a browsable object.
type Layout struct {
	Fields []Field
	// Key names the fields that identify one record. Empty means records cannot be edited,
	// and Note says why.
	Key      []string
	Note     string
	Estimate int64 // approximate record count, negative if unknown
}

type SortKey struct {
	Field string
	Desc  bool
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

// Condition is one term of a typed filter. Conditions are ANDed.
type Condition struct {
	Field string
	Op    FilterOp
	Value Value
}

// BrowseQuery selects one page. After is the last record of the previous page, aligned with
// Layout.Fields. Offset is the number of records already read, used only without a Key.
type BrowseQuery struct {
	Sort   []SortKey
	Filter []Condition
	After  []Value
	Offset int64
	Limit  int
}

// Editor is an optional Conn facet that turns intended edits into exact steps.
type Editor interface {
	// Plan has no side effects.
	Plan(ref ObjectRef, l Layout, cs ChangeSet) (Plan, error)
}

// Change sets fields on the record whose Key values match. Old holds the values the user saw
// for each field in Set, so a record changed by someone else is a conflict, not an overwrite.
type Change struct {
	Key []Value // aligned with Layout.Key
	Old map[string]Value
	Set map[string]Value
}

type ChangeSet struct{ Changes []Change }

type Atomicity uint8

const (
	AtomicAll Atomicity = iota
	AtomicPerItem
)

// Plan is what will run. Begin, Commit, and Rollback are empty when not supported.
type Plan struct {
	Begin, Commit, Rollback Request
	Steps                   []PlanStep
	Atomicity               Atomicity
}

type PlanStep struct {
	Request    Request
	Preview    string // for reading only: values rendered as literals
	ExpectRows int64
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
type Form struct{ Fields []FormField }

type FormField struct {
	Name, Label, Type string // text, int, bool, enum, secret, file
	Required          bool
	Options           []string
	Default           string
}
