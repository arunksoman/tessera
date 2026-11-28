---
title: Concepts
description: Profiles, tiles, plugins, sessions, the safety gate, history, and audit, the words used throughout Tessera.
sidebar:
  order: 4
---

A few words come up everywhere in Tessera. Here is what each one means.

## Profile
A saved connection: a name, the tile it uses, the tile's connection fields (host, port, user, database, file path, TLS settings), and options such as an environment tag, read-only mode, a colour, and whether to keep history. Profiles live in `config.toml`. Their passwords do not: they live in the encrypted vault, under the profile's name.

## Tile
The code that knows one kind of database. PostgreSQL (`postgres`), MySQL and MariaDB (`mysql`), and SQLite (`sqlite`) are tiles built into `tess`. A tile describes its connection form, opens connections, lists objects for the tree, runs statements and streams their results, and understands its query language well enough to split scripts and classify statements.

## Plugin
A tile that runs as a separate program. tess starts it on first use and talks to it over standard input and output. Plugins add databases without rebuilding tess, and they are installed, signed, and verified like packages. [DuckDB](/databases/duckdb/) is the official plugin.

## Engine
The shared core behind the CLI, TUI, and web app. Front-ends never talk to a tile directly; they go through the engine, so every statement passes the same safety gate and lands in the same history and audit log.

## Session
One physical connection to the database, holding transaction state and settings such as `SET` values. A connection profile opens a pool, and each SQL tab gets its own session. Sessions reset when they close, so read-only mode or a `SET` never leaks to the next user of a pooled connection.

## Safety gate
Before a script runs, the engine splits it into statements and classifies each one as a read, a write, or destructive. Destructive statements, and every write on a production connection, need confirmation before *anything* in the script runs. See [Safety](/guides/safety/).

## Environment tag
`--env production` marks a profile as production. Production connections confirm every write and ask you to type the connection's name. The tag drives safety rules in every front-end.

## Colour
In the web app a profile can have a colour, such as red for the database you must not break. It marks the connection's card, sidebar entry, tabs, and footer. Colours are reminders only; the environment tag is what changes the rules.

## Read-only
`--read-only` refuses writes in tess and asks the server to enforce read-only as well, where the database supports it. It is a guardrail: for a hard guarantee, connect as a database role that cannot write.

## History
Every statement you run, with its duration and status, redacted so passwords in statements such as `CREATE USER ... PASSWORD '...'` are removed. Turn it off per profile with `--no-history`. `tess history -c NAME` lists it.

## Audit log
An append-only record of writes, with who ran them, on which connection, and how many rows changed. Each entry is chained to the previous one by hash, so `tess audit verify` can tell if entries were changed or removed. The audit log records writes even when history is off.

## Plan
When you edit cells or change a table's structure, the tile first builds a plan: the exact statements that will run, with a preview you can read. Planning has no side effects. Applying checks that the plan you saw is the plan that runs, and an edit whose row changed since you read it is rolled back as a conflict.
