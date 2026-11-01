// Package store keeps config and profiles in TOML, and history and audit in SQLite.
package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	_ "modernc.org/sqlite"
)

const (
	configFile = "config.toml"
	stateFile  = "state.db"
)

// Dir returns TESSERA_CONFIG_DIR, or the platform config directory.
func Dir() (string, error) {
	if d := os.Getenv("TESSERA_CONFIG_DIR"); d != "" {
		return d, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "tessera"), nil
}

// Profile is a saved connection. Secrets live in the vault, never here.
type Profile struct {
	Name     string `toml:"-"`
	Tile     string `toml:"tile"`
	Env      string `toml:"env,omitempty"` // "production" raises confirmations
	ReadOnly bool   `toml:"read_only,omitempty"`
	// NoHistory stops recording statements for this profile. Audit still records writes.
	NoHistory bool              `toml:"no_history,omitempty"`
	Values    map[string]string `toml:"values,omitempty"`
}

type config struct {
	Settings
	Profiles map[string]Profile `toml:"profiles"`
}

// Settings are app-wide preferences.
type Settings struct {
	Theme       string `toml:"theme,omitempty"`        // light, dark, or empty to follow the system
	HistoryKeep int    `toml:"history_keep,omitempty"` // newest entries kept, 0 for the default
}

// DefaultHistoryKeep applies when no retention setting is saved.
const DefaultHistoryKeep = 10000

// Settings returns the saved preferences with defaults filled in.
func (s *Store) Settings() (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.loadConfig()
	if err != nil {
		return Settings{}, err
	}
	if c.HistoryKeep == 0 {
		c.HistoryKeep = DefaultHistoryKeep
	}
	return c.Settings, nil
}

func (s *Store) SaveSettings(v Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.loadConfig()
	if err != nil {
		return err
	}
	c.Settings = v
	return s.saveConfig(c)
}

type Store struct {
	dir string
	db  *sql.DB
	mu  sync.Mutex
}

var migrations = []string{
	`CREATE TABLE history (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		at INTEGER NOT NULL,
		profile TEXT NOT NULL,
		statement TEXT NOT NULL,
		elapsed_ms INTEGER NOT NULL,
		rows INTEGER NOT NULL,
		error TEXT NOT NULL
	);
	CREATE INDEX history_profile ON history (profile, id);
	CREATE TABLE audit (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		at INTEGER NOT NULL,
		actor TEXT NOT NULL,
		profile TEXT NOT NULL,
		statement TEXT NOT NULL,
		rows INTEGER NOT NULL,
		error TEXT NOT NULL,
		prev TEXT NOT NULL,
		hash TEXT NOT NULL
	);`,
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, stateFile)
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	s := &Store{dir: dir, db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	// shortcut: on Windows this is a no-op and privacy relies on the user profile's ACLs.
	os.Chmod(path, 0o600)
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Dir returns the directory holding this store's files.
func (s *Store) Dir() string { return s.dir }

func (s *Store) migrate() error {
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	for ; version < len(migrations); version++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[version]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", version+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, version+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) loadConfig() (config, error) {
	c := config{Profiles: map[string]Profile{}}
	raw, err := os.ReadFile(filepath.Join(s.dir, configFile))
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := toml.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("%s: %w", configFile, err)
	}
	if c.Profiles == nil {
		c.Profiles = map[string]Profile{}
	}
	return c, nil
}

