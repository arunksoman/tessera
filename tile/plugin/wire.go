// Package plugin runs a tile in its own process and talks to it over standard input and
// output. It imports only the standard library, like the tile package.
//
// A plugin author writes an ordinary tile and calls Serve from main:
//
//	func main() { plugin.Serve(mytile.Tile{}) }
//
// The protocol is newline-delimited JSON, so a plugin may also be written in another
// language. Each line is a request {"id","method","params"}, a response {"id","result"} or
// {"id","error"}, or {"cancel": id}. Either side may send requests, and ids are per sender.
// Tile types travel in their encoding/json form, so byte values are base64. The methods are
// the constants in this file. The protocol is v0, like the tile contract.
package plugin

import "github.com/arunksoman/tessera/tile"

// Methods the host calls on a plugin.
const (
	MHandshake       = "handshake"
	MConfigWarnings  = "tile.config_warnings"
	MOpen            = "tile.open"
	MConnCaps        = "conn.capabilities"
	MConnPing        = "conn.ping"
	MConnClose       = "conn.close"
	MConnSession     = "conn.session"
	MTreeChildren    = "tree.children"
	MTreeDescribe    = "tree.describe"
	MLangSplit       = "lang.split"
	MLangClassify    = "lang.classify"
	MLangRedact      = "lang.redact"
	MBrowseLayout    = "browser.layout"
	MBrowseRequest   = "browser.request"
	MEditorPlan      = "editor.plan"
	MExplainRequest  = "explainer.request"
	MSessionExecute  = "session.execute"
	MSessionPing     = "session.ping"
	MSessionClose    = "session.close"
	MStreamNext      = "stream.next"
	MStreamClose     = "stream.close"
	MHostSecret      = "host.secret" // the one method a plugin calls on the host
	protocolRevision = 1
)

// Facets lists the optional interfaces a tile, connection, or session implements, because a
// type assertion cannot cross a process boundary.
type Facets struct {
	ConfigWarner bool `json:"config_warner,omitempty"`
	Tree         bool `json:"tree,omitempty"`
	Browser      bool `json:"browser,omitempty"`
	Editor       bool `json:"editor,omitempty"`
	Explainer    bool `json:"explainer,omitempty"`
	Pinger       bool `json:"pinger,omitempty"`
}

type HandshakeRequest struct {
	APIVersion int `json:"api_version"`
	Protocol   int `json:"protocol"`
	// TessVersion is the host's version, for logs. The host itself checks the manifest's
	// MinTess and MaxTess.
	TessVersion string `json:"tess_version,omitempty"`
}

type HandshakeReply struct {
	Manifest tile.Manifest `json:"manifest"`
	Protocol int           `json:"protocol"`
	Facets   Facets        `json:"facets"`
}

type OpenRequest struct {
	Config tile.ConnConfig `json:"config"`
	// Env names this connection's environment, for host.secret calls.
	Env uint64 `json:"env"`
}

type OpenReply struct {
	Conn     uint64 `json:"conn"`
	Language string `json:"language"`
	Facets   Facets `json:"facets"`
}

type SecretRequest struct {
	Env  uint64 `json:"env"`
	Name string `json:"name"`
}

type SecretReply struct {
	Value []byte `json:"value"`
}

// Ref names a connection, session, or stream handle.
type Ref struct {
	ID uint64 `json:"id"`
}

type SessionRequest struct {
	Conn    uint64              `json:"conn"`
	Options tile.SessionOptions `json:"options"`
}

type SessionReply struct {
	Session uint64 `json:"session"`
	Facets  Facets `json:"facets"`
}

type ChildrenRequest struct {
	Conn   uint64         `json:"conn"`
	Parent tile.ObjectRef `json:"parent"`
	Page   tile.Page      `json:"page"`
}

type ChildrenReply struct {
	Nodes []tile.Node `json:"nodes"`
	Next  string      `json:"next"`
}

type RefRequest struct {
	Conn uint64         `json:"conn"`
	Ref  tile.ObjectRef `json:"ref"`
}

type TextRequest struct {
	Conn uint64 `json:"conn"`
	Text string `json:"text"`
}

type BrowseRequest struct {
	Conn   uint64           `json:"conn"`
	Ref    tile.ObjectRef   `json:"ref"`
	Layout tile.Layout      `json:"layout"`
	Query  tile.BrowseQuery `json:"query"`
}

type PlanRequest struct {
	Conn    uint64         `json:"conn"`
	Ref     tile.ObjectRef `json:"ref"`
	Layout  tile.Layout    `json:"layout"`
	Changes tile.ChangeSet `json:"changes"`
}

type ExplainRequest struct {
	Conn   uint64             `json:"conn"`
	Text   string             `json:"text"`
	Format tile.ExplainFormat `json:"format"`
}

type ExecuteRequest struct {
	Session uint64       `json:"session"`
	Request tile.Request `json:"request"`
}

type StreamReply struct {
	Stream uint64 `json:"stream"`
}
