// Package cli implements tess's non-interactive commands.
package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"os/user"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/arunksoman/tessera/internal/engine"
	"github.com/arunksoman/tessera/internal/ioformat"
	"github.com/arunksoman/tessera/internal/secrets"
	"github.com/arunksoman/tessera/internal/store"
	"github.com/arunksoman/tessera/tile"
)

const usage = `tess: a universal database client

Usage:
  tess profile add NAME --tile postgres|mysql [--host H] [--port N] [--user U] [--database D]
                        [--tls MODE] [--env production] [--read-only] [--password-stdin]
  tess profile list
  tess profile rm NAME
  tess connect NAME
  tess query -c NAME [-f table|csv|json] [--yes] [--raw] [--timeout 30s] [SQL]
  tess history -c NAME [-n 20]
  tess audit verify
  tess version
`

// App holds what commands need. Tests swap the streams.
type App struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	Tiles          []tile.Tile
	Version        string
	// TTY reports whether Stdin can answer a confirmation.
	TTY bool
}

// Exit codes: 0 ok, 1 runtime error, 2 usage error, 3 refused by the safety gate.
const (
	exitOK = iota
	exitError
	exitUsage
	exitRefused
)

// Run returns the process exit code.
func (a *App) Run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(a.Stderr, usage)
		return exitUsage
	}
	var err error
	switch args[0] {
	case "version", "--version":
		fmt.Fprintf(a.Stdout, "tess %s\n", a.Version)
	case "help", "--help", "-h":
		fmt.Fprint(a.Stdout, usage)
	case "profile":
		err = a.profile(args[1:])
	case "connect":
		err = a.connect(args[1:])
	case "query":
		err = a.query(args[1:])
	case "history":
		err = a.history(args[1:])
	case "audit":
		err = a.audit(args[1:])
	default:
		err = usageErr("unknown command %q", args[0])
	}
	return a.finish(err)
}

type errUsage struct{ msg string }

func (e errUsage) Error() string { return e.msg }

func usageErr(format string, args ...any) error { return errUsage{fmt.Sprintf(format, args...)} }

func (a *App) finish(err error) int {
	var ue errUsage
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, flag.ErrHelp):
		return exitOK
	case errors.As(err, &ue):
		fmt.Fprintf(a.Stderr, "tess: %s\n\n%s", ue.msg, usage)
		return exitUsage
	case errors.Is(err, engine.ErrReadOnly), errors.Is(err, engine.ErrNeedsConfirm), errors.Is(err, engine.ErrDeclined):
		fmt.Fprintf(a.Stderr, "tess: refused: %v\n", err)
		return exitRefused
	}
	fmt.Fprintf(a.Stderr, "tess: %v\n", err)
	return exitError
}

func (a *App) flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	return fs
}

// parse accepts flags before and after positional arguments, which flag.Parse alone does not.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return positional, nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}

func (a *App) openStore() (*store.Store, *secrets.Vault, error) {
	dir, err := store.Dir()
	if err != nil {
		return nil, nil, err
	}
	st, err := store.Open(dir)
	if err != nil {
		return nil, nil, err
	}
	key, err := secrets.MasterKey(dir)
	if err != nil {
		st.Close()
		return nil, nil, err
	}
	vault, err := secrets.OpenVault(dir, key)
	if err != nil {
		st.Close()
		return nil, nil, err
	}
	return st, vault, nil
}

func actor() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return "unknown"
}

func (a *App) profile(args []string) error {
	if len(args) == 0 {
		return usageErr("profile needs add, list, or rm")
	}
	switch args[0] {
	case "add":
		return a.profileAdd(args[1:])
	case "list":
		st, _, err := a.openStore()
		if err != nil {
			return err
		}
		defer st.Close()
		ps, err := st.Profiles()
		if err != nil {
			return err
		}
		for _, p := range ps {
			tag := ""
			if p.Env != "" {
				tag += " [" + p.Env + "]"
			}
			if p.ReadOnly {
				tag += " [read-only]"
			}
			fmt.Fprintf(a.Stdout, "%s\t%s\t%s%s\n", p.Name, p.Tile, p.Values["host"], tag)
		}
		return nil
	case "rm":
		if len(args) != 2 {
			return usageErr("profile rm needs a name")
		}
		st, vault, err := a.openStore()
		if err != nil {
			return err
		}
		defer st.Close()
		if err := st.DeleteProfile(args[1]); err != nil {
			return err
		}
		return vault.Delete(engine.SecretName(args[1], "password"))
	}
	return usageErr("unknown profile command %q", args[0])
}

