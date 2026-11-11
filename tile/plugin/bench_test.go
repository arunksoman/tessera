package plugin

import (
	"context"
	"errors"
	"io"
	"strconv"
	"testing"

	"github.com/arunksoman/tessera/tile"
)

// benchRows is how many rows each benchmark iteration reads, in batches of 1000 like the SQL
// tiles send.
const benchRows = 100_000

func drain(b *testing.B, s tile.Session) {
	st, err := s.Execute(context.Background(), tile.Request{Text: "rows " + strconv.Itoa(benchRows)})
	if err != nil {
		b.Fatal(err)
	}
	defer st.Close()
	for {
		f, err := st.Next(context.Background())
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			b.Fatal(err)
		}
		_ = f
	}
}

func benchSession(b *testing.B, conn tile.Conn) {
	s, err := conn.Session(context.Background(), tile.SessionOptions{})
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		drain(b, s)
	}
	b.ReportMetric(float64(benchRows*b.N)/b.Elapsed().Seconds(), "rows/s")
}

func BenchmarkStreamInProcess(b *testing.B) {
	conn, err := (&fakeTile{batch: 1000}).Open(context.Background(), tile.ConnConfig{}, testEnv)
	if err != nil {
		b.Fatal(err)
	}
	benchSession(b, conn)
}

func BenchmarkStreamOutOfProcess(b *testing.B) {
	hostR, pluginW := io.Pipe()
	pluginR, hostW := io.Pipe()
	go ServeConn(pluginR, pluginW, &fakeTile{batch: 1000})
	defer hostW.Close()
	conn, err := NewClient(hostR, hostW).Open(context.Background(), tile.ConnConfig{}, testEnv)
	if err != nil {
		b.Fatal(err)
	}
	benchSession(b, conn)
}
