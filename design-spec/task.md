# Tessera Tasks

Implementation checklist for [design.md](design.md). Section numbers below refer to that document.

## Scope

**In:** `tess` CLI and TUI, `tess serve` web app, MySQL/MariaDB, PostgreSQL, and SQLite files. DuckDB files through an official plugin in `plugins/duckdb`, released on its own.

**Out for now:** Wails desktop, Redis, MongoDB, Elasticsearch, remote libSQL (Turso), query sharing and charts. Redis and Elasticsearch can arrive as installable plugins without changes to `tess`.

**Changes from the design:**

- No internal Redis tile, so the `tile` contract is tested on SQL only. It stays `v0`, and NoSQL may force breaking changes to it later.
- Third-party plugins were pulled forward from Phase 3, without gRPC: a standard-library JSON protocol over standard input and output (design section 3).
- No Wails spike, `Transport` abstraction, `go.work`, or `desktop/` module. The HTTP API and SPA stay free of desktop assumptions, so these can be added later.

## Decisions needed first

- [x] GitHub owner: `github.com/arunksoman`
- [x] License: MIT
- [x] Oldest supported servers: PostgreSQL 14+, MySQL 8.0+, MariaDB 10.11+
- [x] `tess serve` starts as a localhost tool and becomes a team service in Phase 3
- [x] Name "Tessera" accepted

## Phase 0: Spikes

Each result can change the design. Results are in [spike-results.md](spike-results.md).

- [x] Build a virtual grid prototype with 1M rows and scaled scrolling
- [ ] Measure grid frame rate in Chromium, Firefox, and WebKit against the 55 FPS target
- [ ] Test the grid with a screen reader and decide DOM versus canvas fallback
- [ ] Stream PostgreSQL rows through `pgconn` to a spool to the browser, and measure throughput and memory
- [ ] Stream MySQL rows the same way, including a slow reader against `net_write_timeout`
- [x] Prove MySQL cancel through `KILL QUERY` on a second connection
- [x] Prove PostgreSQL cancel through `CancelRequestContextWatcherHandler`
- [x] Measure binary size with `modernc.org/sqlite` and the Charm stack
- [x] Run CodeMirror and SvelteKit static output under the strict CSP in Chromium, Firefox, and WebKit
- [ ] Explain WebKit's one style-src message on a system colour-scheme change, and check it on real Safari
- [x] Verify whether `go-sql-driver/mysql` exposes the original table name of result columns
- [x] Verify MariaDB `parsec` auth and `charm.land/huh/v2`
- [ ] Verify the Windows SSH agent pipe through `os.OpenFile`
- [x] Revise the performance targets from these measurements

**Exit:** targets are confirmed or revised, and the grid approach is chosen.

## Phase 1: MVP

### Project setup

- [x] Run `git init`, then add `go.mod`, `.gitignore`, license, and README
- [ ] Create the repository layout from section 10, without `desktop/` and `internal/tiles/redis/` (built package by package; `internal/spool` and `internal/wire` remain)
- [x] Set up CI for `go vet`, `go test`, `gofmt`, and `CGO_ENABLED=0` builds on Linux, macOS, and Windows
- [x] Add `govulncheck` per `GOOS`
- [x] Add `osv-scanner` once npm dependencies exist
- [x] Add the dependency allowlist check
- [x] Add the `docker compose` database matrix behind `//go:build integration`
- [ ] Add benchmarks with `benchstat`, and fail CI on a regression over 10%

### Tile contract

- [x] Move `design-spec/tile-api-sketch.go` into `tile/`, marked `v0`
- [x] Stable JSON tags on every contract type, pinned by a golden test, as the plugin wire format
- [x] Language-reported transaction and schema effects (`Classification.Tx`, `Classification.Schema`) instead of SQL verbs in the engine
- [x] Cursor paging for browse (`PagingCursor`), through the engine, TUI, and web API
- [x] Document and key-value result conventions, and OLAP as the absence of `tx.*` capabilities
- [ ] Resolve the open items in [contract-review.md](contract-review.md) before the first document or key-value tile ships
- [x] Remove facets and types that neither SQL tile needs yet
- [x] Build the `tiletest` conformance suite: streaming, cancel, read-only, tree paging, hostile identifiers
- [x] Add browse and plan purity to `tiletest`, once those facets exist

