package cli

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yeboahd24/ussd-lab/internal/netdetect"
	"github.com/yeboahd24/ussd-lab/internal/session"
)

// syncBuffer is a concurrency-safe io.Writer. runDev writes from its own
// goroutine while the test polls the output, and bytes.Buffer is not safe for
// that -- the race detector catches it immediately.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestResolveHost_Precedence(t *testing.T) {
	t.Parallel()

	t.Run("flag wins", func(t *testing.T) {
		t.Parallel()

		host, detection, err := resolveHost("10.0.0.1", "192.168.1.1")
		if err != nil {
			t.Fatalf("resolveHost() error = %v", err)
		}
		if host != "10.0.0.1" {
			t.Errorf("host = %q, want the flag value", host)
		}
		if detection != nil {
			t.Error("detection ran even though --host was given")
		}
	})

	t.Run("config used when no flag", func(t *testing.T) {
		t.Parallel()

		host, _, err := resolveHost("", "192.168.1.1")
		if err != nil {
			t.Fatalf("resolveHost() error = %v", err)
		}
		if host != "192.168.1.1" {
			t.Errorf("host = %q, want the config value", host)
		}
	})

	t.Run("falls back to detection", func(t *testing.T) {
		t.Parallel()

		host, detection, err := resolveHost("", "")
		if err != nil {
			t.Skipf("no LAN address on this machine: %v", err)
		}
		if host == "" {
			t.Error("host is empty")
		}
		if detection == nil {
			t.Error("detection result not reported")
		}
	})
}

func TestOpenStore(t *testing.T) {
	t.Parallel()

	t.Run("memory", func(t *testing.T) {
		t.Parallel()

		store, cleanup, err := openStore(context.Background(), "memory", "")
		if err != nil {
			t.Fatalf("openStore() error = %v", err)
		}
		defer cleanup()

		if store == nil {
			t.Error("store is nil")
		}
	})

	t.Run("empty defaults to memory", func(t *testing.T) {
		t.Parallel()

		_, cleanup, err := openStore(context.Background(), "", "")
		if err != nil {
			t.Fatalf("openStore() error = %v", err)
		}
		cleanup()
	})

	t.Run("sqlite", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "s.db")
		store, cleanup, err := openStore(context.Background(), "sqlite", path)
		if err != nil {
			t.Fatalf("openStore() error = %v", err)
		}
		defer cleanup()

		if store == nil {
			t.Fatal("store is nil")
		}
		if _, err := os.Stat(path); err != nil {
			t.Errorf("database file not created: %v", err)
		}
	})

	t.Run("unknown", func(t *testing.T) {
		t.Parallel()

		_, _, err := openStore(context.Background(), "redis", "")
		if err == nil {
			t.Fatal("error = nil, want a failure for an unknown store")
		}
		if !strings.Contains(err.Error(), "memory or sqlite") {
			t.Errorf("error = %v, want it to name the valid options", err)
		}
	})
}

func TestPrintDashboard(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	printDashboard(&buf, dashboardInfo{
		project:     "my-fintech",
		url:         "http://192.168.1.20:7345/s/token123",
		callback:    "http://localhost:8000/ussd",
		serviceCode: "*124#",
		store:       "memory",
		latency:     250 * time.Millisecond,
		color:       false,
		showQR:      false,
	})

	out := buf.String()
	for _, want := range []string{
		"my-fintech", "*124#", "http://localhost:8000/ussd",
		"http://192.168.1.20:7345/s/token123", "memory", "250ms",
		"airplane mode", "Waiting for a session",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dashboard missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b[") {
		t.Error("colour escapes present with color=false")
	}
}

// When detection is ambiguous the alternatives must be shown, because a
// silently wrong address produces a QR that scans and then hangs.
func TestPrintDashboard_ShowsAmbiguity(t *testing.T) {
	t.Parallel()

	res, err := netdetect.DetectSystem()
	if err != nil {
		t.Skip("cannot enumerate interfaces")
	}
	// Force the ambiguous branch regardless of this machine's configuration.
	res.Ambiguous = true

	var buf bytes.Buffer
	printDashboard(&buf, dashboardInfo{
		project: "p", url: "http://x/s/y", callback: "http://c", serviceCode: "*1#",
		store: "memory", detection: &res, showQR: false,
	})

	out := buf.String()
	if !strings.Contains(out, "equally plausible") {
		t.Errorf("ambiguity not reported:\n%s", out)
	}
	if !strings.Contains(out, "--host") {
		t.Errorf("no --host guidance:\n%s", out)
	}
}

func TestLiveLog_FormatsSession(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	log := newLiveLog(&buf, false)
	ctx := context.Background()
	now := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)

	events := []session.Event{
		{SessionID: "sess_1", Type: session.EventSessionStarted, Timestamp: now,
			Payload: map[string]any{"service_code": "*124#"}},
		{SessionID: "sess_1", Type: session.EventApplicationRequest, Timestamp: now,
			Payload: map[string]any{"text": ""}},
		{SessionID: "sess_1", Type: session.EventApplicationResponse, Timestamp: now,
			Payload: map[string]any{"type": "CON", "text": "Welcome\n1. Send"}},
		{SessionID: "sess_1", Type: session.EventInputReceived, Timestamp: now,
			Payload: map[string]any{"text": "1"}},
		{SessionID: "sess_1", Type: session.EventSessionCompleted, Timestamp: now},
	}
	for _, e := range events {
		log.Emit(ctx, e)
	}

	out := buf.String()
	for _, want := range []string{
		"sess_1", "START *124#", "USER → 1", "APP  → CON", "SESSION COMPLETED",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q\n%s", want, out)
		}
	}

	// A multi-line menu must be flattened so the log stays scannable.
	if strings.Contains(out, "Welcome\n1. Send") {
		t.Error("multi-line screen was not flattened")
	}
	if !strings.Contains(out, "Welcome / 1. Send") {
		t.Errorf("expected the flattened form:\n%s", out)
	}

	// APPLICATION_REQUEST adds no information beyond the input already shown.
	if strings.Contains(out, "APPLICATION_REQUEST") {
		t.Error("noisy APPLICATION_REQUEST event was rendered")
	}
}

