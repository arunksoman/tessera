---
title: DuckDB
description: Install the official DuckDB plugin and open DuckDB database files.
sidebar:
  order: 4
---

DuckDB is an official plugin, built from [`plugins/duckdb`](https://github.com/arunksoman/tessera/tree/main/plugins/duckdb) in the Tessera repository. It is a plugin rather than part of tess because DuckDB's Go driver needs cgo, and tess is built without it. DuckDB is linked into the plugin, so you install nothing else. Its stability is **beta**.

## Install

```sh
tess plugin trust arunksoman "XE8ka9jYYlC5h4yz9AKGxKZb/MgP2BjDXujGKJ4WCZI="
tess plugin install duckdb --from github:arunksoman/tessera@duckdb-v__DUCKDB_VERSION__
tess profile add warehouse --tile duckdb --set path=warehouse.duckdb
tess query -c warehouse "SELECT * FROM duckdb_tables()"
```

Builds exist for Linux amd64 and arm64 (glibc 2.35 or newer, with `libstdc++`), macOS, and Windows amd64. To use DuckDB in the web app, set `allow_in_server = true` under `[plugins]` in `config.toml`. See [Installing plugins](/guides/plugins/) for what the install checks.

## Connection fields

| Field | Meaning |
|---|---|
| `path` | The database file. It must exist unless `create` is set. A path containing `?` is refused. |
| `create` | `true` creates the file if it is missing. |
| `external` | `true` lets SQL read and write other files and URLs, for example `read_parquet('data/*.parquet')` or `COPY ... TO`. Off by default. |

```sh
tess profile add lake --tile duckdb --set path=lake.duckdb --set create=true --set external=true
tess query -c lake --yes "CREATE TABLE trips AS SELECT * FROM read_parquet('trips/*.parquet')"
```

## Safety

- With `external` off, SQL can reach no file or URL except the database itself, and cannot switch that back on. Turn it on only for files you trust: a view stored in the file runs with the same access.
- Extensions never install or load on their own, and community extensions are off. Load one yourself with `LOAD`.
- DuckDB opens a file once per process with one set of settings, so two profiles for the same file need the same `external` choice.
- DuckDB sets read-only mode for a whole database, not per connection, so a read-only profile is enforced by tess's safety gate, not by DuckDB.

## What works

Tree of schemas, tables, and views; browse, inline edit, import, export, and text `EXPLAIN` plans. Values stay exact: integers up to 128 bits, decimals with their full scale, times, intervals, UUIDs, and lists, structs, and maps as JSON.

Not yet: the structure designer, macros in the tree, JSON plans, and exact JSON values in query results (browsing a table shows them exactly).
