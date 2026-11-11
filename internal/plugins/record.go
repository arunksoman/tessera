package plugins

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/arunksoman/tessera/tile"
)

const recordFile = "plugin.json"

// Record is what tess remembers about an installed plugin.
type Record struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Source  string `json:"source"`
	// Publisher names the trusted key that signed the plugin, empty when unsigned.
	Publisher    string        `json:"publisher,omitempty"`
	SHA256       string        `json:"sha256"`
	Binary       string        `json:"binary"`
	Manifest     tile.Manifest `json:"manifest"`
	ConfigWarner bool          `json:"config_warner,omitempty"`
	InstalledAt  time.Time     `json:"installed_at"`
}

func (r Record) Signed() bool { return r.Publisher != "" }

func (r Record) pluginDir(root string) string { return filepath.Join(root, r.Name) }

func (r Record) binaryPath(root string) string {
	return filepath.Join(r.pluginDir(root), r.Binary)
}

// validName keeps a plugin name usable as a directory and a tile ID.
var validName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

func checkName(name string, builtins []string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("plugin name %q must be lowercase letters, digits, and dashes", name)
	}
	if slices.Contains(builtins, name) {
		return fmt.Errorf("%q is built into tess and cannot be replaced by a plugin", name)
	}
	return nil
}

func binaryName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// Dir returns the plugin directory inside the config directory.
func Dir(configDir string) string { return filepath.Join(configDir, "plugins") }

// List returns installed plugins sorted by name. Unreadable entries are reported, not fatal.
func List(root string) ([]Record, []error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, []error{err}
	}
	var out []Record
	var errs []error
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		r, err := readRecord(root, e.Name())
		if err != nil {
			errs = append(errs, fmt.Errorf("plugin %s: %w", e.Name(), err))
			continue
		}
		out = append(out, r)
	}
	return out, errs
}

func readRecord(root, name string) (Record, error) {
	var r Record
	b, err := os.ReadFile(filepath.Join(root, name, recordFile))
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, err
	}
	if r.Name != name || r.Manifest.ID != name || r.Binary != binaryName(name) {
		return r, errors.New("record does not match its directory")
	}
	return r, nil
}

func writeRecord(dir string, r Record) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, recordFile), b, 0o600)
}

// Load returns a tile for each installed plugin, skipping any that clash with a built-in tile
// or fail to read. Processes start only when a connection opens. Call Close on each when done.
func Load(root string, builtins []string) ([]*Tile, []error) {
	recs, errs := List(root)
	var out []*Tile
	for _, r := range recs {
		if err := checkName(r.Name, builtins); err != nil {
			errs = append(errs, fmt.Errorf("plugin %s skipped: %w", r.Name, err))
			continue
		}
		out = append(out, &Tile{rec: r, dir: root})
	}
	return out, errs
}

// AsTile returns t with the optional facets its plugin reported.
func AsTile(t *Tile) tile.Tile {
	if t.rec.ConfigWarner {
		return warnTile{t}
	}
	return t
}

// Remove deletes an installed plugin.
func Remove(root, name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("no plugin named %q", name)
	}
	if _, err := readRecord(root, name); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("no plugin named %q", name)
		}
	}
	return os.RemoveAll(filepath.Join(root, name))
}

// Verify checks an installed plugin's binary still matches its pinned hash.
func Verify(root, name string) error {
	r, err := readRecord(root, name)
	if err != nil {
		return err
	}
	return checkHash(r.binaryPath(root), r.SHA256)
}
