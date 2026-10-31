# Phase 0 Spike Results

Measured on 1 November 2026 on a Windows 11 laptop, with databases in Docker Desktop on the same machine. Tests are in [internal/spike](../internal/spike/spike_test.go) and [web/spikes/grid](../web/spikes/grid/).

Run them with `docker compose up -d --wait`, then `go test -tags integration -v ./internal/spike/`.

## Findings that changed the design

| Finding | Evidence | Change |
|---|---|---|
| Firefox stops honouring `position: sticky` near 8.9M px | The window drifted off screen once `scrollTop` passed about 8.95M | Scroll height capped at 8M px, not 17.9M |
| At scale, native wheel scrolling skips rows | One scrollbar pixel covers 3.5 rows at 1M rows | The grid owns its position and handles wheel input. 120 px now moves 4.3 rows in all three browsers |
| `go-sql-driver/mysql` has no `parsec` support | No mention in the v1.10.1 source or README | `parsec` moved from v1.x to later |
| MySQL exposes no source table through `database/sql` | `ColumnTypes` gives only name, type, nullable, and scan type | MySQL console results need lexer and catalog checks to be editable, as the design expected |
| An absolute rows/s target mostly measures the server | See Throughput below | Target changed to "within 10% of the raw driver" |

## Findings that confirmed the design

| Claim | Evidence |
|---|---|
| MySQL context cancel leaves the statement running | `SELECT SLEEP` still in `PROCESSLIST` after cancel, on MySQL 8.4 and MariaDB 11.4 |
| `KILL QUERY` on a second connection works with the user's own credentials, and the connection survives | Stopped in about 210 ms on both |
| PostgreSQL cancel works and the connection survives | SQLSTATE `57014` after 306 ms, with a 200 ms deadline |
| A paused MySQL reader gets dropped | Reads failed with `invalid connection` after a 6 s pause with `net_write_timeout=2` |
| PostgreSQL reports each result column's source | `TableOID` set on real columns, 0 on computed ones |
| Streaming keeps memory flat | Heap stayed at 0.4 MB across 2M PostgreSQL rows and 1M MySQL-family rows |

## Throughput

| Case | Rows/s |
|---|---|
| PostgreSQL, cheap values (10 integers) | 216,658 |
| PostgreSQL, expensive expressions (`md5`, `numeric` division) | 85,079 |
| MySQL 8.4 and MariaDB 11.4, expensive expressions | 126,958 and 140,084 (the log did not label which was which) |

The old 200k rows/s target holds when the query is cheap. With expensive expressions the server is the bottleneck: `psql` inside the container alone needed 12.3 s for the 2M rows, a limit of about 163k rows/s. Through the Docker Desktop port the same query took 23.5 s, so the port proxy also costs a large share.

## Binary size

A `CGO_ENABLED=0` stripped build linking pgx, the MySQL driver, `modernc.org/sqlite`, Bubble Tea, Bubbles, Lip Gloss, `huh`, go-keyring, TOML, and `x/crypto` was **9.7 MB**, with 81 modules in the graph.

This is a lower bound. The program barely calls these packages, so the linker removed most of their code. CI should track the real size as code arrives. The 45 MB target still looks safe.

## Grid

The prototype holds 821 DOM nodes for 1M rows by 20 columns, and reaches the first, middle, and last rows in all three browsers. These are headless numbers at 1600×900.

| Pattern | Chromium 156 | Firefox 157 | WebKit 27.2 |
|---|---|---|---|
| Idle (browser ceiling) | 60 FPS | 60 FPS | 62 FPS |
| Wheel | 72 FPS | 61 FPS | 50 FPS |
| Scrollbar drag | 30 FPS | 27 FPS | 15 FPS |
| Random jumps | 29 FPS | 28 FPS | 15 FPS |

No blank frames were seen. Drag and jumps replace every cell on every frame, so they are the worst case.

**Not concluded.** Headless runs may have no GPU rasterization, so these numbers do not settle the 55 FPS target. A visible-window run could not be done from an agent session, because a browser window behind the editor stops receiving animation frames. Run it with the browser in front:

```sh
HEADED=1 PLAYWRIGHT_DIR=<dir with playwright installed> node web/spikes/grid/measure.mjs
```

The rows in `index.html` still set `will-change: transform`, which gives each row its own layer and may cost frame rate. Try removing it first. If drag stays low on real hardware, the next fix is to paint placeholders during fast drags, since real data needs fetching at that point anyway. Canvas is the last resort, because of accessibility.

## Still open

- Foreground FPS measurement, above.
- Screen reader test of the grid. This needs a person with NVDA, JAWS, or VoiceOver.
- Windows SSH agent pipe through `os.OpenFile`. The `ssh-agent` service is stopped on this machine and the pipe is absent.
- PostgreSQL and MySQL streaming through the spool to a browser. The parts are proven separately, and the end-to-end path waits for the spool and server.
- CSP with CodeMirror and SvelteKit output.
