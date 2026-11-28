---
title: Architecture
description: How Tessera's packages fit together, for contributors and curious users.
sidebar:
  order: 1
---

Tessera is one Go module that builds one binary, `tess`, with `CGO_ENABLED=0`. The web app is a SvelteKit single-page app embedded into that binary. The full design, including parts not built yet, is in [design.md](https://github.com/arunksoman/tessera/blob/main/design-spec/design.md).

## Dependencies point one way

```mermaid
flowchart TD
    main["cmd/tess<br/>explicit list of built-in tiles"] --> cli[internal/cli]
    main --> server["internal/server<br/>tess serve"]
    cli --> tui["internal/tui<br/>Bubble Tea"]
    cli --> engine
    server --> engine
    tui --> engine["internal/engine<br/>split, gate, run, history, audit"]
    engine --> tile["tile<br/>the contract, stdlib only"]
    engine --> store["internal/store<br/>config.toml, state.db"]
    engine --> secrets["internal/secrets<br/>encrypted vault"]
    cli --> plugins["internal/plugins<br/>install, verify, launch"]
    plugins --> plug["tile/plugin<br/>JSON over stdio"]
    plug --> tile
    pg[internal/tiles/postgres] --> tile
    my[internal/tiles/mysql] --> tile
    lite[internal/tiles/sqlite] --> tile
    pg & my & lite --> sqlish["internal/lang/sqlish<br/>tolerant lexer"]
    pg & my & lite --> sqlgen["internal/tiles/sqlgen<br/>browse and edit SQL"]
    server --> web["web<br/>SvelteKit SPA, embedded"]
```

- **`tile`** is the plugin contract. It imports only the standard library and assumes nothing about SQL. New features arrive as new optional interfaces, never as new methods on existing ones.
- **`internal/engine`** is what every front-end shares. `Conn.Run` splits a script, passes every statement through the safety gate before any runs, then records redacted history and audit. Front-ends go through it, never to tiles directly.
- **`internal/lang/sqlish`** is a lexer that never fails on invalid input, used for splitting, classification, and redaction across the SQL dialects. A comment or quoting mistake there is a safety bug, so it is fuzzed.
- **`internal/server`** serves `/api` through the engine. Statement results stream to the browser as NDJSON; statements that need confirmation return 409 with the prompts, and the browser asks and retries.
- **`internal/tui`** runs workers that report back through events and never touch model state. One action registry drives keys, the palette, and help.

## A statement's path

```mermaid
sequenceDiagram
    participant U as You
    participant F as Front end
    participant E as Engine
    participant L as Tile language
    participant T as Tile session
    participant D as Database
    U->>F: run script
    F->>E: Conn.Run(text)
    E->>L: Split, then Classify each statement
    alt any statement needs confirmation
        E->>F: prompt (nothing has run)
        F->>U: confirm?
        U->>F: yes / type the name
        F->>E: confirmed
    end
    loop each statement
        E->>T: Execute
        T->>D: query
        D-->>T: rows
        T-->>E: Schema, Batch..., Done
        E-->>F: frames, streamed
        E->>E: record redacted history and audit
    end
```

## Invariants

- **Streaming.** Results flow as `Schema`, `Batch`, `Done` frames pulled one at a time. Nothing collects a whole result.
- **Lossless values.** Values stay canonical bytes; integers and decimals never pass through a float.
- **Cancel stops the server.** PostgreSQL uses a server cancel; MySQL uses `KILL QUERY` on a second connection.
- **Connection settings are built field by field** from form values, never from user-supplied connection strings, and TLS verifies by default.
- **The engine never matches statement verbs.** Transaction state and schema changes come from each tile's `Classification`, so non-SQL languages work.
- **Sessions reset on close,** so state cannot leak through a pool.
- **Edits are planned.** Plans have no side effects, and the engine checks the plan's hash so the text previewed is the text run.

## Repository layout

| Path | Contents |
|---|---|
| `cmd/tess` | `main`, `tess serve` flags |
| `tile`, `tile/plugin`, `tile/tiletest` | The public plugin API and conformance suite |
| `internal/…` | Everything private to tess |
| `plugins/duckdb` | The DuckDB plugin, its own Go module because it needs cgo |
| `web` | The SvelteKit web app |
| `docs` | This site (Astro Starlight), and the example plugin in `docs/examples` |
| `design-spec` | Design, task list, and spike measurements |
| `test/fixtures` | Fixture databases and the Sakila sample |
