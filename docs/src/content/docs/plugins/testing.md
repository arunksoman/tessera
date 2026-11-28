---
title: Testing with tiletest
description: Run Tessera's conformance suite against your tile, in process and across the plugin protocol.
sidebar:
  order: 5
---

[`tile/tiletest`](https://github.com/arunksoman/tessera/tree/main/tile/tiletest) is the conformance suite every built-in tile and the DuckDB plugin pass. Behaviour expected of all tiles lives there, not in a single tile's tests, so a passing tile behaves the way the front-ends and the safety gate assume.

## Test across the process boundary

Whatever your database, test your tile both directly and as a plugin. `tiletest.OutOfProcess` starts your test binary again as a plugin and returns a `tile.Tile` that talks to it over the real protocol, so encoding, facets, handles, and cancellation are all exercised.

```go
func TestMain(m *testing.M) {
	tiletest.ServeIfChild(Tile{}) // serves and exits when started by OutOfProcess
	os.Exit(m.Run())
}

func TestBothWays(t *testing.T) {
	for name, tl := range map[string]tile.Tile{"in process": Tile{}, "out of process": tiletest.OutOfProcess(t)} {
		t.Run(name, func(t *testing.T) { /* open, execute, check frames */ })
	}
}
```

The [tutorial plugin's test](https://github.com/arunksoman/tessera/blob/main/docs/examples/tess-kv/main_test.go) is a complete example.

## Run the conformance suite

The suite targets SQL databases today: it creates fixture tables and runs SQL. Supply the database-specific parts in a `Harness`, then call `tiletest.Run` once per way of reaching the tile:

```go
func TestConformance(t *testing.T)             { tiletest.Run(t, harness(t, Tile{})) }
func TestConformanceOutOfProcess(t *testing.T) { tiletest.Run(t, harness(t, tiletest.OutOfProcess(t))) }

func harness(t *testing.T, tl tile.Tile) tiletest.Harness {
	return tiletest.Harness{
		Open:   func(t *testing.T) tile.Conn { return open(t, tl) },
		Schema: tile.ObjectRef{}.Child("schema", "main"),
		ManyRowsSQL: func(n int) string {
			return fmt.Sprintf("SELECT i, md5(i::text) FROM generate_series(1, %d) i", n)
		},
		SleepSQL: "SELECT pg_sleep(30)",
		ArgMark:  "$1",
		RunningSQL: func(marker string) string {
			return "SELECT count(*) FROM pg_stat_activity WHERE query LIKE '%" + marker + "%' AND pid <> pg_backend_pid()"
		},
		HostileTable: func(t *testing.T, s tile.Session) string { /* create and return a table with a hostile name */ },
	}
}
```

The database must hold the fixture tables from [`test/fixtures`](https://github.com/arunksoman/tessera/tree/main/test/fixtures); copy the script for the closest dialect.

| Harness field | Purpose |
|---|---|
| `Open` | Opens a connection to the fixture database |
| `Schema` | The tree path of the fixture tables |
| `ManyRowsSQL(n)` | Returns at least `n` rows of 2 columns, slowly enough to cancel |
| `SleepSQL` | Runs long enough to be cancelled |
| `ArgMark` | The placeholder for the first bound argument, such as `$1` or `?` |
| `RunningSQL(marker)` | Counts running statements containing `marker`, to prove cancel stopped the server. `nil` for embedded stores whose statements run in the calling goroutine. |
| `InexactDecimal` | Skips the exact-decimal check, for stores that keep decimals as floats |
| `HostileTable` | Creates a table whose name is full of quoting characters, and returns the name |
| `CreateRoutine` | Creates `tt_add(a, b)`; `nil` skips the routine tests |
| `Design` | Sets up the structure tests: `Create`, `IntType`, `TriggerAction`; `nil` skips them |

## What the suite checks

| Case | Checks |
|---|---|
| `Stream` | Frame order, batch sizes, and that results stream |
| `Lossless` | Big integers, decimals, and times keep every digit |
| `BindArgs` | Arguments are bound, not spliced |
| `Summary` | Rows affected for writes |
| `SyntaxError` | Errors come back as errors, and the session survives |
| `CancelStopsServer` | Cancelling stops the statement on the server |
| `EarlyCloseIsFast` | Closing a stream early does not read the remaining rows |
| `ReadOnlySession` | Read-only sessions refuse writes on the server |
| `SessionState` | Session state does not leak through the pool |
| `TreePaging`, `Describe`, `TreeRoutines`, `Keys` | The tree pages correctly and describes objects and keys |
| `HostileIdentifiers`, `BrowseHostile` | Quoting survives hostile names |
| `BrowseLayout`, `BrowsePaging`, `BrowseFilter`, `BrowseKeyless` | Browsing with keyset or offset paging, sort, and filters |
| `PlanPurity`, `PlanGuards` | Plans have no side effects, and stale edits become conflicts |
| `Explain`, `SessionPing` | Optional facets work when present |
| `Design`, `CreateScript`, `ObjectActions` | Structure changes, create scripts, rename, truncate, and drop |

Run one case with `go test -run 'TestConformance/CancelStopsServer'`.

## Test your language hard

Your `Language` decides what the safety gate asks about. Table-test `Split` and `Classify` with comments, quotes, nested bodies, and writes hidden in reads, and fuzz them: they must never panic, and a mistake in comment or quote handling is a safety bug.

```go
func FuzzSplit(f *testing.F) {
	f.Add("SELECT ';' -- ;\n; DELETE FROM t")
	f.Fuzz(func(t *testing.T, s string) {
		for _, sp := range (language{}).Split(s) {
			if sp.Start < 0 || sp.End > len(s) || sp.Start > sp.End {
				t.Fatalf("bad span %v for %q", sp, s)
			}
		}
	})
}
```
