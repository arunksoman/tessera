# Contract review: relational, document, and key-value databases

Status: November 2026. This reviews the `tile` contract ([tile/tile.go](../tile/tile.go)) against the database families Tessera supports next. It records what has changed and what is still open. Open items are not decided yet. Each one names a direction to try first.

## Scope

**Supported now:** relational (PostgreSQL, MySQL, MariaDB), document (MongoDB-like), and key-value (Redis-like) databases. Each one may be transactional (OLTP), analytical (OLAP), or both.

**Later:** vector, graph, spatial, time-series, and wide-column databases. They are covered only in [Later families](#later-families), so that today's choices do not block them.

No plugin has been published, so the contract can still change freely. It stays `v0`.

## What changed

| Area | Before | Now |
|---|---|---|
| Wire format | Plugins received Go field names (`"Kind"`, `"Raw"`) as JSON, so renaming a field broke every plugin. | Every `tile` type has explicit snake_case JSON tags. `TestWireNames` in `tile/plugin` pins them. |
| Transaction tracking | The engine matched SQL verbs (`BEGIN`, `COMMIT`, `ROLLBACK … TO`). | `Classification.Tx` (`TxBegin`, `TxEnd`) comes from the tile's language. Redis `MULTI`/`EXEC`/`DISCARD` work, and `COMMIT AND CHAIN` now correctly keeps the transaction open. |
| Schema changes | The engine matched `CREATE`/`ALTER`/`DROP`/`RENAME`. A Redis `RENAME` would have counted as DDL. | `Classification.Schema` comes from the language. It refreshes the tree and the completion cache, and ends a transaction where `tx.ddl` is unsupported. |
| Browse paging | Keyset paging (`After`) or `Offset` only. A short page ended the browse. | `Layout.Paging = PagingCursor`, `BrowseQuery.Cursor`, and `ResultSummary.Cursor` page by server token, as `SCAN` does. Only an empty cursor ends the browse. The helpers are `engine.PageQuery` and `engine.MorePages`, and the web API returns `more` and `cursor`. |
| Result shapes | `ShapeDocument` and `ShapeKeyValue` existed but had no meaning defined. | Conventions are documented: documents are a key plus the whole document as `KindJSON`, and key-value records are key, type, ttl, value. `Layout.Shape` reaches the web API. |
| Lossless values | Not defined for types JSON lacks. | `KindJSON` uses canonical Extended JSON for object IDs and 128-bit decimals. `KindTime` is RFC 3339 with full precision. |
| OLAP | Implied. | Stated: an analytical database leaves out the `tx.*` capabilities. The UI then shows no transaction state, and edit plans run per item. |
| Versions | Not shown, and plugins were not checked against the tess version. | `tess version`, the TUI status line and help, and the web header show the version. A development build shows its commit. Plugins declare `min_tess` and `max_tess` in the descriptor and the manifest, checked at install, at load, and at each start. |
| Copy | The web app's new-tab button and tab titles said "SQL". | They say "Query". |

## Open: needed for document and key-value tiles

Ordered by how soon a Redis or MongoDB tile would hit each one.

1. **Transactions that are API calls, not statements.** MongoDB transactions run through driver session calls, not query text, so `Classification.Tx` never sees them. *Try:* an optional `Transactor` session facet with `Begin`, `Commit`, and `Rollback` methods. The console would call it from UI buttons, shown only when `tx.multi` is supported.
2. **Large values.** Redis strings, lists, and hashes can be hundreds of megabytes. Design section 2 describes `Value.Truncated`, `FullSize`, and `Handle`, but none of them are built. *Try:* add them, with a `Fetcher` facet that reads a value by handle in pages.
3. **Filter and sort ability per field.** Redis `SCAN` filters only by key pattern and cannot sort. MongoDB can do both on any path. Today a tile can only return an error from `BrowseRequest`. *Try:* add `Layout.Sortable` and per-field allowed `FilterOp`s, so front-ends offer only what works.
4. **Editing documents and keys.**
   - `Change.Set` holds flat field names. Nested document paths work only by tile convention ("a.b.c"), which is ambiguous when a field name contains a dot.
   - The `Old` conflict guard maps cleanly to MongoDB (a filter on the old values). Redis has no cheap compare-and-set: it needs `WATCH` plus `MULTI`, or a Lua script.
   - `ExpectRows` means "keys touched" for Redis.
   - *Try:* a path type (`[]string`) in `Change`, and require each tile to say in `Plan.Atomicity` and the preview text how it guards edits.
5. **Request structure.** `Request.Text` is opaque. That is right for Redis command lines, but MongoDB needs a collection plus an operation plus a JSON document.
   - The tile can define a text form, such as `db.orders.find({...})` or a JSON command document.
   - Both front-ends assume one editor language.
   - *Try:* keep `Text` opaque, and let `Language.ID()` choose the editor mode (below).
6. **Editor language in the web app.** CodeMirror is hard-wired to SQL dialects, and the placeholder says "Write SQL". *Try:* map `Language.ID()` to an editor mode: SQL dialects, a key-value command mode, and JSON.
7. **Completion.** `Catalog` asks for tables and columns, and `internal/meta` builds refs as `schema` then `table`. Redis completes commands and key patterns; MongoDB completes collections and field paths. *Try:* make `Catalog` generic over tree refs, `Children(ref)` and `Fields(ref)`, so the cache stops assuming two levels.
8. **Safety classification beyond read and write.** `FLUSHALL` is destructive. `KEYS *` and a full collection scan on an OLAP table are not writes, but they can hurt a production server. *Try:* a `Classification.Expensive` flag, confirmed like a write on production profiles.
9. **Read-only enforcement.** Redis has no session-level read-only mode, apart from replicas and ACLs. MongoDB has none either. These tiles leave `exec.readonly.server` unsupported, so read-only profiles rely on classification alone. The UI should say so when a read-only profile uses such a tile.
10. **Showing documents.** Both grids show a `KindJSON` cell as one line of text. *Try:* a document viewer for `ShapeDocument` (an expandable tree, with an editable JSON form), in both themes and both front-ends.
11. **Database selection as session state.** Redis `SELECT n` changes the database for a session. It belongs in the tree (`db/0..15`) and must be reset when a session closes, as `SET` is today.

## Open: OLAP

1. **Huge results.** The encrypted spool and the "truncated at N rows" cap (task.md, Engine) are not built yet. Analytical queries need them most.
2. **Cancel.** Every OLAP tile must stop work on the server when cancelled (ClickHouse `KILL QUERY`, for example), as the MySQL tile does. `tiletest`'s `CancelStopsServer` already enforces this for every tile.
3. **Costly metadata.** `Layout.Estimate` should come from system tables, never from `count(*)`, and the tree should page partitions. This is guidance for tiles, not a contract change.
4. **Plugin throughput.** The JSON protocol tops out at about 517k one-column rows per second ([spike-results.md](spike-results.md)), which is probably too slow for wide analytical results. OLAP tiles may need to be built in, or the protocol may need a binary batch encoding.

## Open: plugins

- `Env.Dial` (SSH tunnels) and the `Completer` facet do not cross the plugin protocol yet.
- Downgrade protection for signed releases is not built.
- There is no official index or publisher key.
- The host supports exactly one tile API version. Design section 3 promises N and N−1 for a year once `v1` exists, so the handshake should then accept a range.

## Later families

These are recorded so that work on the families above does not close them off.

| Family | What the contract lacks today |
|---|---|
| Vector | A vector value kind (float32 arrays as raw little-endian bytes, not JSON), a similarity filter operator with a top-k limit, and a binary wire encoding so vectors do not bloat as base64 |
| Graph | Node, edge, and path value kinds, a graph result shape, and a graph view in front-ends |
| Spatial | A geometry kind (WKB, with GeoJSON for display), spatial filter operators, and a map view |
| Time series | Time-range filter helpers, downsampling in browse, and a chart view. Results are large, so this depends on the spool and binary encoding. |
| Wide-column | Cursor paging already fits Cassandra's paging state. Partition-key-aware browse and lightweight transactions for edit guards are missing. |

Front-ends should add views per result shape, not per database: table, document, graph, map, and time chart.
