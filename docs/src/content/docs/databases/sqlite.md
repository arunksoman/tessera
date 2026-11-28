---
title: SQLite
description: Open SQLite database files with Tessera, safely, including files you did not create.
sidebar:
  order: 3
---

The `sqlite` tile is built into tess, on a pure-Go SQLite, so it needs no C library.

```sh
tess profile add notes --tile sqlite --set path=./notes.db                  # an existing file
tess profile add scratch --tile sqlite --set path=./scratch.db --set create=true
```

## Connection fields

| Field | Meaning |
|---|---|
| `path` | The database file. Required. |
| `create` | `true` creates the file if it is missing. Without it, a missing file is an error, so a typo never creates an empty database. |

In the web app the path field has a file picker, which can also name a new file. A `tess serve` that is not on loopback refuses file fields.

## Opening files you did not create

A database file can carry views and triggers that run when you query it. tess opens every file with:

- **defensive mode**, which blocks statements that corrupt a file on purpose, such as writing to `sqlite_master`;
- **`trusted_schema` off**, so a function with side effects cannot run from a view or trigger stored in the file;
- **no extensions**, so a file cannot load native code;
- **foreign keys on** and a busy timeout.

The path is percent-encoded into a `file:` URI, so a `?` or `#` in a file name cannot add a parameter. A crafted file is still parsed by SQLite in the tess process; tess can refuse it cleanly, but cannot sandbox it.

## What works

- **Tree:** tables and views of the file.
- **Browse, edit, import, export,** and `EXPLAIN QUERY PLAN`.
- **Designer:** add, rename, and drop columns; add and drop indexes and triggers; and the create script. SQLite's `ALTER TABLE` cannot change a column's type, nullability, or default, or add a key, without rebuilding the table, so the designer refuses those for now.
- **Read-only:** sessions set `query_only`, which SQLite enforces on every attached database.
- **Sessions** are discarded when they close rather than reset, so temporary tables, `ATTACH`, and `PRAGMA` changes never reach the next session.
- **Language:** bracketed identifiers, trigger bodies with semicolons, and `BEGIN IMMEDIATE` and the other lock modes.

SQLite has no exact decimal type, so it stores decimals as floating point. Columns declared `DATE`, `DATETIME`, or `TIMESTAMP` are browsed as text, exactly as stored.

## Load Sakila into SQLite

From a clone of the repository; the data load takes about a minute.

```sh
tess profile add sakila-sqlite --tile sqlite --set path=sakila.db --set create=true --no-history
tess query -c sakila-sqlite < test/fixtures/sakila/sqlite/sqlite-sakila-schema.sql
(echo "BEGIN; PRAGMA defer_foreign_keys = ON;"; cat test/fixtures/sakila/sqlite/sqlite-sakila-insert-data.sql; echo "COMMIT;") |
  tess query -c sakila-sqlite --yes > /dev/null
```