### Plugins

Design section 3. Built before the contract is frozen, so plugins built now may need rebuilding while `tile` is `v0`.

- [x] Out-of-process protocol in `tile/plugin`, standard library only: `Serve` for plugins and a host client that the engine cannot tell from an in-process tile
- [x] Facets, cancel, stream context, and secret callbacks across the process boundary, with frame validation and fail-safe language calls
- [x] `tiletest.OutOfProcess`, and out-of-process conformance tests for the PostgreSQL and MySQL tiles
- [x] Pass the out-of-process conformance run against the compose databases
- [x] `tess plugin install` from `github:OWNER/REPO[@TAG]`, an https descriptor URL, or a local path, with sources and indexes in `config.toml`
- [x] Ed25519 descriptor signatures, `tess plugin trust`, `keygen`, and `sign`; unsigned installs only with `--allow-unsigned`
- [x] SHA-256 pinning at install, re-checked before each launch and by `tess plugin verify`
- [x] Confirmation before install, https-only downloads and redirects, size caps, staged install, manifest check
- [x] Allowlisted plugin environment, per-connection secret scoping, and plugins off by default in `tess serve`
- [x] `tess plugin list` and `remove`, and `profile add --set` for plugin form fields
- [x] Plugin `min_tess` and `max_tess` in the descriptor and manifest, checked at install, load, and every start
- [x] Show the tess version in `tess version`, the TUI status line and help, and the web sidebar; development builds show their commit
- [ ] Meet the out-of-process throughput gate (protocol ceiling measured at about 517k one-column rows/s; see spike-results.md)
- [ ] Carry `Env.Dial` and the `Completer` facet across the protocol
- [x] Refuse downgrades to an older signed version
- [ ] Publish an official index and publisher key, so `tess plugin install NAME` works without configuration

### Platform services

- [x] Secrets: master key in the OS keychain, AES-256-GCM blobs, Argon2id fallback, key file option
- [x] Add a secret type that always prints `[redacted]`
- [x] Add a redacting `slog` handler
- [x] Store: TOML config and profiles under `~/.config/tessera/`, SQLite state with migrations
- [ ] Dialer: dial policy and SSH chains with password, key, agent, keyboard-interactive, and jump hosts
- [ ] Check SSH host keys against `known_hosts` with trust on first use
- [ ] Token helper command for cloud auth, config-file only in server mode

### Engine

- [x] Open saved profiles, with connection reuse inside each tile
- [x] Connection manager: multiple simultaneous connections, open-connection cap, idle close
- [x] Sessions pinned to one connection, with state reset on close
- [x] Long-lived sessions for console tabs, with explicit transactions
- [x] Executor with `context` cancel and timeouts
- [ ] Encrypted result spool: size cap, random page reads, delete on close, sweep at startup
- [ ] Drain into the spool, then cancel at the cap and report truncation
- [ ] Binary page format in `internal/wire`, with fuzz tests
- [x] Metadata cache with lazy loading, TTL, and invalidation on DDL
- [x] History with redaction and a pruning function
- [x] Run history pruning on a retention setting, and add a per-connection off switch
- [x] Audit log with a hash chain

### SQL language support

- [x] Tolerant lexer with PostgreSQL rules: dollar quotes, `E''` strings, comments
- [x] Add MySQL rules: backticks, backslash strings, `#` comments, `/*! */` treated as code
- [x] Support `DELIMITER` in the splitter
- [x] Statement splitter
- [x] Default-deny statement classifier, with corpus and fuzz tests
- [x] Statement redaction for `PASSWORD '...'` and similar
- [x] Clause tracker and alias, CTE, and subquery scope resolution
- [x] Ranked completion from the metadata cache, with a static list of keywords and functions
- [ ] Read keywords and functions from the live server
- [ ] On-demand server-side validation: protocol `Parse` for PostgreSQL, `PREPARE` for MySQL

### PostgreSQL tile

