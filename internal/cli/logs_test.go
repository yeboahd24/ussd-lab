package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yeboahd24/ussd-lab/internal/session"
	"github.com/yeboahd24/ussd-lab/internal/storage/sqlite"
)

// seedHistory writes a completed and a cancelled session into a fresh history
// database and returns its path.
func seedHistory(t *testing.T) (string, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "history.db")
	store, err := sqlite.OpenHistory(context.Background(), path, nil)
	if err != nil {
		t.Fatalf("OpenHistory() error = %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Minute)

	store.Emit(ctx, session.Event{
		EventID: "e1", SessionID: "sess_done", Type: session.EventSessionStarted,
		Timestamp: base,
		Payload: map[string]any{
			"service_code": "*124#", "phone_number": "233240000001",
		},
	})
	store.Emit(ctx, session.Event{
		EventID: "e2", SessionID: "sess_done", Type: session.EventApplicationResponse,
		Timestamp: base, Payload: map[string]any{"type": "CON", "text": "Welcome"},
	})
	store.Emit(ctx, session.Event{
		EventID: "e3", SessionID: "sess_done", Type: session.EventInputReceived,
		Timestamp: base.Add(time.Second), Payload: map[string]any{"text": "2"},
	})
	store.Emit(ctx, session.Event{
		EventID: "e4", SessionID: "sess_done", Type: session.EventApplicationResponse,
		Timestamp: base.Add(time.Second),
		Payload:   map[string]any{"type": "END", "text": "Your balance is GHS 1,000"},
	})
	store.Emit(ctx, session.Event{
		EventID: "e5", SessionID: "sess_done", Type: session.EventSessionCompleted,
		Timestamp: base.Add(time.Second),
	})

	store.Emit(ctx, session.Event{
		EventID: "f1", SessionID: "sess_gone", Type: session.EventSessionStarted,
		Timestamp: base.Add(2 * time.Second),
		Payload:   map[string]any{"service_code": "*124#", "phone_number": "233240000002"},
	})
	store.Emit(ctx, session.Event{
		EventID: "f2", SessionID: "sess_gone", Type: session.EventSessionCancelled,
		Timestamp: base.Add(3 * time.Second),
	})

	return path, "sess_done"
}

func TestLogs_ListsSessions(t *testing.T) {
	t.Parallel()

	path, _ := seedHistory(t)

	var out bytes.Buffer
	err := runLogs(context.Background(), Env{Stdout: &out, Stderr: &out},
		&logsFlags{path: path, limit: 10}, "")
	if err != nil {
		t.Fatalf("runLogs() error = %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"SESSION", "sess_done", "sess_gone", "COMPLETED", "CANCELLED",
		"*124#", "233240000001", "2 sessions",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("listing missing %q\n%s", want, got)
		}
	}

	// Newest first.
	if strings.Index(got, "sess_gone") > strings.Index(got, "sess_done") {
		t.Errorf("sessions are not newest-first:\n%s", got)
	}
}

// Columns must line up: ANSI escapes have no display width, so padding must be
// applied before colour.
func TestLogs_ColumnsAlign(t *testing.T) {
	t.Parallel()

	path, _ := seedHistory(t)

	var out bytes.Buffer
	if err := runLogs(context.Background(), Env{Stdout: &out, Stderr: &out},
		&logsFlags{path: path, limit: 10}, ""); err != nil {
		t.Fatalf("runLogs() error = %v", err)
	}

	var widths []int
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(line, "sess_") {
			widths = append(widths, strings.Index(line, "233240"))
		}
	}
	if len(widths) < 2 {
		t.Fatalf("expected two session rows:\n%s", out.String())
	}
	if widths[0] != widths[1] {
		t.Errorf("phone column starts at %d and %d; columns misaligned:\n%s",
			widths[0], widths[1], out.String())
	}
}

func TestLogs_PrintsTranscript(t *testing.T) {
	t.Parallel()

	path, id := seedHistory(t)

	var out bytes.Buffer
	err := runLogs(context.Background(), Env{Stdout: &out, Stderr: &out},
		&logsFlags{path: path, limit: 10}, id)
	if err != nil {
		t.Fatalf("runLogs() error = %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"Session:", id, "Phone:", "233240000001", "Service:", "*124#",
		"START *124#", "USER → 2", "APP  → END", "SESSION COMPLETED",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("transcript missing %q\n%s", want, got)
		}
	}

	// Replaying one session must not repeat the per-session header used by the
	// live multi-session view.
	if strings.Contains(got, "── "+id) {
		t.Errorf("session header repeated when replaying a single session:\n%s", got)
	}
}

// "No history yet" is normal before the first run and must not look like a
// corrupt database.
func TestLogs_NoHistoryFile(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	err := runLogs(context.Background(), Env{Stdout: &out, Stderr: &out},
		&logsFlags{path: filepath.Join(t.TempDir(), "missing.db"), limit: 10}, "")

	if err == nil {
		t.Fatal("error = nil, want a failure")
	}
	if !strings.Contains(err.Error(), "ussd dev") {
		t.Errorf("error = %v, want guidance to run 'ussd dev'", err)
	}
}

func TestLogs_UnknownSession(t *testing.T) {
	t.Parallel()

	path, _ := seedHistory(t)

	var out bytes.Buffer
	err := runLogs(context.Background(), Env{Stdout: &out, Stderr: &out},
		&logsFlags{path: path, limit: 10}, "sess_nope")

	if err == nil {
		t.Fatal("error = nil, want a failure")
	}
	if !strings.Contains(err.Error(), "ussd logs") {
		t.Errorf("error = %v, want guidance to list sessions", err)
	}
}

func TestLogs_RespectsLimit(t *testing.T) {
	t.Parallel()

	path, _ := seedHistory(t)

	var out bytes.Buffer
	if err := runLogs(context.Background(), Env{Stdout: &out, Stderr: &out},
		&logsFlags{path: path, limit: 1}, ""); err != nil {
		t.Fatalf("runLogs() error = %v", err)
	}
	if !strings.Contains(out.String(), "1 sessions") {
		t.Errorf("limit not applied:\n%s", out.String())
	}
}

func TestRelativeTime(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		ago  time.Duration
		want string
	}{
		{10 * time.Second, "just now"},
		{5 * time.Minute, "5m ago"},
		{3 * time.Hour, "3h ago"},
		{50 * time.Hour, "2d ago"},
	}

	for _, tt := range tests {
		if got := relativeTime(now.Add(-tt.ago), now); got != tt.want {
			t.Errorf("relativeTime(-%v) = %q, want %q", tt.ago, got, tt.want)
		}
	}
}
