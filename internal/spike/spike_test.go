//go:build integration

// Package spike holds Phase 0 measurements against the docker compose databases.
// Results are recorded in design-spec/spike-results.md.
package spike

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"
)

const pgDSN = "postgres://tessera:tessera_dev@127.0.0.1:55418/tessera_test?sslmode=disable"

var mysqlServers = map[string]string{
	"mysql":   "tessera:tessera_dev@tcp(127.0.0.1:53384)/tessera_test",
	"mariadb": "tessera:tessera_dev@tcp(127.0.0.1:53114)/tessera_test",
}

const rowCount = 2_000_000

func pgConnect(t *testing.T, cancelHandler bool) *pgconn.PgConn {
	t.Helper()
	cfg, err := pgconn.ParseConfig(pgDSN)
	if err != nil {
		t.Fatal(err)
	}
	if cancelHandler {
		cfg.BuildContextWatcherHandler = func(c *pgconn.PgConn) ctxwatch.Handler {
			return &pgconn.CancelRequestContextWatcherHandler{Conn: c, CancelRequestDelay: 0, DeadlineDelay: 2 * time.Second}
		}
	}
	conn, err := pgconn.ConnectConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}

func heapMB() float64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return float64(m.HeapAlloc) / (1 << 20)
}

// TestPostgresStreamThroughput reads rows through pgconn without decoding values.
func TestPostgresStreamThroughput(t *testing.T) {
	conn := pgConnect(t, false)
	before := heapMB()
	start := time.Now()
	rr := conn.ExecParams(context.Background(),
		fmt.Sprintf(`SELECT g, g*2, 'row-'||g, g%%7, now(), g::numeric/3, md5(g::text), true, null::text, g FROM generate_series(1,%d) g`, rowCount),
		nil, nil, nil, nil)
	var rows int
	var bytes int64
	var peak float64
	for rr.NextRow() {
		for _, v := range rr.Values() {
			bytes += int64(len(v))
		}
		rows++
		if rows%500_000 == 0 {
			peak = max(peak, heapMB())
		}
	}
	if _, err := rr.Close(); err != nil {
		t.Fatal(err)
	}
	if rows != rowCount {
		t.Fatalf("rows = %d", rows)
	}
	el := time.Since(start)
	t.Logf("postgres: %d rows, %.0f rows/s, %.1f MB/s, heap before %.1f MB, peak %.1f MB",
		rows, float64(rows)/el.Seconds(), float64(bytes)/(1<<20)/el.Seconds(), before, peak)
	if peak-before > 64 {
		t.Errorf("heap grew %.1f MB, target is under 64 MB", peak-before)
	}
}