- [x] Connect over TCP, verifying TLS by default
- [ ] Test Unix socket, all `sslmode` values, and client certificates against a TLS-enabled server
- [x] SCRAM auth, tested against PostgreSQL 14 and 18
- [ ] Test MD5, cert, and peer auth
- [ ] Token helper passwords
- [x] Streaming execution with `ResultReader` in text format
- [x] Object tree: schema, table, view, plus columns and indexes through `Describe`
- [x] Add functions, procedures, and aggregates to the tree, with parameters and result types
- [x] List `pg_catalog` and `information_schema` after user schemas
- [x] Primary and foreign keys through `KeyDescriber`
- [ ] Add databases to the tree
- [x] Browse with keyset pagination, filter AST, sort, and estimated count
- [x] Edit plan and apply with guarded updates
- [ ] Editable detection from result metadata
- [x] Explain as text and JSON
- [x] Server read-only transactions
- [x] Pass the current `tiletest` (versions in the conformance run)
- [x] Pass `tiletest` with browse and plan purity

### MySQL and MariaDB tile

- [x] Connect over TCP, with all five TLS modes tested against a self-signed server
- [ ] Test Unix socket, client certificates, and a successful `VERIFY_IDENTITY` with a trusted CA
- [x] `caching_sha2` (MySQL) and `native` (MariaDB) auth
- [ ] Test `sha256` and `client_ed25519` auth
- [ ] Token helper passwords
- [x] Refuse `mysql_clear_password` without TLS, and keep `allowAllFiles=false`
- [x] Streaming execution with `sql.RawBytes`
- [x] Object tree: schema, table, view, plus columns and indexes through `Describe`
- [x] Add routines to the tree, with parameters and result types
- [x] List system schemas after user schemas
- [x] Primary and foreign keys through `KeyDescriber`
- [x] Browse with keyset pagination, filter AST, sort, and estimated count
- [x] Edit plan and apply with guarded updates and `clientFoundRows=true`
- [x] Explain as text and JSON
- [x] Server-enforced read-only (`SET SESSION TRANSACTION READ ONLY`)
- [x] Pass the current `tiletest` (versions in the conformance run)
- [x] Pass `tiletest` with browse and plan purity

### Safety

- [x] Safety gate combining role, environment tag, classification, and confirmation
- [x] Confirmation for `DROP`, `TRUNCATE`, and `DELETE`/`UPDATE` without `WHERE`
- [x] Type-to-confirm on connections tagged production
- [x] Read-only connections
- [x] Verified TLS by default on new profiles
- [x] Warn when a profile uses a weak TLS mode

### CLI and TUI

- [x] Subcommands: `tess connect`, `tess query`, `tess history`, `tess audit verify`, profile management
- [x] Subcommand: `tess tui`
- [x] Subcommand: `tess serve`
- [x] `tess query` output as table, CSV, and JSON, safe in pipes and scripts
- [x] Lip Gloss theme generated from the shared tokens, with `NO_COLOR` and color fallback
- [x] TUI layout: tree, editor, grid, status line, environment banner
- [x] TUI editor with tabs, autocomplete, and cancel
- [x] TUI grid with browse, sort, filter, and inline edit with preview
- [x] TUI connection forms rendered from `Form`
- [x] Shared action registry and keyboard map from section 12

### Web server

- [x] Bind to `127.0.0.1`, with a per-launch token and one-time URL
- [x] `Host` allowlist, `CrossOriginProtection`, CSP and security headers
- [x] Refuse non-local binding without TLS or an explicit proxy flag
- [ ] Single admin account with Argon2id and secure cookies
- [x] Build connection settings field by field, with no raw connection strings
- [x] API: connections, tree, exec, pages, cancel, plan, apply
- [x] API: keys
- [x] API: structure, structure plan, create script, streaming export download, and streaming import
- [ ] API: describe and complete
- [ ] One SSE event stream per browser tab, handling the HTTP/1.1 connection limit (exec streams NDJSON per request for now)
- [x] Embed the frontend with `go:embed`, with a committed placeholder so builds work

### Web frontend

