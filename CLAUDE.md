# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project state

Tessera is a plugin-based database client in Go. The `tess` command-line client, terminal UI, and `tess serve` web app exist, for PostgreSQL, MySQL, MariaDB, and SQLite files. The web app is a first slice: connections, tree, SQL tabs, browse, and inline edit. `web/spikes/grid` is a throwaway prototype.

- [design-spec/design.md](design-spec/design.md): the architecture, including parts not yet built.
- [design-spec/task.md](design-spec/task.md): the checklist that drives the work.
- [design-spec/spike-results.md](design-spec/spike-results.md): measurements behind design decisions.

## Working rules

- Work test-first, and tick items in `task.md` as they finish. Tick a box only when the whole item is done. Split a partly done item so each box stays truthful.
- Scope is MySQL/MariaDB, PostgreSQL, and SQLite, with CLI, TUI, and web, plus DuckDB as an official plugin in `plugins/duckdb`. Wails and NoSQL are out for now, although `design.md` still describes them.
- `tess` must build with `CGO_ENABLED=0`; only plugins in `plugins/` may use cgo. Every direct dependency, of tess or of a plugin, needs a justification in section 11 of `design.md`.
- All frontend work needs both light and dark themes, following the system by default, with a persistent manual choice.
- `toolchain` in `go.mod` is pinned to a patched Go release because `govulncheck` found reachable standard library vulnerabilities in older ones. Do not lower it.

## Commands

```sh
make                                              # web app + tess; `make help` lists targets
scripts/build-web.sh                              # web app into web/build/app; needs Node.js
CGO_ENABLED=0 go build -o tess ./cmd/tess
go vet ./... && go vet -tags integration ./...
gofmt -l .
go test ./...                                     # unit tests, no services needed

docker compose up -d --wait                       # PostgreSQL 18, MySQL 8.4, MariaDB 11.4
go test -tags integration ./internal/tiles/... ./internal/cli/... ./internal/tui/...

# SQLite needs no server: its conformance suite and Sakila load run with the unit tests
go test ./internal/tiles/sqlite/

# DuckDB is a plugin in its own module because its driver needs cgo; on Windows it needs a UCRT
# MinGW gcc, so without one run it in Docker: docker run --rm -v "$PWD":/src -w /src/plugins/duckdb golang:1.26 go test ./...
(cd plugins/duckdb && CGO_ENABLED=1 go test ./...)

docker compose --profile matrix up -d --wait --build     # adds the oldest supported versions
TESSERA_TEST_MATRIX=1 go test -tags integration ./internal/tiles/...

# One test or conformance case
go test -run TestClassify/writing_cte ./internal/lang/sqlish/
go test -tags integration -run 'TestConformance/pg18/CancelStopsServer' ./internal/tiles/postgres/

go test ./internal/lang/sqlish/ -run '^$' -fuzz FuzzDialects -fuzztime 30s
govulncheck ./...                                 # repeat with GOOS=linux and GOOS=darwin
scripts/check-deps.sh                             # direct dependencies against design.md section 11
```

`internal/spike` holds Phase 0 measurements behind the `integration` tag and takes about a minute. To run the grid measurement, install Playwright outside the repo and run `PLAYWRIGHT_DIR=<dir> node web/spikes/grid/measure.mjs`. Add `HEADED=1` for visible windows, which must stay in the foreground, because a window behind others gets no animation frames.

Check grid themes with `PLAYWRIGHT_DIR=<dir> node web/spikes/grid/theme.mjs`.

Check the built app under the real CSP in three browsers with `PLAYWRIGHT_DIR=<dir> node web/spikes/csp/check.mjs`. It needs `make` and the compose PostgreSQL. `cd web && npm run lint` bans `{@html}` and DOM HTML sinks.

Check the SQL tab end to end in three browsers against PostgreSQL, MySQL, MariaDB, and SQLite with `PLAYWRIGHT_DIR=<dir> node web/spikes/sqltab/check.mjs`; it covers format, run current statement, result export, and manual commit. It needs `make` and the compose servers. `BROWSERS` and `DBS` limit the runs.

A release comes from pushing an annotated tag like `v0.1.0` on a commit in `main`. The tag message becomes the release notes, and `scripts/release-build.sh` sets the version through `-X main.version`.

Compose ports and logins are in [README.md](README.md). Each server has `tessera_test`, the tiny fixture tests depend on, and `sakila`, a vendored sample database for manual use; SQLite gets both from `test/fixtures` in a temporary file during its tests; after changing fixtures run `docker compose down -v` so init scripts run again. Alpine images are the default, except real MySQL, which has no Alpine image. MariaDB is built from `docker/mariadb-alpine`, because no official Alpine image exists either.

## Architecture

Dependencies point one way: `cmd/tess` → `internal/cli` → `internal/tui` → `internal/engine` → the `tile` contract, which `internal/tiles/*` implement. `cmd/tess/main.go` holds the explicit list of compiled-in tiles; installed plugins are added after them in `internal/cli`.

