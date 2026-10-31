// Command tess is the Tessera database client.
package main

import (
	"os"

	"golang.org/x/term"

	"github.com/arunksoman/tessera/internal/cli"
	"github.com/arunksoman/tessera/internal/tiles/mysql"
	"github.com/arunksoman/tessera/internal/tiles/postgres"
	"github.com/arunksoman/tessera/tile"
)

// version is set at release with -ldflags.
var version = "dev"

func main() {
	app := &cli.App{
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
		Version: version,
		TTY:     term.IsTerminal(int(os.Stdin.Fd())),
		// The compiled-in tiles, listed here so what ships is visible in one place.
		Tiles: []tile.Tile{postgres.Tile{}, mysql.Tile{}},
	}
	os.Exit(app.Run(os.Args[1:]))
}