The grid prototype already follows light and dark themes, tested by `web/spikes/grid/theme.mjs`.

- [x] SvelteKit static SPA with a pinned lockfile and `--ignore-scripts`
- [x] Design tokens as CSS variables in light and dark themes
- [ ] Self-hosted Inter and JetBrains Mono fonts
- [x] Theme follows the system by default, with a manual light, dark, or system choice that persists and does not flash on load
- [ ] Every screen and component is tested in both themes, including the AA contrast check
- [x] App shell: tree, tabs, resizable panes, status bar, environment banner
- [x] Connection tiles with color, glyph, and label
- [x] Start page with saved and recent connections
- [x] Connection colours in the web app, chosen from a fixed palette and stored as `color` in the profile; environment tags stay for the TUI and the safety rules
- [x] Connection form: database type picker with logos, grouped fields, TLS options collapsed, and a password reveal
- [x] Inline SVG icons (Lucide paths and Simple Icons database logos, licences in `web/src/lib/icons`) instead of text glyphs
- [x] CodeMirror editor with tabs and SQL keyword completion
- [ ] Engine-driven autocomplete and query history in the editor
- [x] SQL tab: auto and manual commit with commit and rollback, format SQL, run the statement at the cursor, and a keyboard shortcut list
- [x] Export a query result shown in the SQL tab as CSV, JSON, or JSON Lines
- [x] Virtualized grid with keyboard navigation, from the Phase 0 prototype
- [ ] Grid decoding in a Web Worker
- [x] Browse: pagination, sort, filter
- [ ] Browse: column show and hide
- [x] Inline edit with pending state, preview, apply, and rollback
- [x] Column types and primary and foreign key marks in grid headers
- [x] Context menus on grid cells, column headers, and tree nodes, also from the keyboard
- [x] Table and view menu: structure, SQL create script, export, import, rename, truncate, and drop, with typed confirmation for destructive work
- [x] Routines in the tree, with parameter types
- [x] Connection form rendered from `Form`
- [x] Empty, error, and loading states, with row progress and a cancel button
- [x] Render all database values as text only
- [x] Lint rule banning `{@html}` and DOM HTML sinks, in `npm run lint`
- [ ] Add axe and Playwright tests, and bundle size budget in CI

### Export

- [x] Streaming CSV and JSON export
- [x] JSON Lines export, and whole-table export from the web app as a background download
- [x] Neutralize spreadsheet formula cells by default

**Exit:** the authors use it daily on real databases, targets hold in CI, and a public alpha is released.

## Phase 2: Full CRUD and administration

### SQLite

- [x] SQLite tile on `modernc.org/sqlite`, no new dependency: a file path only, created only on request, defensive mode, foreign keys on, `trusted_schema` off
- [x] SQLite lexing: bracketed identifiers, trigger bodies with semicolons, `BEGIN IMMEDIATE` and the other lock modes
- [x] Tree, browse, edit, load, export, and `EXPLAIN QUERY PLAN`, with the conformance suite in the unit tests, in and out of process
- [x] Structure: add, rename, and drop columns; add and drop indexes and triggers; create script; refuse changes that need a table rebuild
- [x] Sakila port for SQLite, loaded by a test
- [x] SQL tab browser check covers SQLite
- [x] File picker for file fields on a local `tess serve`, which can also name a new database to create on connect
- [ ] Table rebuild for column type, nullability, default, and key changes
- [ ] Remote libSQL (Turso) over HTTP
- [ ] Limit which database files `tess serve` opens when it is not on loopback

### DuckDB plugin

Design sections 6 and 11. Its own module in `plugins/duckdb`, because the DuckDB driver needs cgo.