- **`tile`** is the plugin contract and imports only the standard library. It is `v0` until NoSQL tiles prove it. Nothing in it may assume SQL: `Request` text is opaque, and the object tree is plugin-defined. New features belong in new optional interfaces, not as methods on existing ones. Its JSON tags are the plugin wire format, pinned by `TestWireNames`. Open questions for document and key-value tiles are in [design-spec/contract-review.md](design-spec/contract-review.md).
- **`tile/plugin`** runs a tile in its own process: newline-delimited JSON over stdin and stdout, standard library only. `Serve` is the plugin side and `Client` the host side, which reports facets so type assertions still work. Host-side language calls fail safe (destructive, fully redacted) when the plugin cannot answer.
- **`internal/plugins`** installs, verifies, and launches plugins: https or local sources, Ed25519 descriptor signatures against trusted keys, SHA-256 pinned at install and re-checked before each launch, and an allowlisted environment. Plugins are not sandboxed, so every install needs explicit consent, and `tess serve` loads none unless configured to.
- **`tile/tiletest`** is one conformance suite that every tile runs. Add behaviour expected of all tiles here, not in a single tile's tests. Each tile's `integration_test.go` supplies a `Harness`, and runs the suite again out of process through `tiletest.OutOfProcess`.
- **`internal/engine`** is what all front-ends share. `Conn.Run` splits a script, passes every statement through the safety gate before any runs, then records redacted history and audit. Front-ends should go through it, not call tiles directly, or they bypass the gate.
- **`internal/lang/sqlish`** is a tolerant lexer behind `tile.Language`, used for splitting, classification, and redaction. It never fails on invalid input. It is safety-relevant: a comment or quoting mistake becomes a safety bug, so add cases to its tests and fuzz it after changes. Classification is a guardrail and default-deny; unknown verbs count as writes.
- **`internal/store`** holds profiles in `config.toml`, and history and audit in SQLite. **`internal/secrets`** holds passwords in an encrypted vault. Secrets reach tiles only through `Env.Secret`, and `SaveProfile` refuses secret-looking keys.
- **`internal/server`** is `tess serve`: loopback by default, a one-time launch token exchanged for a session cookie, a `Host` allowlist, and a strict CSP. Inline scripts in `index.html` are allowed by hash and CodeMirror's styles by a per-response nonce, so never add inline styles or `{@html}`. `/api` goes through the engine; exec streams NDJSON frames and returns 409 with prompts for statements that need confirmation. Structure changes are planned by the tile's `Designer` and applied through `/apply` like edits. `/export` is a GET so the browser downloads in the background from a read-only session; `/import` takes the raw file as the body and loads it through `Conn.Load`, which gates every batch.
- **`web`** is the SvelteKit 3 SPA (`#lib/...` imports, config inside `vite.config.ts`), built into `web/build/app` and embedded by `web/embed.go`. Rows live in plain arrays with a version counter, not deep `$state`.
- **`internal/tui`** is the Bubble Tea front-end. Workers report through `Model.events` and never touch model state. `actions` is the one registry behind keys, the palette, and help. Tests in `fake_test.go` run a real program with no terminal, on a fake tile.
- **`internal/tiles/sqlite`** is the SQLite tile on `modernc.org/sqlite`. A session's connection is discarded on close rather than reset. The driver parses text in `DATE`, `DATETIME`, and `TIMESTAMP` columns into times, so browsing selects those columns as text to keep guards and paging exact.
- **`internal/tiles/sqlgen`** builds browse and edit statements for the SQL tiles. **`internal/meta`** caches the catalog for completion.
- **`plugins/duckdb`** is the DuckDB tile as an official plugin, its own Go module on cgo `duckdb-go`, built against this commit through `replace`. It imports `sqlish` and `sqlgen` from `internal`, which Go allows inside the same repository. Released from `duckdb-v*` tags, never marked latest.
- **`internal/ioformat`** has streaming writers. Database content is untrusted: the table writer strips terminal control characters, and CSV neutralizes formulas.

### Invariants that are easy to break

- **Streaming:** results flow as `Schema`, then `Batch` frames, then `Done`, pulled through `ResultStream.Next`. Never collect a whole result in memory. `Value.Raw` is valid only until the next `Next`, so copy anything kept.
- **Lossless values:** values stay canonical bytes. Integers and decimals never pass through a float.
- **Cancel and early close must stop the server's work.** PostgreSQL uses a server cancel. MySQL needs `KILL QUERY` on a second connection, because cancelling the driver's context only drops the socket while the statement keeps running, and closing driver rows reads every remaining row. So the MySQL tile deliberately gives the driver a non-cancelling context.
- **Connection settings are built field by field,** never from user-supplied connection strings. `postgres.parseKeys` is an allowlist, and passwords stay out of parsed strings so they cannot appear in errors.
- **TLS verifies by default,** set explicitly in each tile, because PostgreSQL's driver default of `prefer` can silently fall back to plaintext.
- **The engine never matches statement verbs.** Transaction state and schema changes come from `Classification.Tx` and `Classification.Schema`, which each tile's language sets, so non-SQL languages work. Browse paging goes through `engine.PageQuery` and `engine.MorePages`, because a cursor-paged store can return a short page while more remain.
- **Sessions reset state on close,** so read-only mode and `SET` values cannot leak to the next user of a pooled connection.
- **Edits are guarded and planned.** `Plan` has no side effects, and `Conn.Apply` checks the plan hash, so the text previewed is the text run. A row-count mismatch rolls back and reports a conflict.
