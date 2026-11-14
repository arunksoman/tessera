package secrets

import (
	"context"
	"log/slog"
	"regexp"
)

// secretAttr matches attribute keys whose values are always hidden.
var secretAttr = regexp.MustCompile(`(?i)pass|pwd|secret|token|credential|api_?key|private_?key`)

var (
	// urlPassword is the password in scheme://user:password@host.
	urlPassword = regexp.MustCompile(`(://[^:/@\s]*:)[^@\s]*@`)
	// kvPassword is password=value in a DSN or an error that quotes one.
	kvPassword = regexp.MustCompile(`(?i)\b(password|passwd|pwd|secret|token)(\s*=\s*)('[^']*'|"[^"]*"|[^\s"']+)`)
)

// RedactText hides passwords in connection URLs and key=value strings.
func RedactText(s string) string {
	s = urlPassword.ReplaceAllString(s, "${1}"+redactedDisplay+"@")
	return kvPassword.ReplaceAllString(s, "${1}${2}"+redactedDisplay)
}

// NewLogHandler wraps h so secrets do not reach logs: attributes with secret-looking keys
// print as [redacted], and passwords inside messages, strings, and errors are hidden. It is
// a guardrail behind the Secret type and Language.Redact, not a replacement for them.
func NewLogHandler(h slog.Handler) slog.Handler { return logHandler{h} }

type logHandler struct{ next slog.Handler }

func (h logHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

func (h logHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, RedactText(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(redactAttr(a))
		return true
	})
	return h.next.Handle(ctx, out)
}

func (h logHandler) WithAttrs(as []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(as))
	for i, a := range as {
		clean[i] = redactAttr(a)
	}
	return logHandler{h.next.WithAttrs(clean)}
}

func (h logHandler) WithGroup(name string) slog.Handler {
	return logHandler{h.next.WithGroup(name)}
}

func redactAttr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	if v.Kind() != slog.KindGroup && secretAttr.MatchString(a.Key) {
		return slog.String(a.Key, redactedDisplay)
	}
	switch v.Kind() {
	case slog.KindGroup:
		as := v.Group()
		clean := make([]any, len(as))
		for i, g := range as {
			clean[i] = redactAttr(g)
		}
		return slog.Group(a.Key, clean...)
	case slog.KindString:
		return slog.String(a.Key, RedactText(v.String()))
	case slog.KindAny:
		if err, ok := v.Any().(error); ok {
			return slog.String(a.Key, RedactText(err.Error()))
		}
	}
	return slog.Attr{Key: a.Key, Value: v}
}