func (a *App) profileAdd(args []string) error {
	fs := a.flags("profile add")
	tileID := fs.String("tile", "", "postgres or mysql")
	host := fs.String("host", "", "host name or address")
	socket := fs.String("socket", "", "Unix socket (MySQL)")
	port := fs.String("port", "", "port")
	dbUser := fs.String("user", "", "user")
	database := fs.String("database", "", "database")
	tlsMode := fs.String("tls", "", "sslmode (PostgreSQL) or TLS mode (MySQL)")
	envTag := fs.String("env", "", `environment tag; "production" adds confirmations`)
	readOnly := fs.Bool("read-only", false, "refuse writes")
	pwStdin := fs.Bool("password-stdin", false, "read the password from standard input")
	rest, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return usageErr("profile add needs one name")
	}
	var known bool
	for _, t := range a.Tiles {
		known = known || t.Manifest().ID == *tileID
	}
	if !known {
		return usageErr("--tile must be postgres or mysql, got %q", *tileID)
	}
	values := map[string]string{}
	for k, v := range map[string]string{"host": *host, "socket": *socket, "port": *port, "user": *dbUser, "database": *database} {
		if v != "" {
			values[k] = v
		}
	}
	if *tlsMode != "" {
		key := "sslmode"
		if *tileID == "mysql" {
			key = "tlsmode"
		}
		values[key] = *tlsMode
	}
	st, vault, err := a.openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	if *pwStdin {
		pw, err := a.readSecret()
		if err != nil {
			return err
		}
		if err := vault.Set(engine.SecretName(rest[0], "password"), pw); err != nil {
			return err
		}
	}
	return st.SaveProfile(store.Profile{Name: rest[0], Tile: *tileID, Env: *envTag, ReadOnly: *readOnly, Values: values})
}

// readSecret reads one line, without echo on a terminal.
func (a *App) readSecret() ([]byte, error) {
	if f, ok := a.Stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(a.Stderr, "Password: ")
		defer fmt.Fprintln(a.Stderr)
		return term.ReadPassword(int(f.Fd()))
	}
	line, err := bufio.NewReader(a.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return []byte(strings.TrimRight(line, "\r\n")), nil
}

func (a *App) open(ctx context.Context, name string) (*engine.Conn, func(), error) {
	st, vault, err := a.openStore()
	if err != nil {
		return nil, nil, err
	}
	c, err := engine.New(st, vault, actor(), a.Tiles...).Open(ctx, name)
	if err != nil {
		st.Close()
		return nil, nil, err
	}
	return c, func() { c.Close(); st.Close() }, nil
}

func (a *App) connect(args []string) error {
	if len(args) != 1 {
		return usageErr("connect needs a profile name")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	start := time.Now()
	c, done, err := a.open(ctx, args[0])
	if err != nil {
		return err
	}
	defer done()
	if err := c.Ping(ctx); err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "connected to %s in %v\n", args[0], time.Since(start).Round(time.Millisecond))
	return nil
}

