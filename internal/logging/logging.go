// Package logging provides the application's structured logger.
//
// Every logger returned here wraps its handler in a redacting handler, so that
// secrets cannot be logged even by accident. Redaction lives at construction
// time rather than at call sites because a call site that forgets to redact is
// exactly the failure this package exists to prevent.
//
// See docs/adr/002-normalized-ussd-protocol.md and the PII requirements in
// docs/ussd-lab-provider-adapter-design.md §38-39.
package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
)

// Placeholder substituted for the value of a sensitive field.
const Placeholder = "[REDACTED]"

// Format selects the log encoding.
type Format string

const (
	FormatText Format = "text"
	FormatJSON Format = "json"
)

// Options configures the logger.
type Options struct {
	Level  slog.Level
	Format Format
	Output io.Writer
}

// New returns a redacting structured logger.
func New(opts Options) *slog.Logger {
	if opts.Output == nil {
		opts.Output = os.Stderr
	}

	handlerOpts := &slog.HandlerOptions{Level: opts.Level}

	var base slog.Handler
	switch opts.Format {
	case FormatJSON:
		base = slog.NewJSONHandler(opts.Output, handlerOpts)
	default:
		base = slog.NewTextHandler(opts.Output, handlerOpts)
	}

	return slog.New(&redactHandler{inner: base})
}

// exactSensitiveKeys are redacted on an exact (case-insensitive) key match.
//
// Short keys such as "pin" must match exactly: a substring rule would redact
// unrelated fields like "spinner" or "pinned_version".
var exactSensitiveKeys = map[string]bool{
	"pin":      true,
	"otp":      true,
	"passcode": true,
	"cvv":      true,
}

// substringSensitiveKeys are redacted when contained anywhere in the key.
// These tokens are long and specific enough that false positives are unlikely,
// and over-redaction is the safe direction to err in.
var substringSensitiveKeys = []string{
	"password",
	"passwd",
	"secret",
	"token",
	"apikey",
	"api_key",
	"authorization",
	"credential",
	"private_key",
	"session_key",
}

// phoneKeys hold values that are personal data and are masked, not removed:
// a masked number still supports correlating log lines during debugging.
var phoneKeys = map[string]bool{
	"phone":        true,
	"phone_number": true,
	"phonenumber":  true,
	"msisdn":       true,
	"subscriber":   true,
}

// IsSensitive reports whether a field key holds a secret.
func IsSensitive(key string) bool {
	k := strings.ToLower(key)
	if exactSensitiveKeys[k] {
		return true
	}
	for _, s := range substringSensitiveKeys {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

// IsPhoneKey reports whether a field key holds a phone number.
func IsPhoneKey(key string) bool {
	return phoneKeys[strings.ToLower(key)]
}

// MaskPhone partially masks a phone number, keeping enough of it to correlate
// log lines without recording the full subscriber identity.
//
//	233240000001 -> 233******001
//
// Values too short to mask meaningfully are replaced entirely.
func MaskPhone(s string) string {
	const keepPrefix, keepSuffix = 3, 3

	if len(s) <= keepPrefix+keepSuffix {
		return strings.Repeat("*", len(s))
	}
	return s[:keepPrefix] +
		strings.Repeat("*", len(s)-keepPrefix-keepSuffix) +
		s[len(s)-keepSuffix:]
}

// redactHandler scrubs attributes before delegating to the wrapped handler.
type redactHandler struct {
	inner slog.Handler
}

func (h *redactHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *redactHandler) Handle(ctx context.Context, r slog.Record) error {
	clone := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		clone.AddAttrs(redactAttr(a))
		return true
	})
	return h.inner.Handle(ctx, clone)
}

func (h *redactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	scrubbed := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		scrubbed[i] = redactAttr(a)
	}
	return &redactHandler{inner: h.inner.WithAttrs(scrubbed)}
}

func (h *redactHandler) WithGroup(name string) slog.Handler {
	return &redactHandler{inner: h.inner.WithGroup(name)}
}

// redactAttr scrubs one attribute, recursing into groups so that nested
// structures cannot smuggle a secret past the filter.
func redactAttr(a slog.Attr) slog.Attr {
	if a.Value.Kind() == slog.KindGroup {
		src := a.Value.Group()
		out := make([]slog.Attr, len(src))
		for i, ga := range src {
			out[i] = redactAttr(ga)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}
	}

	if IsSensitive(a.Key) {
		return slog.String(a.Key, Placeholder)
	}
	if IsPhoneKey(a.Key) {
		return slog.String(a.Key, MaskPhone(a.Value.String()))
	}
	return a
}
