---
title: PostgreSQL
description: Connect to PostgreSQL 14 and newer, TLS modes, sockets, and what Tessera supports.
sidebar:
  order: 1
---

The `postgres` tile is built into tess. It is tested against PostgreSQL 14 and 18.

```sh
tess profile add app --tile postgres --host db.example.com --user app --database app --password-stdin
```

## Connection fields

| Field | Flag | Default | Meaning |
|---|---|---|---|
| `host` | `--host` | `127.0.0.1` | Host name, address, or a Unix socket directory such as `/var/run/postgresql` |
| `port` | `--port` | `5432` | |
| `user` | `--user` | | Required |
| `password` | `--password-stdin` | | Stored encrypted in the vault |
| `database` | `--database` | | Required |
| `sslmode` | `--tls` | `verify-full` | `disable`, `allow`, `prefer`, `require`, `verify-ca`, or `verify-full` |
| `sslrootcert` | `--set` | | CA certificate file, for a private CA |
| `sslcert`, `sslkey` | `--set` | | Client certificate and key, for certificate authentication |

## TLS

tess defaults to `verify-full`: the connection is encrypted, the certificate must chain to a trusted CA, and its name must match the host. The PostgreSQL driver's own default, `prefer`, silently falls back to plaintext, so tess never uses it unless you ask, and warns when you do.

```sh
# a private CA
tess profile add app --tile postgres --host db.internal --user app --database app \
  --set sslrootcert=/etc/ssl/internal-ca.pem --password-stdin

# a local database without TLS
tess profile add dev --tile postgres --host 127.0.0.1 --user postgres --database dev --tls disable --password-stdin
```

## What works

- **Tree:** schemas, then tables, views, and functions with their parameter types.
- **Browse and edit:** keyset paging on the primary key; sort and filter on the server; edits in one transaction, matched by key and old values.
- **Designer:** columns, indexes, foreign keys, and triggers. DDL runs inside the transaction, so a failed change leaves nothing behind.
- **Cancel:** a server-side cancel request, so `Esc` or `Ctrl+C` stops the query on the server.
- **Read-only:** sessions set `SET SESSION CHARACTERISTICS AS TRANSACTION READ ONLY`.
- **Explain:** `EXPLAIN` as text or JSON in the TUI.
- **Language:** the lexer understands dollar-quoted bodies, nested block comments, and quoted identifiers, so scripts split correctly and a function body between `$$` marks stays one statement.
- **Values:** `numeric`, `bigint`, and the rest stay exact; arrays and JSON come back as text.

Connection settings are passed field by field through an allowlist, never as a connection string you supply, and the password is set after parsing, so it cannot appear in a parse error.
