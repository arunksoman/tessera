package mysql

import (
	"strings"
	"testing"
)

func TestConfigSafeDefaults(t *testing.T) {
	cfg, err := config(map[string]string{"host": "db.example.com", "user": "u"}, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ClientFoundRows {
		t.Error("ClientFoundRows must be on, or guarded updates report false conflicts")
	}
	if cfg.MultiStatements || cfg.AllowAllFiles || cfg.AllowCleartextPasswords || cfg.AllowOldPasswords {
		t.Errorf("unsafe option on: %+v", cfg)
	}
	if cfg.TLS == nil || cfg.TLS.InsecureSkipVerify || cfg.TLS.ServerName != "db.example.com" {
		t.Errorf("new connections must verify the server identity by default: %+v", cfg.TLS)
	}
	if cfg.AllowFallbackToPlaintext {
		t.Error("fallback to plaintext must be off by default")
	}
}

func TestConfigTLSModes(t *testing.T) {
	for mode, check := range map[string]struct{ tls, skip, verifier, fallback bool }{
		"DISABLED":        {},
		"PREFERRED":       {tls: true, skip: true, fallback: true},
		"REQUIRED":        {tls: true, skip: true},
		"VERIFY_CA":       {tls: true, skip: true, verifier: true},
		"VERIFY_IDENTITY": {tls: true},
	} {
		t.Run(mode, func(t *testing.T) {
			cfg, err := config(map[string]string{"host": "h", "tlsmode": mode}, "")
			if err != nil {
				t.Fatal(err)
			}
			if (cfg.TLS != nil) != check.tls || cfg.AllowFallbackToPlaintext != check.fallback {
				t.Fatalf("tls=%v fallback=%v", cfg.TLS, cfg.AllowFallbackToPlaintext)
			}
			if cfg.TLS != nil {
				if cfg.TLS.InsecureSkipVerify != check.skip || (cfg.TLS.VerifyPeerCertificate != nil) != check.verifier {
					t.Fatalf("skip=%v verifier=%v", cfg.TLS.InsecureSkipVerify, cfg.TLS.VerifyPeerCertificate != nil)
				}
			}
		})
	}
	if _, err := config(map[string]string{"tlsmode": "NOPE"}, ""); err == nil || !strings.Contains(err.Error(), "NOPE") {
		t.Fatalf("err = %v", err)
	}
}

func TestConfigValuesCannotInject(t *testing.T) {
	hostile := "u:pw@tcp(evil:1)/x?allowAllFiles=true"
	cfg, err := config(map[string]string{"user": hostile, "database": hostile, "tlsmode": "DISABLED"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.User != hostile || cfg.DBName != hostile || cfg.AllowAllFiles {
		t.Fatalf("%+v", cfg)
	}
}

func TestConfigSocketSkipsTLS(t *testing.T) {
	cfg, err := config(map[string]string{"socket": "/run/mysqld/mysqld.sock", "user": "u"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Net != "unix" || cfg.Addr != "/run/mysqld/mysqld.sock" || cfg.TLS != nil {
		t.Fatalf("%+v", cfg)
	}
}
