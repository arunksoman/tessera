package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strings"

	"github.com/arunksoman/tessera/internal/plugins"
	"github.com/arunksoman/tessera/internal/store"
)

func (a *App) plugin(args []string) error {
	if len(args) == 0 {
		return usageErr("plugin needs install, list, remove, verify, trust, untrust, keys, keygen, or sign")
	}
	switch args[0] {
	case "install":
		return a.pluginInstall(args[1:])
	case "list":
		return a.pluginList()
	case "remove", "rm":
		if len(args) != 2 {
			return usageErr("plugin remove needs a name")
		}
		root, err := pluginRoot()
		if err != nil {
			return err
		}
		if err := plugins.Remove(root, args[1]); err != nil {
			return err
		}
		fmt.Fprintf(a.Stdout, "removed plugin %s; profiles that use it stay saved but cannot connect\n", args[1])
		return nil
	case "verify":
		return a.pluginVerify(args[1:])
	case "trust":
		return a.pluginTrust(args[1:])
	case "untrust":
		if len(args) != 2 {
			return usageErr("plugin untrust needs a publisher name")
		}
		return a.editPluginSettings(func(ps *store.PluginSettings) error {
			if _, ok := ps.TrustedKeys[args[1]]; !ok {
				return fmt.Errorf("no trusted publisher %q", args[1])
			}
			delete(ps.TrustedKeys, args[1])
			return nil
		})
	case "keys":
		return a.pluginKeys()
	case "keygen":
		return a.pluginKeygen(args[1:])
	case "sign":
		return a.pluginSign(args[1:])
	}
	return usageErr("unknown plugin command %q", args[0])
}

func pluginRoot() (string, error) {
	dir, err := store.Dir()
	if err != nil {
		return "", err
	}
	return plugins.Dir(dir), nil
}

func (a *App) pluginSettings() (store.PluginSettings, error) {
	st, _, err := a.openStore()
	if err != nil {
		return store.PluginSettings{}, err
	}
	defer st.Close()
	return st.PluginSettings()
}

func (a *App) editPluginSettings(edit func(*store.PluginSettings) error) error {
	st, _, err := a.openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	ps, err := st.PluginSettings()
	if err != nil {
		return err
	}
	if err := edit(&ps); err != nil {
		return err
	}
	return st.SavePluginSettings(ps)
}

func (a *App) pluginInstall(args []string) error {
	fs := a.flags("plugin install")
	var opt plugins.Options
	fs.StringVar(&opt.From, "from", "", "`SOURCE`: github:OWNER/REPO[@TAG], an https URL of a plugin descriptor, or a local path")
	fs.StringVar(&opt.SHA256, "sha256", "", "require the binary to have this SHA-256 `HEX`")
	fs.BoolVar(&opt.AllowUnsigned, "allow-unsigned", false, "install a plugin no trusted publisher signed")
	fs.BoolVar(&opt.Force, "force", false, "replace an installed plugin of the same name")
	yes := fs.Bool("yes", false, "install without asking")
	rest, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return usageErr("plugin install needs one name")
	}
	name := rest[0]
	if !*yes && !a.TTY {
		return errors.New("refusing to install a plugin without confirmation: run in a terminal or pass --yes")
	}
	st, _, err := a.openStore()
	if err != nil {
		return err
	}
	ps, err := st.PluginSettings()
	root := plugins.Dir(st.Dir())
	st.Close()
	if err != nil {
		return err
	}
	in := &plugins.Installer{Root: root, TrustedKeys: ps.TrustedKeys, Sources: ps.Sources, Indexes: ps.Indexes}
	for _, t := range a.Tiles {
		in.Builtins = append(in.Builtins, t.Manifest().ID)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	p, err := in.Resolve(ctx, name, opt)
	if err != nil {
		return err
	}
	w := a.Stderr
	fmt.Fprintf(w, "plugin    %s %s\n", p.Name, p.Descriptor.Version)
	if p.Descriptor.Description != "" {
		fmt.Fprintf(w, "about     %s\n", oneLine(p.Descriptor.Description))
	}
	if p.Descriptor.Homepage != "" {
		fmt.Fprintf(w, "homepage  %s\n", oneLine(p.Descriptor.Homepage))
	}
	fmt.Fprintf(w, "source    %s\n", oneLine(p.Source))
	fmt.Fprintf(w, "binary    %s\n", oneLine(p.From))
	fmt.Fprintf(w, "sha256    %s\n", p.Artifact.SHA256)
	if p.Publisher != "" {
		fmt.Fprintf(w, "signed by %s (trusted key)\n", p.Publisher)
	} else {
		fmt.Fprintln(w, "WARNING   not signed by a trusted publisher; anyone who controls the source controls this code")
	}
	fmt.Fprintln(w, "A plugin runs as a separate process with your permissions. It is not sandboxed.")
	if !*yes && !a.ask("Install? [y/N] ") {
		return errors.New("not installed")
	}
	rec, err := in.Install(ctx, p)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "installed %s %s (%s); add a profile with: tess profile add NAME --tile %s\n",
		rec.Name, rec.Version, rec.Manifest.Name, rec.Name)
	return nil
}

