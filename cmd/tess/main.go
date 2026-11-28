// Command tess is the Tessera database client.
//
// tess dispatches subcommands: serve starts the web server, and every other
// command belongs to the CLI in internal/cli.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"

	"github.com/arunksoman/tessera/internal/cli"
	"github.com/arunksoman/tessera/internal/secrets"
	"github.com/arunksoman/tessera/internal/tiles/mysql"
	"github.com/arunksoman/tessera/internal/tiles/postgres"
	"github.com/arunksoman/tessera/internal/tiles/sqlite"
	"github.com/arunksoman/tessera/tile"
)

// version is set at release with -ldflags.
var version = "dev"

func main() {
	// Every log line, including net/http's through the standard log package, passes the
	// redacting handler, so a password in an error or URL does not reach the terminal or a file.
	slog.SetDefault(slog.New(secrets.NewLogHandler(slog.NewTextHandler(os.Stderr, nil))))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	app := &cli.App{
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
		Version: version,
		TTY:     term.IsTerminal(int(os.Stdin.Fd())),
		// The compiled-in tiles, listed here so what ships is visible in one place.
		Tiles: []tile.Tile{postgres.Tile{}, mysql.Tile{}, sqlite.Tile{}},
	}
	code := run(ctx, app, os.Args[1:])
	stop()
	os.Exit(code)
}

// run returns the process exit code. ctx ends on Ctrl+C or SIGTERM, which
// stops serve; the CLI handles its own interrupts.
func run(ctx context.Context, app *cli.App, args []string) int {
	if len(args) > 0 && args[0] == "serve" {
		return serve(ctx, app, args[1:])
	}
	return app.Run(args)
}
