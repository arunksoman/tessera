---
title: Configuration
description: config.toml, environment variables, and the files tess keeps.
sidebar:
  order: 3
---

## Files

Everything lives in one directory, created with mode `0700`:

| OS | Default directory |
|---|---|
| Linux | `$XDG_CONFIG_HOME/tessera`, usually `~/.config/tessera` |
| macOS | `~/Library/Application Support/tessera` |
| Windows | `%AppData%\tessera` |

| File | Contents |
|---|---|
| `config.toml` | Settings, plugin settings, and profiles. No secrets. |
| `secrets.json` | Passwords, encrypted with AES-256-GCM |
| `secrets.salt` | The salt for `TESSERA_PASSPHRASE`, when used |
| `state.db` | History and the audit log, in SQLite |
| `plugins/NAME/` | An installed plugin's binary and its `plugin.json` record with the pinned hash |

## config.toml

tess writes this file itself when you add profiles or change settings, and you can edit it by hand.

```toml
theme = "dark"          # TUI theme: "light", "dark", or empty to follow the terminal
history_keep = 10000    # newest history entries kept; 0 means the default, 10,000

[plugins]
allow_in_server = false                                         # let tess serve load plugins
indexes = ["https://example.com/tessera-plugins/index.json"]   # name lookups for install

[plugins.sources]
elasticsearch = "github:acme/tessera-elasticsearch@v0.2.0"

[plugins.trusted_keys]
acme = "BASE64-PUBLIC-KEY"

[profiles.app]
tile = "postgres"
env = "production"      # confirmations for every write
color = "red"           # web app colour: red, orange, yellow, green, cyan, blue, purple, pink
read_only = false
no_history = false

[profiles.app.values]   # the tile's connection form fields
host = "db.example.com"
port = "5432"
user = "app"
database = "app"
sslmode = "verify-full"
```

Field names under `values` that look like secrets (containing `pass`, `pwd`, `secret`, or `token`) are refused. Passwords go in the vault through `--password-stdin` or a form's password field.

## Environment variables

| Variable | Meaning |
|---|---|
| `TESSERA_CONFIG_DIR` | Use this directory instead of the default |
| `TESSERA_MASTER_KEY_FILE` | A file holding exactly 32 bytes, used as the vault's master key instead of the OS keychain |
| `TESSERA_PASSPHRASE` | A passphrase from which the master key is derived with Argon2id, instead of the OS keychain |
| `NO_COLOR` | Turn off colour in the TUI |

When both are set, `TESSERA_MASTER_KEY_FILE` wins over `TESSERA_PASSPHRASE`. See [Passwords and secrets](/guides/secrets/).
