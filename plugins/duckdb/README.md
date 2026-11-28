# DuckDB plugin

Lets tess open [DuckDB](https://duckdb.org) database files. It is a separate program, which tess starts and talks to over standard input and output.

DuckDB is a plugin rather than part of tess because its Go driver needs cgo, and tess is built without it. DuckDB is linked into the plugin binary, so you install nothing else. On Linux the binary uses the system's glibc and `libstdc++`.

## Install

```sh
tess plugin trust arunksoman "PUBLIC-KEY"   # once; the key is in the release notes
tess plugin install duckdb --from github:arunksoman/tessera@duckdb-v0.1.0
tess profile add warehouse --tile duckdb --set path=warehouse.duckdb
tess query -c warehouse "SELECT * FROM duckdb_tables()"
```

Profile fields:

| Field | Meaning |
|---|---|
| `path` | The database file. It must exist unless `create` is set. |
| `create` | `true` creates the file if it is missing. |
| `external` | `true` lets SQL read and write other files and URLs, for example `read_parquet('data/*.parquet')` or `COPY ... TO`. Off by default. |

## Safety

- With `external` off, SQL can reach no file or URL except the database itself, and cannot switch that back on. Turn it on only for files you trust: a view stored in the file runs with the same access.
- Extensions never install or load on their own, and community extensions are off. Load one yourself with `LOAD`.
- DuckDB opens a file once per process with one set of settings, so two profiles for the same file need the same `external` choice.
- DuckDB sets read-only mode for a whole database, not per connection. A read-only profile is enforced by tess's safety check, not by DuckDB.

## Not yet supported

Changing table structure in the designer, macros in the tree, JSON query plans, and exact JSON values in query results (browsing a table shows them exactly). See the DuckDB plugin section of [task.md](../../design-spec/task.md).

## Develop

The plugin is its own Go module and builds against the tessera code in the same commit. It needs a C toolchain: gcc or clang on Linux and macOS, and a UCRT MinGW gcc on Windows (MSYS2: `pacman -S mingw-w64-ucrt-x86_64-gcc`). Older MinGW builds that link against msvcrt fail with undefined `__stdio_common_vsnprintf_s`.

```sh
cd plugins/duckdb
CGO_ENABLED=1 go test ./...     # the conformance suite, in and out of process
CGO_ENABLED=1 go build -o tess-duckdb .
tess plugin install duckdb --yes --allow-unsigned --from ./tess-duckdb
```

Without a local toolchain, test in Docker from the repository root:

```sh
docker run --rm -v "$PWD":/src -w /src/plugins/duckdb golang:1.26 go test ./...
```

## Release

Push an annotated tag such as `duckdb-v0.1.0` on a commit in `main`. The tag message becomes the release notes. [release-duckdb.yml](../../.github/workflows/release-duckdb.yml) builds the binaries on Linux, macOS, and Windows, writes and signs `tessera-plugin.json` with the `TESSERA_PLUGIN_SIGNING_KEY` secret (a key from `tess plugin keygen`), and publishes a release that is not marked latest, because the latest release of this repository is tess.
