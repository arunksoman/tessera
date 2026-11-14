package secrets

import (
	"bytes"
	"context"
	"errors"
	"log"
	"log/slog"
	"strings"
	"testing"
)

func logged(t *testing.T, f func(*slog.Logger)) string {
	t.Helper()
	var buf bytes.Buffer
	f(slog.New(NewLogHandler(slog.NewTextHandler(&buf, nil))))
	return buf.String()
}

func TestLogHandlerRedactsSecretKeys(t *testing.T) {
	out := logged(t, func(l *slog.Logger) {
		l.Info("open", "user", "app", "password", "hunter2", "PGPASSWORD", "pw2",
			"api_token", "tok3", "client_secret", []byte("sec4"), "rows", 3)
	})
	for _, leak := range []string{"hunter2", "pw2", "tok3", "sec4"} {
		if strings.Contains(out, leak) {
			t.Errorf("leaked %q: %s", leak, out)
		}
	}
	for _, keep := range []string{"user=app", "rows=3", "password=[redacted]"} {
		if !strings.Contains(out, keep) {
			t.Errorf("missing %q: %s", keep, out)
		}
	}
}

func TestLogHandlerRedactsGroupsAndWith(t *testing.T) {
	out := logged(t, func(l *slog.Logger) {
		l = l.With("token", "w1").WithGroup("conn")
		l.Info("x", slog.Group("auth", "pwd", "g2", "method", "scram"), "secret", "g3")
	})
	for _, leak := range []string{"w1", "g2", "g3"} {
		if strings.Contains(out, leak) {
			t.Errorf("leaked %q: %s", leak, out)
		}
	}
	if !strings.Contains(out, "conn.auth.method=scram") {
		t.Errorf("group lost: %s", out)
	}
}

// Passwords hide in values too: connection URLs, DSNs, and wrapped driver errors.
func TestLogHandlerRedactsValues(t *testing.T) {
	out := logged(t, func(l *slog.Logger) {
		l.Error("dial postgres://app:s3cret@db:5432/x failed",
			"err", errors.New(`connect "host=db password=p4ss user=app"`),
			"dsn", "mysql://root:r00t@tcp(db)/", "secret", Secret{value: []byte("v1")})
	})
	for _, leak := range []string{"s3cret", "p4ss", "r00t", "v1"} {
		if strings.Contains(out, leak) {
			t.Errorf("leaked %q: %s", leak, out)
		}
	}
	for _, keep := range []string{"postgres://app:[redacted]@db:5432/x", "user=app"} {
		if !strings.Contains(out, keep) {
			t.Errorf("missing %q: %s", keep, out)
		}
	}
}

// The standard log package, used by net/http for server errors, goes through the handler
// once it is the default.
func TestLogHandlerAsDefault(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	defer slog.SetDefault(old)
	slog.SetDefault(slog.New(NewLogHandler(slog.NewTextHandler(&buf, nil))))
	log.Printf("http: dial mysql://u:leak@h/")
	if strings.Contains(buf.String(), "leak") {
		t.Fatal(buf.String())
	}
	if !NewLogHandler(slog.NewTextHandler(&buf, nil)).Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("Enabled must follow the wrapped handler")
	}
}
