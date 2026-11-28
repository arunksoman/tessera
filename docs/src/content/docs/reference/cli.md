---
title: CLI reference
description: Every tess command and flag.
sidebar:
  order: 1
---

Flags may come before or after positional arguments. `tess help` prints a summary. Exit codes: `0` success, `1` error, `2` usage error, `3` refused by a safety check.

## tess profile add

```
tess profile add NAME --tile TILE [flags]
```

Saves a connection profile, replacing one with the same name. Names use letters, digits, `_`, `.`, and `-`.

| Flag | Meaning |
|---|---|
| `--tile TILE` | `postgres`, `mysql`, `sqlite`, or an installed plugin. Required. |
| `--host H` | Host name or address (PostgreSQL also accepts a socket directory) |
| `--port N` | Port |
| `--socket PATH` | Unix socket (MySQL and MariaDB) |
| `--user U` | User |
| `--database D` | Database |
| `--set KEY=VALUE` | Any other field of the tile's connection form. Repeatable. |
| `--tls MODE` | `sslmode` on PostgreSQL, `tlsmode` on MySQL |
| `--env production` | Tag as production: every write needs typed confirmation |
| `--read-only` | Refuse writes, and ask the server to enforce read-only |
| `--no-history` | Keep no statement history; writes are still audited |
| `--password-stdin` | Read the password from standard input (without echo on a terminal) and store it encrypted |

Weak settings, such as TLS that does not verify, print warnings.

## tess profile list

Prints one line per profile: name, tile, host, and `[production]` or `[read-only]` tags.

## tess profile rm NAME

Deletes the profile and its stored password.

## tess connect NAME

Connects, pings, and prints how long it took. Times out after 15 seconds.

## tess query

```
tess query -c NAME [-f table|csv|json] [--yes] [--raw] [--timeout 30s] [SQL]
```

Runs SQL given as an argument, or read from standard input when none is given. A script runs statement by statement in one session, after the whole script passes the safety gate.

| Flag | Meaning |
|---|---|
| `-c NAME` | Profile. Required. |
| `-f FORMAT` | `table` (default), `csv`, or `json` |
| `--yes` | Answer yes to every confirmation, printing each one to standard error |
| `--raw` | In CSV, keep cells starting with `=`, `+`, `-`, or `@` as they are instead of neutralizing them |
| `--timeout DURATION` | Cancel on the server after this long, such as `30s` or `5m` |

`Ctrl+C` cancels the running statement on the server. Rows go to standard output; `N rows affected` and prompts go to standard error. Without a terminal, or when SQL comes from standard input, statements that need confirmation are refused (exit 3) unless `--yes` is given.

## tess history

```
tess history -c NAME [-n 20]
```

Prints the newest statements for a profile: time, `ok` or `error`, duration in milliseconds, and the redacted statement.

## tess audit verify

Checks the hash chain of the audit log and prints how many entries it holds, or fails if an entry was changed or removed.

## tess tui

```
tess tui [NAME]
```

Opens the terminal UI, on a profile or on the connection picker. Needs a terminal. See [The terminal UI](/guides/tui/).

## tess serve

```
tess serve [--addr HOST:PORT] [--tls-cert FILE --tls-key FILE] [--behind-proxy] [--allow-host NAME]... [--no-browser]
```

| Flag | Meaning |
|---|---|
| `--addr HOST:PORT` | Listen address. Default `127.0.0.1:0`, a free port on loopback. |
| `--tls-cert FILE`, `--tls-key FILE` | Serve HTTPS with this PEM certificate and key |
| `--behind-proxy` | A TLS-terminating proxy sits in front, so plain HTTP on a non-loopback address is allowed |
| `--allow-host NAME` | Also accept `NAME` in the `Host` header. Repeatable. |
| `--no-browser` | Print the sign-in link without opening a browser |

A non-loopback address needs TLS or `--behind-proxy`. See [Run tess serve on a server](/guides/deploy/).

## tess plugin

| Command | Meaning |
|---|---|
| `tess plugin install NAME [--from SOURCE]` | Install a plugin. Without `--from`, looks the name up in `config.toml`. |
| `  --sha256 HEX` | Also require the binary to have this hash |
| `  --allow-unsigned` | Allow a descriptor or binary with no signature |
| `  --force` | Replace an installed plugin |
| `  --allow-downgrade` | With `--force`, allow an older or incomparable version |
| `  --yes` | Do not ask; required without a terminal |
| `tess plugin list` | Installed plugins: name, version, stability, signer, source |
| `tess plugin remove NAME` | Uninstall |
| `tess plugin verify [NAME]` | Check installed binaries against their pinned hashes |
| `tess plugin trust PUBLISHER KEY` | Trust a publisher's Ed25519 public key |
| `tess plugin untrust PUBLISHER` | Stop trusting it |
| `tess plugin keys` | List trusted publishers |
| `tess plugin keygen --out FILE` | Create a signing key; prints the public key |
| `tess plugin sign --key FILE DESCRIPTOR` | Write `DESCRIPTOR.sig` |

See [Installing plugins](/guides/plugins/) and [Publishing a plugin](/plugins/publishing/).

## tess version

Prints `tess VERSION`. Development builds print the commit too.