// oneLine keeps text from a descriptor to one line of printable characters.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f || r >= 0x80 && r < 0xa0 {
			return -1
		}
		return r
	}, s)
}

func (a *App) ask(prompt string) bool {
	fmt.Fprint(a.Stderr, prompt)
	line, _ := bufio.NewReader(a.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

func (a *App) pluginList() error {
	root, err := pluginRoot()
	if err != nil {
		return err
	}
	recs, errs := plugins.List(root)
	for _, r := range recs {
		signer := "unsigned"
		if r.Signed() {
			signer = "signed by " + r.Publisher
		}
		fmt.Fprintf(a.Stdout, "%s\t%s\t%s\t%s\n", r.Name, r.Version, signer, oneLine(r.Source))
	}
	for _, err := range errs {
		fmt.Fprintf(a.Stderr, "warning: %v\n", err)
	}
	return nil
}

func (a *App) pluginVerify(args []string) error {
	root, err := pluginRoot()
	if err != nil {
		return err
	}
	names := args
	if len(names) == 0 {
		recs, errs := plugins.List(root)
		if len(errs) > 0 {
			return errors.Join(errs...)
		}
		for _, r := range recs {
			names = append(names, r.Name)
		}
	}
	var failed []error
	for _, n := range names {
		if err := plugins.Verify(root, n); err != nil {
			failed = append(failed, fmt.Errorf("plugin %s: %w", n, err))
			continue
		}
		fmt.Fprintf(a.Stdout, "%s\tok\n", n)
	}
	return errors.Join(failed...)
}

func (a *App) pluginTrust(args []string) error {
	if len(args) != 2 {
		return usageErr("plugin trust needs a publisher name and a base64 Ed25519 public key")
	}
	if _, err := plugins.ParseKey(args[1]); err != nil {
		return err
	}
	return a.editPluginSettings(func(ps *store.PluginSettings) error {
		if ps.TrustedKeys == nil {
			ps.TrustedKeys = map[string]string{}
		}
		ps.TrustedKeys[args[0]] = strings.TrimSpace(args[1])
		return nil
	})
}

func (a *App) pluginKeys() error {
	ps, err := a.pluginSettings()
	if err != nil {
		return err
	}
	names := make([]string, 0, len(ps.TrustedKeys))
	for n := range ps.TrustedKeys {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		fmt.Fprintf(a.Stdout, "%s\t%s\n", n, ps.TrustedKeys[n])
	}
	return nil
}

func (a *App) pluginKeygen(args []string) error {
	fs := a.flags("plugin keygen")
	out := fs.String("out", "", "write the private key to `FILE`, which must not exist")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if *out == "" {
		return usageErr("plugin keygen needs --out FILE")
	}
	pub, priv, err := plugins.Keygen()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(*out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(priv); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "%s\n", pub)
	fmt.Fprintf(a.Stderr, "private key in %s; users trust the public key above with: tess plugin trust NAME KEY\n", *out)
	return nil
}

func (a *App) pluginSign(args []string) error {
	fs := a.flags("plugin sign")
	key := fs.String("key", "", "private key `FILE` from tess plugin keygen")
	rest, err := parse(fs, args)
	if err != nil {
		return err
	}
	if *key == "" || len(rest) != 1 {
		return usageErr("plugin sign needs --key FILE and a descriptor")
	}
	if err := plugins.SignFile(*key, rest[0]); err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "wrote %s.sig\n", rest[0])
	return nil
}