- [x] DuckDB lexing in `sqlish`: PostgreSQL's rules with block comments that do not nest
- [x] Plugin on `duckdb-go`, statically linked: a file path only, created only on request, extensions never installed or loaded on their own, file and URL access off unless the profile allows it
- [x] Values: exact integers to 128 bits, decimals with their scale, times, intervals, UUIDs, and lists, structs, and maps as JSON
- [x] Tree, browse, edit, load, and `EXPLAIN`, with the conformance suite in the unit tests, in and out of process
- [x] CI on Linux, macOS, and Windows, and a release workflow for `duckdb-v*` tags that builds, signs, and publishes a descriptor
- [x] End-to-end check: `tess` installs the plugin binary and queries a file through it
- [ ] First signed release, with the signing key in the repository secrets
- [ ] Structure designer: columns and indexes (DuckDB has no triggers, so the shared structure tests need a way to skip them)
- [ ] Macros in the tree as routines
- [ ] JSON plans, read from `EXPLAIN (FORMAT JSON)`'s second column
- [ ] Lossless JSON values in query results; the driver decodes JSON into floats, though browsing reads it exactly
- [ ] Read-only profiles enforced by DuckDB itself, which sets access mode per database rather than per connection

### Data

- [x] Foreign-key jump in the web app
- [ ] Foreign-key jump in the TUI
- [ ] Form view for single records
- [ ] Insert, duplicate, and bulk delete rows
- [ ] Visual filter builder plus raw expression
- [ ] View and edit large values through `Handle`

### Structure

- [ ] `Actions` pipeline with form, plan, preview, confirm, apply, and audit
- [x] `Designer` facet: describe structure, plan structure changes, and a create script, across the plugin protocol and in the conformance suite
- [x] Alter, rename, truncate, and drop tables, and drop views
- [ ] Create and drop databases, schemas, and tables
- [x] Columns, indexes, primary keys, and foreign keys
- [ ] Check and exclusion constraints
- [x] Triggers
- [ ] Views and routines
- [x] Table designer with generated SQL preview, in the web app, for existing tables
- [ ] Table designer in the TUI
- [x] Show atomicity in previews, including MySQL's implicit DDL commit

### Users and server

- [ ] Users, roles, and grants
- [ ] Process list and kill query
- [ ] Server variables, status metrics, and table sizes
- [ ] Explain plan tree view

### Import and export

- [x] Streaming CSV, JSON, and JSON Lines import in the web app, gated and in one transaction (`Loader` facet)
- [ ] Import in the CLI and TUI, with column mapping
- [x] SQL export with `INSERT`
- [ ] SQL export with `COPY`
- [ ] Write-only XLSX export
- [ ] Backup and restore through the user's `pg_dump` and `mysqldump`

### Workspace

- [ ] Saved queries
- [x] Command palette in the TUI
- [ ] Command palette in the web app
- [ ] TUI forms for all `Actions`
- [ ] Keyboard-only grid operation that follows the ARIA grid pattern

**Exit:** day-to-day administration no longer needs phpMyAdmin or DBeaver.

## Phase 3: Team server and v1.0

### Multi-user

- [ ] User accounts and server-side session revocation
- [ ] Connection roles: `viewer`, `reader`, `editor`, `admin`, with optional per-role database credentials
- [ ] OIDC login
- [ ] Rate limits per IP and account
- [ ] Global read-only mode
- [ ] Admin-only connection creation, with link-local and metadata addresses denied by default
- [ ] Audit log viewer and syslog forwarding

### Additional connection modes

- [ ] `sslnegotiation=direct`, multi-host, and `target_session_attrs`
- [ ] LDAP and PAM over TLS
- [ ] PgBouncer transaction mode toggle
- [ ] `.pgpass` and `pg_service.conf` import for the CLI, and `~/.my.cnf` import
- [ ] Kerberos for PostgreSQL through `RegisterGSSProvider` and `gokrb5`

### Release

- [x] Reproducible builds, verified by building twice in the release job
- [x] CycloneDX SBOM with each release
- [x] Signed SLSA provenance through GitHub attestations (Sigstore keyless)
- [x] Release job on version tags on `main`, for Linux, macOS, and Windows on amd64 and arm64
- [x] Release 0.1.0 (CLI only)
- [x] Release 0.2.0 (CLI and TUI)
- [x] Include the web app in releases, once `tess serve` exists
- [ ] Nightly benchmarks on fixed hardware, with published results
- [ ] User docs and a threat model page
- [ ] External security audit, with findings resolved
- [ ] Release v1.0

**Exit:** v1.0 is released with audit findings resolved.
