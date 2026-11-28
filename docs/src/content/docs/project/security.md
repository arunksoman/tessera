---
title: Security model
description: What Tessera protects, the threats it considers, and how it mitigates each one.
sidebar:
  order: 2
---

A database client holds the keys to production. This page is Tessera's threat model: what it protects, what it defends against, and what it does not.

## Assets

Database credentials, query results, history and audit data, and above all the ability to run statements on production systems.

## Threats and mitigations

| Threat | Mitigation |
|---|---|
| Theft of the config directory or a backup | Passwords are encrypted with AES-256-GCM. The master key lives in the OS keychain, not beside the vault. Files are mode `0600` in a `0700` directory. |
| A host with no keychain | A key file (`TESSERA_MASTER_KEY_FILE`) or an Argon2id passphrase (`TESSERA_PASSPHRASE`), clearly weaker and documented as such. |
| Secrets in logs, history, or audit | Statements are redacted before they are stored. Secret values have a type that prints `[redacted]`. Passwords never appear in flags, config files, or connection strings, so they cannot reach parse errors. |
| Cross-site requests to `tess serve` | Origin checks on every state-changing request, plus `SameSite=Strict` cookies. |
| DNS rebinding against a local server | A `Host` header allowlist, always on. |
| Other local processes or websites reaching the server | Loopback by default, and a random one-time token handed to the browser through the launch URL. |
| Stored XSS from database content | Values are rendered only as text. A strict CSP (`default-src 'none'; script-src 'self'`) with no inline script but hashed bootstrap code. A lint rule bans `{@html}` and DOM HTML sinks. |
| SQL injection in generated statements | Values are always bound. Identifiers go through each tile's quoting, tested against hostile names. Filters are a typed structure, not text. |
| Accidental production damage | Default-deny classification, confirmation before a script starts, typed confirmation on production, previews of every edit and structure change, and conflict detection. |
| A malicious database server | `LOAD DATA LOCAL` disabled, message size limits, terminal control characters stripped from output. |
| A malicious SQLite or DuckDB file | SQLite: defensive mode, `trusted_schema` off, no extensions. DuckDB: no automatic extension loading, file and network access off by default. A crafted file is still parsed in process, so it can be refused cleanly but not sandboxed. |
| Man in the middle | TLS verifies certificates and host names by default on every remote profile, with warnings for weaker modes. |
| Formula injection through exports | CSV export neutralizes cells starting with `=`, `+`, `-`, or `@` by default. |
| Tampering with history | An append-only audit log with a hash chain, checked by `tess audit verify`. It makes tampering visible; it cannot stop someone with write access to your files. |
| A malicious plugin | Explicit install with publisher, source, and hash shown; Ed25519 signatures against keys you trust; the binary pinned by SHA-256 and re-checked before every start; an allowlisted environment; only its own connections' secrets; and off by default in `tess serve`. Plugins are **not sandboxed**. |
| Supply-chain attacks on tess | `CGO_ENABLED=0`, a pinned Go toolchain, reproducible builds checked by building twice, `govulncheck` and `osv-scanner` in CI, `npm ci --ignore-scripts`, a justification required for every direct dependency, a CycloneDX SBOM and signed SLSA provenance with every release, and container images scanned before publishing. |

## Limits, stated plainly

- **Classification is a guardrail.** A function called by a `SELECT` can write. For a hard guarantee, connect with a database role that cannot.
- **Read-only mode** can be undone by a session that turns it off. Again, use a read-only role.
- **Plugins run with your permissions.** tess limits what it hands them, but cannot stop a malicious plugin from doing what any program you run can do.
- **`tess serve` has no user accounts yet.** Whoever opens the one-time link first gets the session. Keep it on loopback, or behind TLS and a trusted network.
- **History is redacted, not encrypted,** so it stays searchable. Turn it off per connection with `--no-history`.

## Reporting a vulnerability

Please report security issues privately through [GitHub security advisories](https://github.com/arunksoman/tessera/security/advisories/new), not in public issues.
