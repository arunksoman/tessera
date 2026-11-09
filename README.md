# Tessera

An open-source database client with a plugin for each database. A tessera is a single tile in a mosaic: each plugin is a tile, and together they form one picture of your data.

**Status: early development.** The `tess` command-line client and terminal UI work with PostgreSQL, MySQL, and MariaDB. The web app is not built yet. See [the task list](design-spec/task.md) and [the design](design-spec/design.md).

## Install

Download an archive for your platform from [Releases](https://github.com/arunksoman/tessera/releases). To verify it:

```sh
sha256sum --check --ignore-missing sha256sums.txt
gh attestation verify tess_0.2.0_linux_amd64.tar.gz --repo arunksoman/tessera
```

## Build

Requires Go 1.26.

```sh
CGO_ENABLED=0 go build -o tess ./cmd/tess
```

## Use

```sh
# The password is read from standard input and encrypted. It never goes in a flag or the config file.
printf '%s\n' "$DB_PASSWORD" | tess profile add app --tile postgres --host db.example.com \
  --user app --database app --password-stdin

tess connect app
tess query -c app "SELECT * FROM orders LIMIT 10"
tess query -c app -f csv "SELECT * FROM orders" > orders.csv
tess history -c app
```

New remote profiles verify the server's TLS identity unless you choose a weaker mode with `--tls`, which prints a warning.

### Terminal UI

```sh
tess tui          # pick a connection, or create one in a form
tess tui app      # open a saved profile
```

Three panes: an object tree, a SQL editor, and a result grid. Browse a table from the tree, sort and filter on the server, and edit cells. Edits stay pending until you preview them with `Ctrl+S`, then apply them in one transaction. If someone else changed a record since you read it, nothing is applied and you see a conflict.

| Keys | Action |
|---|---|
| `F5` or `Ctrl+Enter` | Run the statement at the cursor (`F6` runs the whole script) |
| `Esc` | Cancel a running query on the server |
| `Tab` | Complete at the cursor |
| `F7`, `F8` | Explain as text, as JSON |
| `Alt+1`, `Alt+2`, `Alt+3`, `Shift+Tab` | Focus the tree, editor, grid, next pane |
| `Alt+T`, `Alt+W`, `Alt+]`, `Alt+[` | New, close, next, previous tab |
| `Alt+C` | Switch connection |
| `Ctrl+R` | Statement history |
| `Ctrl+K`, `F1` | Command palette, help |
| `F9` | Theme: system, light, dark (remembered) |
| `Ctrl+Q` | Quit |

In the grid: `Enter` edits, `v` shows a whole value, `s` sorts, `f` filters, `c` clears filters, and `u` undoes an edit. `NO_COLOR` is respected. `Ctrl+Enter` needs a terminal that can report it, so `F5` always works.

Results in the grid stop at 50,000 rows or 64 MiB, then the query is cancelled and the grid says it was truncated. In an open PostgreSQL transaction that cancel aborts the transaction. Use `tess query` to stream larger results.

### Web server

```sh
tess serve                               # 127.0.0.1 on a free port
tess serve --addr 127.0.0.1:8080
```

`tess serve` opens your default browser on a link with a one-time token, and prints the link too; `--no-browser` only prints it. Opening it signs that browser in, and the link stops working, so restart `tess serve` for a new one.

The web app works like the TUI: add connections from a form, expand the tree, click a table to browse it with sort, filters, and paging, double-click a cell to edit it, then preview the exact SQL and apply it. A SQL tab runs scripts with `Ctrl+Enter` and streams the results; `Esc` or Cancel stops the statement on the server. The same safety rules apply: destructive statements ask first, and production connections ask you to type their name. Alt+T opens a SQL tab, Alt+W closes one.

The web app is embedded into `tess` at build time. Build it before `go build`, which needs Node.js:

```sh
make            # builds the web app, then tess; `make help` lists the other targets
```

Without make, run `scripts/build-web.sh`, then `CGO_ENABLED=0 go build -o tess ./cmd/tess`.

A `tess` built without it serves a page that says so.

It listens on loopback by default. Any other address needs TLS (`--tls-cert` and `--tls-key`) or `--behind-proxy` for a TLS-terminating proxy, and the names browsers use must be passed with `--allow-host`. Requests with any other `Host` are refused, which blocks DNS rebinding.

### Safety

`DROP`, `TRUNCATE`, and `DELETE` or `UPDATE` without `WHERE` need confirmation. Connections created with `--env production` also confirm every write. A script is checked before its first statement runs. In scripts, `--yes` answers confirmations.

`--read-only` refuses writes in the client and asks the server to enforce read-only as well. This is a guardrail: a session can undo it, and a client cannot prove a statement is harmless. For a hard guarantee, connect with a read-only database role.

Exit codes: `0` success, `1` error, `2` usage error, `3` refused by a safety check.

### Secrets

The master key lives in the OS keychain. On hosts without one, set `TESSERA_MASTER_KEY_FILE` to a 32-byte key file, or `TESSERA_PASSPHRASE` to a passphrase. Config and state live in the platform config directory, or in `TESSERA_CONFIG_DIR` if set.

## Develop

Tests follow the design's rule that every tile passes one conformance suite, [tile/tiletest](tile/tiletest/tiletest.go).

```sh
go test ./...                                # unit tests, no services needed

docker compose up -d --wait                  # PostgreSQL 18, MySQL 8.4, MariaDB 11.4
go test -tags integration ./internal/...     # against those databases

docker compose --profile matrix up -d --wait --build    # adds the oldest supported versions
TESSERA_TEST_MATRIX=1 go test -tags integration ./internal/tiles/...
```

The databases are also available to `tess` itself, on localhost only. The passwords are throwaway development values.

| Service | Image | Port | User / password / database |
|---|---|---|---|
| `postgres` | `postgres:18-alpine` | 55418 | `tessera` / `tessera_dev` / `tessera_test` |
| `mysql` | `mysql:8.4` | 53384 | same |
| `mariadb` | built from `alpine:3.22` (11.4) | 53114 | same |
| `postgres-oldest` | `postgres:14-alpine` | 55414 | same |
| `mysql-oldest` | `mysql:8.0` | 53380 | same |
| `mariadb-oldest` | built from `alpine:3.20` (10.11) | 53111 | same |

Alpine is used wherever it exists. Oracle publishes no Alpine MySQL image, and Alpine's own `mysql` package is really MariaDB, so real MySQL uses the official image.

```sh
printf 'tessera_dev\n' | tess profile add local-pg --tile postgres --host 127.0.0.1 --port 55418 \
  --user tessera --database tessera_test --tls disable --password-stdin
```

## Release

Push an annotated version tag on a commit in `main`. The tag message becomes the release notes.

```sh
git tag -a v0.2.0 -m "Release notes here"
git push origin v0.2.0
```

The [release workflow](.github/workflows/release.yml) runs CI, checks the build is reproducible, then publishes archives, checksums, an SBOM, and signed provenance. Web and desktop apps are not built yet: when `tess serve` exists, the web app ships inside the same binary.

## License

[MIT](LICENSE)