// The session header must appear once per session, not per event.
func TestLiveLog_HeaderPerSession(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	log := newLiveLog(&buf, false)
	ctx := context.Background()
	now := time.Now()

	for _, id := range []string{"sess_a", "sess_a", "sess_b"} {
		log.Emit(ctx, session.Event{
			SessionID: id, Type: session.EventSessionStarted, Timestamp: now,
			Payload: map[string]any{"service_code": "*124#"},
		})
	}

	out := buf.String()
	if got := strings.Count(out, "── sess_a"); got != 1 {
		t.Errorf("sess_a header appeared %d times, want 1", got)
	}
	if got := strings.Count(out, "── sess_b"); got != 1 {
		t.Errorf("sess_b header appeared %d times, want 1", got)
	}
}

func TestLiveLog_TruncatesLongScreens(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	newLiveLog(&buf, false).Emit(context.Background(), session.Event{
		SessionID: "sess_1", Type: session.EventApplicationResponse, Timestamp: time.Now(),
		Payload: map[string]any{"type": "CON", "text": strings.Repeat("x", 500)},
	})

	for _, line := range strings.Split(buf.String(), "\n") {
		if len(line) > 200 {
			t.Errorf("log line is %d characters; long screens must be truncated", len(line))
		}
	}
}

// runDev must start, print the dashboard, and shut down cleanly on context
// cancellation -- the Ctrl-C path.
func TestRunDev_StartsAndStops(t *testing.T) {
	devApp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "CON Menu")
	}))
	defer devApp.Close()

	dir := t.TempDir()
	cfg := fmt.Sprintf(`project: testproj
application:
  callback: %s/ussd
ussd:
  service_code: "*124#"
  session_timeout: 60
simulator:
  port: 0
`, devApp.URL)

	path := filepath.Join(dir, "ussd.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	out := &syncBuffer{}
	env := Env{Stdout: out, Stderr: out}
	global := &globalFlags{configPath: path}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runDev(ctx, env, global,
			&devFlags{host: "127.0.0.1", store: "memory", noQR: true})
	}()

	// Wait for the dashboard, then request shutdown.
	deadline := time.After(5 * time.Second)
	for !strings.Contains(out.String(), "Waiting for a session") {
		select {
		case err := <-done:
			t.Fatalf("runDev returned early: %v\n%s", err, out.String())
		case <-deadline:
			t.Fatalf("dashboard never printed:\n%s", out.String())
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("runDev() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runDev did not stop after cancellation")
	}

	got := out.String()
	for _, want := range []string{"testproj", "*124#", "/s/", "Simulator stopped"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\n%s", want, got)
		}
	}
}

func TestRunDev_MissingConfig(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	env := Env{Stdout: &out, Stderr: &out}
	global := &globalFlags{configPath: filepath.Join(t.TempDir(), "nope.yaml")}

	err := runDev(context.Background(), env, global, &devFlags{})
	if err == nil {
		t.Fatal("error = nil, want a failure")
	}
	if !strings.Contains(err.Error(), "ussd init") {
		t.Errorf("error = %v, want guidance to run 'ussd init'", err)
	}
}
