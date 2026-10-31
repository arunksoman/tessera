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

// Form describes a data-driven form rendered by every front-end.
type Form struct{ Fields []FormField }

type FormField struct {
	Name, Label, Type string // text, int, bool, enum, secret, file
	Required          bool
	Options           []string
	Default           string
}
