---
title: MySQL and MariaDB
description: Connect to MySQL 8.0 and newer and MariaDB 10.11 and newer, TLS modes, sockets, and what Tessera supports.
sidebar:
  order: 2
---

The `mysql` tile is built into tess and serves both MySQL and MariaDB. It is tested against MySQL 8.0 and 8.4, and MariaDB 10.11 and 11.4.

```sh
tess profile add shop --tile mysql --host db.example.com --user shop --database shop --password-stdin
```

## Connection fields

| Field | Flag | Default | Meaning |
|---|---|---|---|
| `host` | `--host` | `127.0.0.1` | Host name or address |
| `socket` | `--socket` | | A Unix socket path, used instead of `host` and `port` |
| `port` | `--port` | `3306` | |
| `user` | `--user` | | Required |
| `password` | `--password-stdin` | | Stored encrypted in the vault |
| `database` | `--database` | | Optional: without it, the tree lists every database you can see |
| `tlsmode` | `--tls` | `VERIFY_IDENTITY` | `DISABLED`, `PREFERRED`, `REQUIRED`, `VERIFY_CA`, or `VERIFY_IDENTITY` |
| `tlsca` | `--set` | | CA certificate file |
| `tlscert`, `tlskey` | `--set` | | Client certificate and key |

## TLS

tess defaults to `VERIFY_IDENTITY`: encrypted, with the certificate checked against a trusted CA and the host name. Weaker modes print a warning.

```sh
tess profile add local --tile mysql --host 127.0.0.1 --user root --tls DISABLED --password-stdin
tess profile add sock --tile mysql --socket /run/mysqld/mysqld.sock --user me --password-stdin
```

## What works

- **Tree:** databases, then tables, views, and routines with their parameters.
- **Browse and edit,** with the same conflict checks as every tile.
- **Designer:** columns (including `auto_increment` and other attributes, kept when a column is restated), indexes including full-text, foreign keys, and triggers. MySQL commits each DDL statement on its own, so the preview warns that a structure change is not atomic.
- **Cancel:** `KILL QUERY` on a second connection. Closing the client socket is not enough on MySQL: the statement would keep running.
- **Read-only:** sessions set `SET SESSION TRANSACTION READ ONLY`.
- **Scripts:** `DELIMITER` lines work as in the `mysql` client, for procedure and trigger bodies.

## Hardening

The driver is configured field by field, never from a DSN string you supply. `LOAD DATA LOCAL INFILE` is always off, so a malicious server cannot read your files; cleartext and old-style passwords are refused; and multi-statement packets are off, so tess sends one statement at a time.
