---
title: Passwords and secrets
description: How tess stores passwords, where the master key lives, and how to run on hosts without a keychain.
sidebar:
  order: 5
---

tess never stores a password in `config.toml`, never accepts one as a command-line flag, and never prints one in an error.

## How passwords are stored

When you add a profile with `--password-stdin`, or fill in the password field in the TUI or web app, tess encrypts the password with AES-256-GCM and stores it in `secrets.json` in the config directory, under the profile's name. Only the 32-byte **master key** that unlocks the vault is kept elsewhere.

tess finds the master key in this order:

1. **`TESSERA_MASTER_KEY_FILE`**: a file holding exactly 32 random bytes.
2. **`TESSERA_PASSPHRASE`**: a passphrase, stretched into a key with Argon2id and a random salt stored beside the vault.
3. **The OS keychain**: macOS Keychain, Windows Credential Manager, or the Secret Service on Linux (GNOME Keyring, KWallet). On first use tess creates a random key and stores it there.

The keychain is the default, and the strongest option on a desktop: a copy of your config directory alone cannot decrypt anything.

## Hosts without a keychain

Servers, containers, and Linux machines without D-Bus have no keychain, and tess says so:

```
OS keychain unavailable (...). Set TESSERA_MASTER_KEY_FILE to a 32-byte key file, or TESSERA_PASSPHRASE to a passphrase
```

Create a key file once, and keep it outside the config directory:

```sh
head -c 32 /dev/urandom > ~/.tess-master.key
chmod 600 ~/.tess-master.key
export TESSERA_MASTER_KEY_FILE=~/.tess-master.key
```

Or use a passphrase, for example from your secret manager:

```sh
export TESSERA_PASSPHRASE="$(pass show tess)"
```

Whichever you choose, use the same one every time: a vault written with one key cannot be read with another. Both are weaker than a keychain, because anyone who can read the key file or the environment can decrypt the vault.

## What plugins see

A plugin gets a connection's password only when it opens that connection, through a callback scoped to that connection, and only until the connection closes. It never sees other profiles' secrets, the master key, or variables such as `TESSERA_MASTER_KEY_FILE`: plugin processes start with an allowlisted environment.

## Secrets in history and logs

Statements are redacted before they reach history or the audit log, so `ALTER USER app PASSWORD 'hunter2'` is stored without the password. Errors from the database driver are built from connection settings that never include the password. tess refuses to save a profile field whose name looks like a secret (containing `pass`, `pwd`, `secret`, or `token`), so a password set with `--set` cannot end up in `config.toml` by mistake.

## Moving to a new machine

Copy the config directory, then make the same master key available: the same key file or passphrase, or, with a keychain, add your profiles' passwords again with `tess profile add ... --password-stdin` because the keychain key does not move with the files.
