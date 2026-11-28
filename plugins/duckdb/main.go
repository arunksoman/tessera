// Command tess-duckdb is the DuckDB tile for tess, served as a plugin. It is a separate binary
// because the DuckDB driver needs cgo, which tess itself does not allow. DuckDB is linked in
// statically, so the binary needs nothing else installed.
package main

import "github.com/arunksoman/tessera/tile/plugin"

// version and minTess are set at release with -ldflags: the plugin's own version, and the
// oldest tess release it was built to work with.
var (
	version = "dev"
	minTess = ""
)

func main() { plugin.Serve(Tile{}) }
