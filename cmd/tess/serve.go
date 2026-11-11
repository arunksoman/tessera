package main

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/arunksoman/tessera/internal/cli"
	"github.com/arunksoman/tessera/internal/server"
	tessversion "github.com/arunksoman/tessera/internal/version"
	"github.com/arunksoman/tessera/web"
)

// serve runs tess serve. Exit codes follow the CLI: 0 ok, 1 runtime error, 2 usage error.
func serve(ctx context.Context, app *cli.App, args []string) int {
	stdout, stderr := app.Stdout, app.Stderr
	fs := flag.NewFlagSet("tess serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfg := server.Config{Assets: web.Build()}
	fs.StringVar(&cfg.Addr, "addr", "127.0.0.1:0", "listen on `HOST:PORT`; port 0 picks a free one")
	fs.StringVar(&cfg.CertFile, "tls-cert", "", "TLS certificate `FILE` (PEM)")
	fs.StringVar(&cfg.KeyFile, "tls-key", "", "TLS private key `FILE` (PEM)")
	fs.BoolVar(&cfg.BehindProxy, "behind-proxy", false, "a TLS-terminating proxy sits in front, so plain HTTP on a non-loopback address is allowed")
	fs.Func("allow-host", "also accept `NAME` in the Host header; repeatable", func(v string) error {
		cfg.AllowedHosts = append(cfg.AllowedHosts, v)
		return nil
	})
	noBrowser := fs.Bool("no-browser", false, "print the link without opening a browser")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "tess: serve takes no arguments")
		fs.Usage()
		return 2
	}
	e, closeEngine, err := app.OpenEngine()
	if err != nil {
		fmt.Fprintf(stderr, "tess: %v\n", err)
		return 1
	}
	defer closeEngine()
	cfg.Engine = e
	cfg.Version = tessversion.Describe(app.Version)
	s, err := server.New(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "tess: %v\n", err)
		return 1
	}
	ready := func(link string) {
		if err := openBrowser(link); err != nil {
			fmt.Fprintf(stderr, "tess: could not open a browser (%v); open the link above\n", err)
		}
	}
	if *noBrowser {
		ready = nil
	}
	if err := s.Run(ctx, stdout, ready); err != nil {
		fmt.Fprintf(stderr, "tess: %v\n", err)
		return 1
	}
	return 0
}