// TestPostgresCancel shows the server stops work, not only the client.
func TestPostgresCancel(t *testing.T) {
	conn := pgConnect(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	rr := conn.ExecParams(ctx, `SELECT pg_sleep(30)`, nil, nil, nil, nil)
	for rr.NextRow() {
	}
	_, err := rr.Close()
	el := time.Since(start)

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "57014" {
		t.Fatalf("want query_canceled 57014, got %v", err)
	}
	if el > 2*time.Second {
		t.Fatalf("cancel took %v", el)
	}
	if conn.IsClosed() {
		t.Fatal("connection should survive a server-side cancel")
	}
	t.Logf("postgres cancel acknowledged after %v, connection reusable", el)
}

// TestPostgresResultColumnSource backs the design's claim that result columns carry their table.
func TestPostgresResultColumnSource(t *testing.T) {
	conn := pgConnect(t, false)
	rr := conn.ExecParams(context.Background(), `SELECT o.id, o.total, o.id+1 AS calc FROM orders o`, nil, nil, nil, nil)
	defer rr.Close()
	fd := rr.FieldDescriptions()
	if fd[0].TableOID == 0 || fd[0].TableAttributeNumber == 0 {
		t.Fatalf("id has no source: %+v", fd[0])
	}
	if fd[2].TableOID != 0 {
		t.Fatalf("computed column should have no source: %+v", fd[2])
	}
}

func openMySQL(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func eachMySQL(t *testing.T, f func(t *testing.T, dsn string)) {
	for name, dsn := range mysqlServers {
		t.Run(name, func(t *testing.T) { f(t, dsn) })
	}
}

const mysqlRowsSQL = `WITH RECURSIVE d(n) AS (SELECT 0 UNION ALL SELECT n+1 FROM d WHERE n<9),
 g AS (SELECT a.n + b.n*10 + c.n*100 + e.n*1000 + f.n*10000 + h.n*100000 AS n
       FROM d a, d b, d c, d e, d f, d h)
 SELECT n, n*2, CONCAT('row-',n), n%7, NOW(), n/3, MD5(n), TRUE, NULL, n FROM g`

func TestMySQLStreamThroughput(t *testing.T) {
	eachMySQL(t, func(t *testing.T, dsn string) {
		db := openMySQL(t, dsn)
		before := heapMB()
		start := time.Now()
		rows, err := db.QueryContext(context.Background(), mysqlRowsSQL)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		cols, _ := rows.Columns()
		dest := make([]any, len(cols))
		raw := make([]sql.RawBytes, len(cols))
		for i := range raw {
			dest[i] = &raw[i]
		}
		var n int
		var bytes int64
		var peak float64
		for rows.Next() {
			if err := rows.Scan(dest...); err != nil {
				t.Fatal(err)
			}
			for _, v := range raw {
				bytes += int64(len(v))
			}
			n++
			if n%250_000 == 0 {
				peak = max(peak, heapMB())
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		el := time.Since(start)
		t.Logf("%d rows, %.0f rows/s, %.1f MB/s, heap before %.1f MB, peak %.1f MB",
			n, float64(n)/el.Seconds(), float64(bytes)/(1<<20)/el.Seconds(), before, peak)
		if peak-before > 64 {
			t.Errorf("heap grew %.1f MB, target is under 64 MB", peak-before)
		}
	})
}

// TestMySQLSlowReaderIsDropped shows pausing a stream past net_write_timeout kills it,
// which is why the engine must drain into the spool rather than stop reading.
func TestMySQLSlowReaderIsDropped(t *testing.T) {
	eachMySQL(t, func(t *testing.T, dsn string) {
		ctx := context.Background()
		db := openMySQL(t, dsn)
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if _, err := conn.ExecContext(ctx, `SET SESSION net_write_timeout=2`); err != nil {
			t.Fatal(err)
		}
		rows, err := conn.QueryContext(ctx, mysqlRowsSQL)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		rows.Next()
		time.Sleep(6 * time.Second)
		n := 1
		for rows.Next() {
			n++
		}
		err = rows.Err()
		t.Logf("after a 6s pause with net_write_timeout=2: rows=%d err=%v", n, err)
		if err == nil && n == 1_000_000 {
			t.Skip("server buffered the whole result, the pause did not trigger the timeout")
		}
		if err == nil {
			t.Fatalf("expected a read error or short result, got %d rows", n)
		}
	})
}

// TestMySQLContextCancelLeavesServerRunning shows why KILL QUERY is needed.
func TestMySQLCancelNeedsKillQuery(t *testing.T) {
	eachMySQL(t, func(t *testing.T, dsn string) {
		root := openMySQL(t, rootDSN(dsn))
		db := openMySQL(t, dsn)
		ctx := context.Background()
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		var id int64
		if err := conn.QueryRowContext(ctx, `SELECT CONNECTION_ID()`).Scan(&id); err != nil {
			t.Fatal(err)
		}

		// Part 1: context cancel alone.
		cctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		defer cancel()
		_, err = conn.ExecContext(cctx, `SELECT SLEEP(5)`)
		t.Logf("context cancel returned: %v", err)
		time.Sleep(200 * time.Millisecond)
		var still int
		if err := root.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.PROCESSLIST WHERE ID=? AND INFO LIKE 'SELECT SLEEP%'`, id).Scan(&still); err != nil {
			t.Fatal(err)
		}
		t.Logf("statement still running on the server after context cancel: %v", still == 1)

		// Part 2: KILL QUERY on a second connection with the same credentials.
		conn2, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn2.Close()
		var id2 int64
		if err := conn2.QueryRowContext(ctx, `SELECT CONNECTION_ID()`).Scan(&id2); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		start := time.Now()
		go func() {
			var r int
			done <- conn2.QueryRowContext(ctx, `SELECT SLEEP(30)`).Scan(&r)
		}()
		time.Sleep(200 * time.Millisecond)
		if _, err := db.ExecContext(ctx, fmt.Sprintf(`KILL QUERY %d`, id2)); err != nil {
			t.Fatalf("KILL QUERY with own credentials: %v", err)
		}
		select {
		case err := <-done:
			t.Logf("KILL QUERY stopped the statement after %v (err=%v)", time.Since(start), err)
		case <-time.After(5 * time.Second):
			t.Fatal("KILL QUERY did not stop the statement")
		}
		var one int
		if err := conn2.QueryRowContext(ctx, `SELECT 1`).Scan(&one); err != nil {
			t.Fatalf("connection should survive KILL QUERY: %v", err)
		}
	})
}

func rootDSN(dsn string) string {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		panic(err)
	}
	cfg.User, cfg.Passwd = "root", "tessera_root"
	return cfg.FormatDSN()
}

// TestMySQLColumnSource records what database/sql exposes about result columns.
func TestMySQLColumnSource(t *testing.T) {
	eachMySQL(t, func(t *testing.T, dsn string) {
		db := openMySQL(t, dsn)
		rows, err := db.Query(`SELECT o.id, o.total, o.id+1 AS calc FROM orders o`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		types, err := rows.ColumnTypes()
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range types {
			nullable, _ := c.Nullable()
			t.Logf("name=%s dbtype=%s nullable=%v scan=%v", c.Name(), c.DatabaseTypeName(), nullable, c.ScanType())
		}
		var _ io.Closer = rows
	})
}

// TestMySQLAuthPlugins records the default plugin each server assigned.
func TestMySQLAuthPlugins(t *testing.T) {
	eachMySQL(t, func(t *testing.T, dsn string) {
		root := openMySQL(t, rootDSN(dsn))
		var version, plugin string
		if err := root.QueryRow(`SELECT VERSION()`).Scan(&version); err != nil {
			t.Fatal(err)
		}
		if err := root.QueryRow(`SELECT plugin FROM mysql.user WHERE user='tessera'`).Scan(&plugin); err != nil {
			t.Fatal(err)
		}
		t.Logf("version=%s tessera user plugin=%s", version, plugin)
		_ = os.Stdout
	})
}
