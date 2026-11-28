---
title: Installing plugins
description: Trust publishers, install, update, verify, and remove plugins that add databases to tess.
sidebar:
  order: 7
---

PostgreSQL, MySQL, MariaDB, and SQLite are built into `tess`. Other databases come as **plugins**: separate programs that tess starts when you first use them and talks to over standard input and output. To tess and to you, a plugin's database works like a built-in one, in every front-end and through the same safety gate.

## Install a plugin

```sh
tess plugin trust arunksoman "XE8ka9jYYlC5h4yz9AKGxKZb/MgP2BjDXujGKJ4WCZI="   # once per publisher
tess plugin install duckdb --from github:arunksoman/tessera@duckdb-v__DUCKDB_VERSION__
tess profile add warehouse --tile duckdb --set path=warehouse.duckdb
tess query -c warehouse "SELECT 42"
```

Before installing, tess shows the plugin's name, version, source, binary URL, SHA-256, and who signed it, and asks you to confirm. Without a terminal, pass `--yes`.

### Sources

`--from` takes one of:

| Source | Meaning |
|---|---|
| `github:OWNER/REPO` | The `tessera-plugin.json` descriptor in the repository's latest GitHub release |
| `github:OWNER/REPO@TAG` | The descriptor in the release with that tag |
| `https://…/tessera-plugin.json` | A descriptor anywhere on https |
| `./path/to/descriptor.json`, `file:PATH` | A descriptor on disk |
| `./tess-myplugin` | A bare plugin binary, for development |

Without `--from`, tess looks the name up in `config.toml`: first in `[plugins.sources]`, then in each index listed in `[plugins] indexes`:

```toml
[plugins]
indexes = ["https://example.com/tessera-plugins/index.json"]

[plugins.sources]
elasticsearch = "github:acme/tessera-elasticsearch@v0.2.0"
```

An index is a JSON file on https mapping names to sources: `{"plugins": {"redis": "github:acme/tessera-redis"}}`. An index cannot point at a local path. It need not be signed, because trust rests on the descriptor's own signature.

### Trust and signatures

Publishers sign their descriptors with an Ed25519 key. You decide whose keys you trust:

```sh
tess plugin trust acme "BASE64-PUBLIC-KEY"
tess plugin keys            # list trusted publishers
tess plugin untrust acme
```

| Descriptor | Result |
|---|---|
| Signed by a trusted key | Installs, after you confirm |
| Signed, but by no trusted key | **Refused**, even with `--allow-unsigned`: it means tampering or the wrong key |
| Not signed | Refused unless you pass `--allow-unsigned`, with a warning |

You can also pin the binary's hash with `--sha256 HEX`, signed or not.

## What tess checks

At install:

- Only https downloads; a redirect to anything else, or a URL with credentials, is refused.
- The descriptor's name matches the name you asked for, its tile API version matches tess, its `min_tess` and `max_tess` include your tess, and it has a build for your platform.
- The binary's SHA-256 matches the descriptor while it downloads.
- Started once, the plugin reports a tile ID equal to its name, a tess range that includes yours, and a stability of `alpha`, `beta`, or `stable`.
- Reinstalling an older release needs `--force --allow-downgrade`, so a compromised source cannot roll you back to a vulnerable version.

Before **every** start, tess hashes the binary again and refuses to run it if it changed. Check all plugins at any time:

```sh
tess plugin verify          # or: tess plugin verify duckdb
```

## Manage plugins

```sh
tess plugin list                                 # name, version, stability, source
tess plugin install duckdb --force --from ...    # update to a newer release
tess plugin remove duckdb
```

Plugins live in `plugins/NAME/` in the config directory. `tess plugin list`, the TUI connection form, and the web app's connection picker show each plugin's version and stability beside its name.

## The risk you take

A plugin is not sandboxed. It runs as you, with your files and your network. tess limits what it hands over: an allowlisted environment (no `TESSERA_*` keys, no cloud credentials), only the secrets of the connections opened through it, and nothing at all from `tess serve` unless you set `allow_in_server = true`. But a malicious plugin can still do anything a program you run can do. Install plugins only from publishers you would trust with a binary.
