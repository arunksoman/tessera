package postgres

import (
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestConnStringCannotInjectSettings(t *testing.T) {
	hostile := `x' sslmode='disable`
	cfg, err := pgconn.ParseConfig(connString(map[string]string{
		"host": "db.example.com", "user": hostile, "database": `a\b`, "sslmode": "require",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.User != hostile {
		t.Fatalf("user = %q", cfg.User)
	}
	if cfg.Database != `a\b` {
		t.Fatalf("database = %q", cfg.Database)
	}
	if cfg.TLSConfig == nil {
		t.Fatal("a value in user turned TLS off")
	}
}

func TestConnStringIgnoresUnknownKeys(t *testing.T) {
	got := connString(map[string]string{"host": "h", "servicefile": "/etc/passwd", "passfile": "/x", "password": "secret"})
	if got != "host='h' sslmode='verify-full' " {
		t.Fatalf("connString = %q", got)
	}
}

func TestNewProfilesVerifyTLSByDefault(t *testing.T) {
	t.Setenv("PGSSLMODE", "disable") // the environment must not weaken a profile either
	cfg, err := pgconn.ParseConfig(connString(map[string]string{"host": "db.example.com", "user": "u"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TLSConfig == nil || cfg.TLSConfig.InsecureSkipVerify || cfg.TLSConfig.ServerName != "db.example.com" {
		t.Fatalf("TLS = %+v", cfg.TLSConfig)
	}
	if len(cfg.Fallbacks) != 0 {
		t.Fatalf("a verify-full profile must not fall back to plaintext: %+v", cfg.Fallbacks)
	}
}
