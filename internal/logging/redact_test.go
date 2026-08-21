package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// newTestLogger returns a JSON logger writing into buf, for assertion.
func newTestLogger(buf *bytes.Buffer) *slog.Logger {
	return New(Options{Level: slog.LevelDebug, Format: FormatJSON, Output: buf})
}

func TestRedact_SecretsNeverReachOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		key  string
	}{
		{"pin", "pin"},
		{"uppercase PIN", "PIN"},
		{"otp", "otp"},
		{"password", "password"},
		{"user_password", "user_password"},
		{"secret", "client_secret"},
		{"token", "access_token"},
		{"api key", "api_key"},
		{"authorization", "authorization"},
		{"credential", "provider_credential"},
	}

	const secret = "hunter2-SUPERSECRET"

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			newTestLogger(&buf).Info("event", slog.String(tt.key, secret))

			out := buf.String()
			if strings.Contains(out, secret) {
				t.Errorf("secret leaked for key %q: %s", tt.key, out)
			}
			if !strings.Contains(out, Placeholder) {
				t.Errorf("no redaction placeholder for key %q: %s", tt.key, out)
			}
		})
	}
}

// Short keys must match exactly, or unrelated fields get destroyed.
func TestRedact_NoFalsePositives(t *testing.T) {
	t.Parallel()

	tests := []struct {
		key   string
		value string
	}{
		{"spinner", "loading"},
		{"pinned_version", "1.2.3"},
		{"session_id", "sess_123"},
		{"service_code", "*124#"},
		{"status", "ACTIVE"},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			newTestLogger(&buf).Info("event", slog.String(tt.key, tt.value))

			if !strings.Contains(buf.String(), tt.value) {
				t.Errorf("value for %q was wrongly redacted: %s", tt.key, buf.String())
			}
		})
	}
}

func TestMaskPhone(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in, want string
	}{
		{"233240000001", "233******001"},
		{"0240000001", "024****001"},
		{"123456", "******"},
		{"12", "**"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()

			if got := MaskPhone(tt.in); got != tt.want {
				t.Errorf("MaskPhone(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestRedact_PhoneNumbersMasked(t *testing.T) {
	t.Parallel()

	const phone = "233240000001"

	for _, key := range []string{"phone", "phone_number", "msisdn"} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			newTestLogger(&buf).Info("event", slog.String(key, phone))

			out := buf.String()
			if strings.Contains(out, phone) {
				t.Errorf("full phone number leaked for %q: %s", key, out)
			}
			if !strings.Contains(out, "233******001") {
				t.Errorf("phone not masked as expected for %q: %s", key, out)
			}
		})
	}
}

// A secret nested inside a group must not escape redaction.
func TestRedact_NestedGroup(t *testing.T) {
	t.Parallel()

	const secret = "nested-SUPERSECRET"

	var buf bytes.Buffer
	newTestLogger(&buf).Info("event",
		slog.Group("provider",
			slog.String("name", "simulator"),
			slog.String("api_key", secret),
		),
	)

	out := buf.String()
	if strings.Contains(out, secret) {
		t.Errorf("secret leaked from nested group: %s", out)
	}
	if !strings.Contains(out, "simulator") {
		t.Errorf("non-sensitive nested value lost: %s", out)
	}
}

// Attributes bound via With() are scrubbed once, at binding time.
func TestRedact_WithAttrs(t *testing.T) {
	t.Parallel()

	const secret = "with-SUPERSECRET"

	var buf bytes.Buffer
	logger := newTestLogger(&buf).With(slog.String("api_token", secret))
	logger.Info("event")

	if strings.Contains(buf.String(), secret) {
		t.Errorf("secret leaked via With(): %s", buf.String())
	}
}

func TestNew_ProducesValidJSON(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	newTestLogger(&buf).Info("hello", slog.String("session_id", "sess_1"))

	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if m["msg"] != "hello" {
		t.Errorf("msg = %v, want hello", m["msg"])
	}
	if m["session_id"] != "sess_1" {
		t.Errorf("session_id = %v, want sess_1", m["session_id"])
	}
}

func TestNew_RespectsLevel(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := New(Options{Level: slog.LevelWarn, Format: FormatJSON, Output: &buf})
	logger.Debug("should not appear")

	if buf.Len() != 0 {
		t.Errorf("debug record emitted at warn level: %s", buf.String())
	}
}
