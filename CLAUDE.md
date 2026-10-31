# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project state

Tessera is a plugin-based database client in Go. Only the `tess` command-line client exists so far, for PostgreSQL, MySQL, and MariaDB. The terminal UI, web server, and frontend are not built, and `web/spikes/grid` is a throwaway prototype.

- [design-spec/design.md](design-spec/design.md): the architecture, including parts not yet built.
- [design-spec/task.md](design-spec/task.md): the checklist that drives the work.
- [design-spec/spike-results.md](design-spec/spike-results.md): measurements behind design decisions.

## Working rules

- Work test-first, and tick items in `task.md` as they finish. Tick a box only when the whole item is done. Split a partly done item so each box stays truthful.
- Scope is MySQL/MariaDB and PostgreSQL, with CLI, TUI, and web. Wails and NoSQL are out for now, although `design.md` still describes them.
- `tess` must build with `CGO_ENABLED=0`. Every direct dependency needs a justification in section 11 of `design.md`.
- `toolchain` in `go.mod` is pinned to a patched Go release because `govulncheck` found reachable standard library vulnerabilities in older ones. Do not lower it.

## Commands

```sh
CGO_ENABLED=0 go build -o tess ./cmd/tess
go vet ./... && go vet -tags integration ./...
gofmt -l .
go test ./...                                     # unit tests, no services needed

docker compose up -d --wait                       # PostgreSQL 18, MySQL 8.4, MariaDB 11.4
go test -tags integration ./internal/tiles/... ./internal/cli/...

docker compose --profile matrix up -d --wait --build     # adds the oldest supported versions
TESSERA_TEST_MATRIX=1 go test -tags integration ./internal/tiles/...

# One test or conformance case
go test -run TestClassify/writing_cte ./internal/lang/sqlish/
go test -tags integration -run 'TestConformance/pg18/CancelStopsServer' ./internal/tiles/postgres/

go test ./internal/lang/sqlish/ -run '^$' -fuzz FuzzDialects -fuzztime 30s
govulncheck ./...                                 # repeat with GOOS=linux and GOOS=darwin
```

`internal/spike` holds Phase 0 measurements behind the `integration` tag and takes about a minute. To run the grid measurement, install Playwright outside the repo and run `PLAYWRIGHT_DIR=<dir> node web/spikes/grid/measure.mjs`. Add `HEADED=1` for visible windows, which must stay in the foreground, because a window behind others gets no animation frames.

Compose ports and logins are in [README.md](README.md). Alpine images are the default, except real MySQL, which has no Alpine image. MariaDB is built from `docker/mariadb-alpine`, because no official Alpine image exists either.

## Architecture

Dependencies point one way: `cmd/tess` → `internal/cli` → `internal/engine` → the `tile` contract, which `internal/tiles/*` implement. `cmd/tess/main.go` holds the explicit list of compiled-in tiles.

- **`tile`** is the plugin contract and imports only the standard library. It is `v0` until NoSQL tiles prove it. Nothing in it may assume SQL: `Request` text is opaque, and the object tree is plugin-defined. New features belong in new optional interfaces, not as methods on existing ones.
- **`tile/tiletest`** is one conformance suite that every tile runs. Add behaviour expected of all tiles here, not in a single tile's tests. Each tile's `integration_test.go` supplies a `Harness`.
- **`internal/engine`** is what all front-ends share. `Conn.Run` splits a script, passes every statement through the safety gate before any runs, then records redacted history and audit. Front-ends should go through it, not call tiles directly, or they bypass the gate.
- **`internal/lang/sqlish`** is a tolerant lexer behind `tile.Language`, used for splitting, classification, and redaction. It never fails on invalid input. It is safety-relevant: a comment or quoting mistake becomes a safety bug, so add cases to its tests and fuzz it after changes. Classification is a guardrail and default-deny; unknown verbs count as writes.
- **`internal/store`** holds profiles in `config.toml`, and history and audit in SQLite. **`internal/secrets`** holds passwords in an encrypted vault. Secrets reach tiles only through `Env.Secret`, and `SaveProfile` refuses secret-looking keys.
- **`internal/ioformat`** has streaming writers. Database content is untrusted: the table writer strips terminal control characters, and CSV neutralizes formulas.

### Invariants that are easy to break

- **Streaming:** results flow as `Schema`, then `Batch` frames, then `Done`, pulled through `ResultStream.Next`. Never collect a whole result in memory. `Value.Raw` is valid only until the next `Next`, so copy anything kept.
- **Lossless values:** values stay canonical bytes. Integers and decimals never pass through a float.
- **Cancel and early close must stop the server's work.** PostgreSQL uses a server cancel. MySQL needs `KILL QUERY` on a second connection, because cancelling the driver's context only drops the socket while the statement keeps running, and closing driver rows reads every remaining row. So the MySQL tile deliberately gives the driver a non-cancelling context.
- **Connection settings are built field by field,** never from user-supplied connection strings. `postgres.parseKeys` is an allowlist, and passwords stay out of parsed strings so they cannot appear in errors.
- **TLS verifies by default,** set explicitly in each tile, because PostgreSQL's driver default of `prefer` can silently fall back to plaintext.
- **Sessions reset state on close,** so read-only mode and `SET` values cannot leak to the next user of a pooled connection.