func (s *Store) saveConfig(c config) error {
	var b strings.Builder
	if err := toml.NewEncoder(&b).Encode(c); err != nil {
		return err
	}
	path := filepath.Join(s.dir, configFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

var secretKey = regexp.MustCompile(`(?i)pass|pwd|secret|token`)

func (s *Store) SaveProfile(p Profile) error {
	if !validName.MatchString(p.Name) || strings.Contains(p.Name, "..") {
		return fmt.Errorf("invalid profile name %q: use letters, digits, '_', '.', and '-'", p.Name)
	}
	for k := range p.Values {
		if secretKey.MatchString(k) {
			return fmt.Errorf("%q looks like a secret and cannot be stored in %s", k, configFile)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.loadConfig()
	if err != nil {
		return err
	}
	c.Profiles[p.Name] = p
	return s.saveConfig(c)
}

func (s *Store) Profile(name string) (Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.loadConfig()
	if err != nil {
		return Profile{}, err
	}
	p, ok := c.Profiles[name]
	if !ok {
		return Profile{}, fmt.Errorf("no profile named %q", name)
	}
	p.Name = name
	return p, nil
}

// Profiles returns all profiles sorted by name.
func (s *Store) Profiles() ([]Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.loadConfig()
	if err != nil {
		return nil, err
	}
	out := make([]Profile, 0, len(c.Profiles))
	for name, p := range c.Profiles {
		p.Name = name
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b Profile) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func (s *Store) DeleteProfile(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.loadConfig()
	if err != nil {
		return err
	}
	if _, ok := c.Profiles[name]; !ok {
		return fmt.Errorf("no profile named %q", name)
	}
	delete(c.Profiles, name)
	return s.saveConfig(c)
}

type HistoryEntry struct {
	At        time.Time
	Profile   string
	Statement string // already redacted by the caller
	ElapsedMS int64
	Rows      int64
	Error     string
}

func (s *Store) AddHistory(e HistoryEntry) error {
	_, err := s.db.Exec(`INSERT INTO history (at, profile, statement, elapsed_ms, rows, error) VALUES (?, ?, ?, ?, ?, ?)`,
		time.Now().UnixMilli(), e.Profile, e.Statement, e.ElapsedMS, e.Rows, e.Error)
	return err
}

// History returns a profile's newest entries first.
func (s *Store) History(profile string, limit int) ([]HistoryEntry, error) {
	rows, err := s.db.Query(`SELECT at, profile, statement, elapsed_ms, rows, error FROM history
		WHERE profile = ? ORDER BY id DESC LIMIT ?`, profile, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HistoryEntry
	for rows.Next() {
		var e HistoryEntry
		var at int64
		if err := rows.Scan(&at, &e.Profile, &e.Statement, &e.ElapsedMS, &e.Rows, &e.Error); err != nil {
			return nil, err
		}
		e.At = time.UnixMilli(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// PruneHistory keeps only the newest keep entries across all profiles.
func (s *Store) PruneHistory(keep int) error {
	_, err := s.db.Exec(`DELETE FROM history WHERE id NOT IN (SELECT id FROM history ORDER BY id DESC LIMIT ?)`, keep)
	return err
}

type AuditEntry struct {
	Actor     string
	Profile   string
	Statement string // already redacted by the caller
	Rows      int64
	Error     string
}

func auditHash(prev string, at int64, e AuditEntry) string {
	h := sha256.New()
	// Lengths keep fields from running together, so moved text cannot give the same hash.
	fmt.Fprintf(h, "%s|%d|%d:%s|%d:%s|%d:%s|%d|%d:%s", prev, at,
		len(e.Actor), e.Actor, len(e.Profile), e.Profile, len(e.Statement), e.Statement, e.Rows, len(e.Error), e.Error)
	return hex.EncodeToString(h.Sum(nil))
}

// AddAudit appends to a hash chain. The chain shows tampering; it cannot prevent it against
// someone with file access, so forward to syslog when that matters.
func (s *Store) AddAudit(e AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var prev string
	err = tx.QueryRow(`SELECT hash FROM audit ORDER BY id DESC LIMIT 1`).Scan(&prev)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	at := time.Now().UnixMilli()
	if _, err := tx.Exec(`INSERT INTO audit (at, actor, profile, statement, rows, error, prev, hash) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		at, e.Actor, e.Profile, e.Statement, e.Rows, e.Error, prev, auditHash(prev, at, e)); err != nil {
		return err
	}
	return tx.Commit()
}

// VerifyAudit walks the chain and returns the number of valid entries.
func (s *Store) VerifyAudit() (int, error) {
	rows, err := s.db.Query(`SELECT at, actor, profile, statement, rows, error, prev, hash FROM audit ORDER BY id`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	want := ""
	for rows.Next() {
		var at int64
		var e AuditEntry
		var prev, hash string
		if err := rows.Scan(&at, &e.Actor, &e.Profile, &e.Statement, &e.Rows, &e.Error, &prev, &hash); err != nil {
			return n, err
		}
		n++
		if prev != want || hash != auditHash(prev, at, e) {
			return n, fmt.Errorf("audit log broken at entry %d", n)
		}
		want = hash
	}
	return n, rows.Err()
}
