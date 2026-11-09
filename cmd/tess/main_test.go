package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arunksoman/tessera/internal/cli"
)

func runTess(ctx context.Context, stdout io.Writer, args ...string) (int, string) {
	var errb bytes.Buffer
	app := &cli.App{Stdin: strings.NewReader(""), Stdout: stdout, Stderr: &errb, Version: "test"}
	code := run(ctx, app, args)
	return code, errb.String()
}

func TestCLICommandsStillDispatch(t *testing.T) {
	var out bytes.Buffer
	if code, _ := runTess(context.Background(), &out, "version"); code != 0 || out.String() != "tess test\n" {
		t.Fatalf("%d %q", code, out.String())
	}
	out.Reset()
	if code, _ := runTess(context.Background(), &out, "help"); code != 0 || !strings.Contains(out.String(), "tess serve") {
		t.Fatalf("help lacks serve: %q", out.String())
	}
}

func TestServeRefusals(t *testing.T) {
	cases := []struct {
		args []string
		code int
		msg  string
	}{
		{[]string{"serve", "--addr", "0.0.0.0:0"}, 1, "refusing to serve plain HTTP"},
		{[]string{"serve", "--tls-cert", "c.pem"}, 1, "--tls-key"},
		{[]string{"serve", "extra"}, 2, "serve takes no arguments"},
		{[]string{"serve", "--nope"}, 2, "flag provided but not defined"},
		{[]string{"serve", "-h"}, 0, "-allow-host NAME"},
	}
	for _, c := range cases {
		code, msg := runTess(context.Background(), io.Discard, c.args...)
		if code != c.code || !strings.Contains(msg, c.msg) {
			t.Errorf("%v: %d %q", c.args, code, msg)
		}
	}
}

// stubBrowser records links instead of opening a real browser.
func stubBrowser(t *testing.T) chan string {
	opened := make(chan string, 1)
	old := openBrowser
	openBrowser = func(link string) error { opened <- link; return nil }
	t.Cleanup(func() { openBrowser = old })
	return opened
}

func TestServeOpensBrowser(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		open bool
	}{
		{"loopback opens", []string{"serve", "--addr", "127.0.0.1:0"}, true},
		{"--no-browser", []string{"serve", "--addr", "127.0.0.1:0", "--no-browser"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			opened := stubBrowser(t)
			ctx, cancel := context.WithCancel(context.Background())
			pr, pw := io.Pipe()
			done := make(chan int, 1)
			go func() { code, _ := runTess(ctx, pw, c.args...); pw.Close(); done <- code }()
			line, err := bufio.NewReader(pr).ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			go io.Copy(io.Discard, pr)
			link := strings.TrimSpace(line[strings.Index(line, "http://"):])
			if c.open {
				select {
				case got := <-opened:
					if got != link {
						t.Fatalf("opened %q, printed %q", got, link)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("browser not opened")
				}
			}
			cancel()
			if code := <-done; code != 0 {
				t.Fatalf("exit %d", code)
			}
			select {
			case got := <-opened:
				if !c.open {
					t.Fatalf("opened %q despite --no-browser", got)
				}
			default:
			}
		})
	}
}

func TestServeBrowserFailureIsNotFatal(t *testing.T) {
	old := openBrowser
	openBrowser = func(string) error { return errors.New("no display") }
	t.Cleanup(func() { openBrowser = old })
	ctx, cancel := context.WithCancel(context.Background())
	pr, pw := io.Pipe()
	done := make(chan struct{})
	var code int
	var msg string
	go func() { code, msg = runTess(ctx, pw, "serve", "--addr", "127.0.0.1:0"); pw.Close(); close(done) }()
	// The second line follows the attempt to open the browser.
	br := bufio.NewReader(pr)
	br.ReadString('\n')
	br.ReadString('\n')
	go io.Copy(io.Discard, pr)
	cancel()
	<-done
	if code != 0 || !strings.Contains(msg, "could not open a browser") {
		t.Fatalf("%d %q", code, msg)
	}
}

func TestServeStartsAndStops(t *testing.T) {
	stubBrowser(t)
	ctx, cancel := context.WithCancel(context.Background())
	pr, pw := io.Pipe()
	done := make(chan int, 1)
	go func() {
		code, msg := runTess(ctx, pw, "serve", "--addr", "127.0.0.1:0")
		if msg != "" {
			t.Log(msg)
		}
		pw.Close()
		done <- code
	}()
	line, err := bufio.NewReader(pr).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	go io.Copy(io.Discard, pr)
	link := strings.TrimSpace(line[strings.Index(line, "http://"):])

	jar, _ := cookiejar.New(nil)
	resp, err := (&http.Client{Jar: jar, Timeout: 5 * time.Second}).Get(link)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "<title>Tessera</title>") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop")
	}
}

// TestMain keeps tests away from the real config directory and OS keychain.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "tess-main-test")
	if err != nil {
		panic(err)
	}
	key := filepath.Join(dir, "master.key")
	if err := os.WriteFile(key, bytes.Repeat([]byte{7}, 32), 0o600); err != nil {
		panic(err)
	}
	os.Setenv("TESSERA_CONFIG_DIR", dir)
	os.Setenv("TESSERA_MASTER_KEY_FILE", key)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
