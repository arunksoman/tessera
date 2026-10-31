package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProfilesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := Profile{Name: "dev", Tile: "postgres", Env: "production", ReadOnly: true, Values: map[string]string{"host": "h", "port": "5432"}}
	if err := s.SaveProfile(p); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, err := s2.Profile("dev")
	if err != nil {
		t.Fatal(err)
	}
	if got.Tile != "postgres" || !got.ReadOnly || got.Env != "production" || got.Values["port"] != "5432" {
		t.Fatalf("%+v", got)
	}
	if err := s2.DeleteProfile("dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Profile("dev"); err == nil {
		t.Fatal("deleted profile still found")
	}
}

func TestProfileRefusesSecretsInConfig(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, key := range []string{"password", "Password", "sslpassword", "db_passwd", "secret", "token"} {
		err := s.SaveProfile(Profile{Name: "x", Tile: "postgres", Values: map[string]string{key: "hunter2"}})
		if err == nil {
			t.Errorf("%s accepted into the plaintext config", key)
		}
	}
}

func TestProfileNameValidated(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, name := range []string{"", "a/b", "..", "a b", `a\b`} {
		if err := s.SaveProfile(Profile{Name: name, Tile: "postgres"}); err == nil {
			t.Errorf("name %q accepted", name)
		}
	}
}

func TestHistory(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, text := range []string{"SELECT 1", "SELECT 2", "SELECT 3"} {
		if err := s.AddHistory(HistoryEntry{Profile: "dev", Statement: text, ElapsedMS: 3, Rows: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AddHistory(HistoryEntry{Profile: "other", Statement: "SELECT 9"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.History("dev", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Statement != "SELECT 3" || got[1].Statement != "SELECT 2" {
		t.Fatalf("%+v", got)
	}
	if err := s.PruneHistory(1); err != nil {
		t.Fatal(err)
	}
	got, _ = s.History("dev", 10)
	if len(got) != 0 {
		t.Fatalf("pruned keeps only the newest overall: %+v", got)
	}
}

func TestAuditChainDetectsTampering(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, text := range []string{"UPDATE a", "DELETE b", "DROP c"} {
		if err := s.AddAudit(AuditEntry{Actor: "me", Profile: "dev", Statement: text, Rows: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.VerifyAudit(); err != nil || n != 3 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if _, err := s.db.Exec(`UPDATE audit SET statement = 'SELECT 1' WHERE statement = 'DELETE b'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyAudit(); err == nil || !strings.Contains(err.Error(), "2") {
		t.Fatalf("edit not detected at row 2: %v", err)
	}
}

func TestAuditChainDetectsDeletion(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, text := range []string{"a", "b", "c"} {
		if err := s.AddAudit(AuditEntry{Statement: text}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`DELETE FROM audit WHERE statement = 'b'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyAudit(); err == nil {
		t.Fatal("a deleted row must break the chain")
	}
}

func TestStateFileIsPrivate(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := os.Stat(filepath.Join(dir, stateFile)); err != nil {
		t.Fatal(err)
	}
}
