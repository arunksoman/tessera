---
title: The command line
description: Manage profiles, run queries and scripts, export results, and read history from the shell with tess.
sidebar:
  order: 3
---

The `tess` command line is for scripts, pipelines, and quick questions. Every command is listed in the [CLI reference](/reference/cli/); this page shows how they fit together.

## Profiles

A profile is a saved connection. Add one with the database's tile and its fields:

```sh
tess profile add app --tile postgres --host db.example.com --user app --database app --password-stdin
tess profile add shop --tile mysql --socket /run/mysqld/mysqld.sock --user shop --database shop --password-stdin
tess profile add notes --tile sqlite --set path=./notes.db
tess profile add warehouse --tile duckdb --set path=warehouse.duckdb      # a plugin
```

- `--host`, `--port`, `--user`, `--database`, and `--socket` set the common fields. `--set KEY=VALUE` sets any other field of the database's form, such as `sslrootcert` or `path`. The fields of each database are on its page under [Databases](/databases/postgresql/).
- `--tls MODE` sets the TLS mode: `sslmode` on PostgreSQL, `tlsmode` on MySQL. New profiles verify the server's certificate and name by default, and tess warns about weaker modes.
- `--password-stdin` reads the password from standard input, without echo on a terminal, and stores it encrypted. Passwords are never accepted as flags, so they cannot leak into shell history or process lists.
- `--env production` tags the profile as production: every write asks for confirmation.
- `--read-only` refuses writes, and asks the server to enforce read-only too.
- `--no-history` keeps no statement history. Writes are still audited.

Running `profile add` with an existing name replaces that profile. Profile names use letters, digits, `_`, `.`, and `-`.

```sh
tess profile list         # name, tile, host, and tags
tess profile rm app       # also deletes its stored password
tess connect app          # check that it connects, and how long it takes
```

## Queries

`tess query -c PROFILE` runs SQL given as an argument, or read from standard input when there is none:

```sh
tess query -c app "SELECT count(*) FROM orders"
tess query -c app < migration.sql
pg_dump --schema-only olddb | tess query -c app --yes
```

A script runs statement by statement in one session, so `BEGIN` ... `COMMIT` and `SET` work as you expect. Before the first statement runs, tess classifies every statement in the script, so a script that would need confirmation halfway through asks first, or is refused, before it changes anything. MySQL and MariaDB scripts may use `DELIMITER` lines, as in the `mysql` client, for procedure and trigger bodies.

### Output formats

| `-f` | Output |
|---|---|
| `table` (default) | Aligned columns for reading. Terminal control characters in values are stripped. |
| `csv` | RFC 4180 CSV with a header row. Cells starting with `=`, `+`, `-`, or `@` are prefixed with `'` so spreadsheets do not run them; `--raw` keeps them as they are. |
| `json` | A JSON array of objects. Integers and decimals keep every digit. |

Results stream: tess writes rows as they arrive and never holds a whole result in memory, so exporting a large table needs no more memory than a small one.

```sh
tess query -c app -f csv "SELECT * FROM events" | gzip > events.csv.gz
tess query -c app -f json "SELECT id, total FROM orders WHERE id = 42" | jq '.[0].total'
```

Statements that change rows print `N rows affected` on standard error, so standard output stays clean for data.

### Cancel and timeouts

`Ctrl+C` cancels the running statement on the server, not only the client. `--timeout 30s` does the same after a duration.

### Confirmations in scripts

Statements that need confirmation prompt on the terminal. When nobody can answer, because the SQL came from standard input or there is no terminal, tess refuses the script with exit code 3 instead of guessing. `--yes` answers every confirmation and prints what it confirmed:

```sh
tess query -c app --yes "DELETE FROM sessions"
```

See [Safety](/guides/safety/) for what needs confirmation.

## History and audit

```sh
tess history -c app          # the last 20 statements: time, status, duration, statement
tess history -c app -n 100
tess audit verify            # check the audit log's hash chain
```

History is redacted: passwords in statements such as `ALTER USER ... PASSWORD '...'` are removed before anything is stored.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Success |
| `1` | Error, such as a connection failure or a SQL error |
| `2` | Usage error: an unknown command or a bad flag |
| `3` | Refused by a safety check: read-only, needs confirmation, or declined |

```sh
tess query -c app < cleanup.sql
case $? in
  0) echo "done" ;;
  3) echo "cleanup needs a human to confirm it" ;;
  *) echo "cleanup failed" ;;
esac
```