func (a *App) query(args []string) error {
	fs := a.flags("query")
	name := fs.String("c", "", "profile name")
	format := fs.String("f", "table", "table, csv, or json")
	yes := fs.Bool("yes", false, "answer yes to confirmations")
	raw := fs.Bool("raw", false, "keep spreadsheet formulas in CSV instead of neutralizing them")
	timeout := fs.Duration("timeout", 0, "cancel after this long")
	rest, err := parse(fs, args)
	if err != nil {
		return err
	}
	if *name == "" {
		return usageErr("query needs -c PROFILE")
	}
	var text string
	fromStdin := false
	switch len(rest) {
	case 0:
		b, err := io.ReadAll(a.Stdin)
		if err != nil {
			return err
		}
		text, fromStdin = string(b), true
	case 1:
		text = rest[0]
	default:
		return usageErr("query takes one SQL argument: quote it")
	}
	newWriter, err := writerFor(*format, *raw)
	if err != nil {
		return err
	}

	// Ctrl-C cancels the statement on the server, not only this process.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}
	c, done, err := a.open(ctx, *name)
	if err != nil {
		return err
	}
	defer done()

	out := bufio.NewWriter(a.Stdout)
	defer out.Flush()
	var w ioformat.Writer
	sink := func(_ int, f tile.Frame) error {
		switch {
		case f.Schema != nil:
			w = newWriter(out)
			return w.Begin(f.Schema.Fields)
		case f.Batch != nil:
			cols := len(f.Batch.Cells) / max(f.Batch.Rows, 1)
			for r := range f.Batch.Rows {
				if err := w.Row(f.Batch.Cells[r*cols : (r+1)*cols]); err != nil {
					return err
				}
			}
		case f.Done != nil:
			if w != nil {
				err := w.End()
				w = nil
				return err
			}
			if f.Done.HasAffected {
				fmt.Fprintf(a.Stderr, "%d rows affected\n", f.Done.RowsAffected)
			}
		}
		return nil
	}
	return c.Run(ctx, text, engine.RunOptions{Confirm: a.confirmer(*yes, fromStdin, *name)}, sink)
}

func writerFor(format string, raw bool) (func(io.Writer) ioformat.Writer, error) {
	switch format {
	case "table":
		return ioformat.NewTable, nil
	case "csv":
		return func(w io.Writer) ioformat.Writer { return ioformat.NewCSV(w, !raw) }, nil
	case "json":
		return ioformat.NewJSON, nil
	}
	return nil, usageErr("-f must be table, csv, or json, got %q", format)
}

// confirmer returns nil when nobody can answer, so the gate refuses rather than guesses.
func (a *App) confirmer(yes, stdinUsed bool, profile string) engine.Confirm {
	if yes {
		return func(p engine.Prompt) bool {
			fmt.Fprintf(a.Stderr, "confirmed by --yes: %s\n", p.Class.Reason)
			return true
		}
	}
	if stdinUsed || !a.TTY {
		return nil
	}
	return func(p engine.Prompt) bool {
		fmt.Fprintf(a.Stderr, "%s\n  %s\n", p.Class.Reason, p.Statement)
		want := "yes"
		if p.Env == engine.EnvProduction {
			fmt.Fprintf(a.Stderr, "This connection is tagged PRODUCTION.\n")
			want = profile
		}
		fmt.Fprintf(a.Stderr, "Type %q to run it: ", want)
		line, _ := bufio.NewReader(a.Stdin).ReadString('\n')
		return strings.TrimSpace(line) == want
	}
}

func (a *App) history(args []string) error {
	fs := a.flags("history")
	name := fs.String("c", "", "profile name")
	n := fs.Int("n", 20, "entries to show")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if *name == "" {
		return usageErr("history needs -c PROFILE")
	}
	st, _, err := a.openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	entries, err := st.History(*name, *n)
	if err != nil {
		return err
	}
	for _, e := range entries {
		status := "ok"
		if e.Error != "" {
			status = "error"
		}
		fmt.Fprintf(a.Stdout, "%s\t%s\t%dms\t%s\n", e.At.Format(time.DateTime), status, e.ElapsedMS, strings.Join(strings.Fields(e.Statement), " "))
	}
	return nil
}

func (a *App) audit(args []string) error {
	if len(args) != 1 || args[0] != "verify" {
		return usageErr("audit needs verify")
	}
	st, _, err := a.openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	n, err := st.VerifyAudit()
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "audit log intact: %d entries\n", n)
	return nil
}
