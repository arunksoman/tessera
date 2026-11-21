package sqlgen

import (
	"strconv"
	"strings"

	"github.com/arunksoman/tessera/tile"
)

// Stage lists one group of tree nodes in key order: at most n nodes whose key comes after the
// given one. A node's key is the name in the last segment of its ref.
type Stage func(after string, n int) ([]tile.Node, error)

// PageStages pages through groups of nodes listed one after another, such as tables before
// routines, behind one cursor. The cursor is the stage number and the last key read.
func PageStages(limit int, cursor string, stages ...Stage) ([]tile.Node, string, error) {
	if limit <= 0 {
		limit = 200
	}
	start, after := parseCursor(cursor)
	var out []tile.Node
	var from []int
	for i := start; i < len(stages) && len(out) <= limit; i++ {
		a := ""
		if i == start {
			a = after
		}
		nodes, err := stages[i](a, limit+1-len(out))
		if err != nil {
			return nil, "", err
		}
		for range nodes {
			from = append(from, i)
		}
		out = append(out, nodes...)
	}
	if len(out) <= limit {
		return out, "", nil
	}
	out = out[:limit]
	path := out[limit-1].Ref.Path
	return out, strconv.Itoa(from[limit-1]) + ":" + path[len(path)-1].Name, nil
}

// PageSlice pages through a short list already read in full, with the cursor as an offset.
func PageSlice(nodes []tile.Node, p tile.Page) ([]tile.Node, string, error) {
	limit := p.Limit
	if limit <= 0 {
		limit = 200
	}
	from, _ := strconv.Atoi(p.Cursor)
	from = max(0, min(from, len(nodes)))
	to := min(len(nodes), from+limit)
	next := ""
	if to < len(nodes) {
		next = strconv.Itoa(to)
	}
	return nodes[from:to], next, nil
}

// AddForeign adds one column of a foreign key, read in key order, to fks. The target is the
// table named in the tree as schema, then table.
func AddForeign(fks []tile.ForeignKey, name, field, schema, table, targetField string) []tile.ForeignKey {
	if n := len(fks); n == 0 || fks[n-1].Name != name {
		target := tile.ObjectRef{}.Child("schema", schema).Child("table", table)
		fks = append(fks, tile.ForeignKey{Name: name, Target: target})
	}
	fk := &fks[len(fks)-1]
	fk.Fields = append(fk.Fields, field)
	fk.TargetFields = append(fk.TargetFields, targetField)
	return fks
}

// parseCursor reads "stage:key". Anything else is a key in the first stage.
func parseCursor(c string) (int, string) {
	n, key, ok := strings.Cut(c, ":")
	if !ok {
		return 0, c
	}
	i, err := strconv.Atoi(n)
	if err != nil || i < 0 {
		return 0, c
	}
	return i, key
}
