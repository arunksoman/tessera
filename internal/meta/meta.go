// Package meta caches catalog data for completion, loading each part only when first needed.
package meta

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/arunksoman/tessera/tile"
)

const (
	pageSize  = 500
	maxTables = 20000 // bounds memory on a very large catalog
)

// Cache is safe for concurrent use.
type Cache struct {
	tree tile.ObjectTree
	ttl  time.Duration
	now  func() time.Time

	mu       sync.Mutex
	tables   []tile.ObjectName
	loadedAt time.Time
	columns  map[tile.ObjectName]columnEntry
}

type columnEntry struct {
	names    []string
	loadedAt time.Time
}

func New(tree tile.ObjectTree, ttl time.Duration) *Cache {
	return &Cache{tree: tree, ttl: ttl, now: time.Now, columns: map[tile.ObjectName]columnEntry{}}
}

// Invalidate drops everything, after DDL or on a manual refresh.
func (c *Cache) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tables, c.loadedAt = nil, time.Time{}
	c.columns = map[tile.ObjectName]columnEntry{}
}

// Catalog returns a view that loads missing data through ctx. Load failures read as empty,
// because completion must never fail a keystroke, and they are not cached.
func (c *Cache) Catalog(ctx context.Context) tile.Catalog { return catalog{c, ctx} }

type catalog struct {
	c   *Cache
	ctx context.Context
}

func (v catalog) Tables() []tile.ObjectName {
	v.c.mu.Lock()
	defer v.c.mu.Unlock()
	return slices.Clone(v.c.loadTables(v.ctx))
}

func (c *Cache) loadTables(ctx context.Context) []tile.ObjectName {
	if !c.loadedAt.IsZero() && c.now().Sub(c.loadedAt) < c.ttl {
		return c.tables
	}
	var out []tile.ObjectName
	roots, err := c.all(ctx, tile.ObjectRef{})
	if err != nil {
		return c.tables
	}
	for _, schema := range roots {
		nodes, err := c.all(ctx, schema.Ref)
		if err != nil {
			return c.tables
		}
		for _, n := range nodes {
			if n.Browsable && len(out) < maxTables {
				out = append(out, tile.ObjectName{Schema: schema.Label, Name: n.Label})
			}
		}
	}
	c.tables, c.loadedAt = out, c.now()
	return out
}

func (c *Cache) all(ctx context.Context, parent tile.ObjectRef) ([]tile.Node, error) {
	var out []tile.Node
	cursor := ""
	for {
		nodes, next, err := c.tree.Children(ctx, parent, tile.Page{Cursor: cursor, Limit: pageSize})
		if err != nil {
			return nil, err
		}
		out = append(out, nodes...)
		if next == "" || len(out) >= maxTables {
			return out, nil
		}
		cursor = next
	}
}

func (v catalog) Columns(name tile.ObjectName) []string {
	c := v.c
	c.mu.Lock()
	defer c.mu.Unlock()
	tables := c.loadTables(v.ctx)
	at := slices.IndexFunc(tables, func(t tile.ObjectName) bool {
		return t.Name == name.Name && (name.Schema == "" || t.Schema == name.Schema)
	})
	if at < 0 {
		return nil
	}
	full := tables[at]
	if e, ok := c.columns[full]; ok && c.now().Sub(e.loadedAt) < c.ttl {
		return e.names
	}
	ref := tile.ObjectRef{}.Child("schema", full.Schema).Child("table", full.Name)
	d, err := c.tree.Describe(v.ctx, ref)
	if err != nil {
		return nil
	}
	var names []string
	for _, s := range d.Sections {
		if s.Title == "Columns" {
			for _, r := range s.Rows {
				names = append(names, string(r[0].Raw))
			}
		}
	}
	c.columns[full] = columnEntry{names, c.now()}
	return names
}
