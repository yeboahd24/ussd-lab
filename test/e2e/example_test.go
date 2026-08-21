package e2e

import (
	"bufio"
	"context"
	"net/http"
	"net/http/cookiejar"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yeboahd24/ussd-lab/internal/appclient"
	"github.com/yeboahd24/ussd-lab/internal/session"
	"github.com/yeboahd24/ussd-lab/internal/simulator"
	"github.com/yeboahd24/ussd-lab/internal/storage/memory"
)

// startExample builds and runs examples/simple-bank as a SEPARATE PROCESS.
//
// Importing the example would defeat the point. The claim being tested is that
// a developer's application is an ordinary HTTP server in any language, needing
// no SDK and no in-process cooperation -- so the test talks to a real binary
// over a real socket, exactly as USSD Lab will in the field.
func startExample(t *testing.T) string {
	t.Helper()

	if testing.Short() {
		t.Skip("builds a binary; skipped in -short")
	}

	bin := filepath.Join(t.TempDir(), "simple-bank")
	build := exec.Command("go", "build", "-o", bin, "../../examples/simple-bank")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build example: %v\n%s", err, out)
	}

	cmd := exec.Command(bin, "-addr", "127.0.0.1:0")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start example: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	// The example prints its resolved address, so the test never has to guess
	// a free port or race against startup.
	addrCh := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			if _, addr, found := strings.Cut(line, "listening on "); found {
				addrCh <- addr
				return
			}
		}
		close(addrCh)
	}()

	select {
	case addr, ok := <-addrCh:
		if !ok || addr == "" {
			t.Fatal("example did not report a listening address")
		}
		return "http://" + addr + "/ussd"
	case <-time.After(10 * time.Second):
		t.Fatal("example did not start in time")
		return ""
	}
}

// newStackFor wires the real simulator against an already-running application.
func newStackFor(t *testing.T, callbackURL string) *stack {
	t.Helper()

	client, err := appclient.New(appclient.Options{
		CallbackURL: callbackURL,
		Timeout:     3 * time.Second,
	})
	if err != nil {
		t.Fatalf("appclient.New() error = %v", err)
	}

	engine, err := session.New(session.Options{
		Store: memory.New(), App: client, Timeout: 120 * time.Second,
	})
	if err != nil {
		t.Fatalf("session.New() error = %v", err)
	}

	srv, err := simulator.New(simulator.Options{
		Engine: engine, ProjectID: "simple-bank",
		ServiceCode: "*124#", BindAddr: "127.0.0.1:0",
	})
	if err != nil {
		t.Fatalf("simulator.New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Serve(ctx) }()
	t.Cleanup(func() { cancel(); _ = srv.Shutdown() })

	jar, _ := cookiejar.New(nil)
	s := &stack{
		baseURL: "http://" + srv.Addr().String(),
		client:  &http.Client{Timeout: 10 * time.Second, Jar: jar},
	}
	s.attach(t, srv)
	return s
}

// Every menu path in the example, driven through the whole stack against a
// real separate process. This is the MVP acceptance flow from design §19.
func TestE2E_Example_AllPaths(t *testing.T) {
	callback := startExample(t)

	tests := []struct {
		name     string
		steps    []string
		wantType string
		contains string
	}{
		{
			name:     "send money confirmed",
			steps:    []string{"1", "0241234567", "100", "1"},
			wantType: "END",
			contains: "Transaction successful.",
		},
		{
			name:     "send money cancelled",
			steps:    []string{"1", "0241234567", "100", "2"},
			wantType: "END",
			contains: "Transaction cancelled.",
		},
		{
			name:     "check balance",
			steps:    []string{"2"},
			wantType: "END",
			contains: "GHS 1,000",
		},
		{
			name:     "exit",
			steps:    []string{"3"},
			wantType: "END",
			contains: "Goodbye",
		},
		{
			name:     "invalid choice",
			steps:    []string{"9"},
			wantType: "END",
			contains: "Invalid choice",
		},
		{
			name:     "invalid recipient",
			steps:    []string{"1", "abc"},
			wantType: "END",
			contains: "does not look like a phone number",
		},
		{
			name:     "invalid amount",
			steps:    []string{"1", "0241234567", "abc"},
			wantType: "END",
			contains: "not a valid amount",
		},
		{
			name:     "stops at recipient prompt",
			steps:    []string{"1"},
			wantType: "CON",
			contains: "Enter recipient number:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newStackFor(t, callback)

			scr := s.dial(t, "*124#")
			if !strings.Contains(scr.Text, "Welcome to MyBank") {
				t.Fatalf("first screen = %q", scr.Text)
			}

			for _, step := range tt.steps {
				scr = s.input(t, scr.SessionID, step)
			}

			if scr.Type != tt.wantType {
				t.Errorf("type = %s, want %s (text: %q)", scr.Type, tt.wantType, scr.Text)
			}
			if !strings.Contains(scr.Text, tt.contains) {
				t.Errorf("text = %q, want to contain %q", scr.Text, tt.contains)
			}
		})
	}
}

// The example holds no session state of its own, so interleaved sessions must
// not contaminate one another.
func TestE2E_Example_InterleavedSessions(t *testing.T) {
	callback := startExample(t)
	s := newStackFor(t, callback)

	a := s.dial(t, "*124#")
	b := s.dial(t, "*124#")

	// Drive both conversations one step at a time, alternating.
	a = s.input(t, a.SessionID, "1")
	b = s.input(t, b.SessionID, "2")

	if !strings.Contains(a.Text, "Enter recipient number:") {
		t.Errorf("session A = %q, want the recipient prompt", a.Text)
	}
	if b.Type != "END" || !strings.Contains(b.Text, "GHS 1,000") {
		t.Errorf("session B = %s %q, want END with the balance", b.Type, b.Text)
	}

	a = s.input(t, a.SessionID, "0241234567")
	if !strings.Contains(a.Text, "Enter amount:") {
		t.Errorf("session A = %q after B ended; state leaked between sessions", a.Text)
	}
}

// A session against the example must survive a full four-step transfer with
// the accumulated text arriving intact.
func TestE2E_Example_AccumulationReachesApp(t *testing.T) {
	callback := startExample(t)
	s := newStackFor(t, callback)

	scr := s.dial(t, "*124#")
	for i, step := range []string{"1", "0241234567", "250"} {
		scr = s.input(t, scr.SessionID, step)
		if scr.Type != "CON" {
			t.Fatalf("step %d ended the session early: %s %q", i, scr.Type, scr.Text)
		}
	}

	// The confirmation screen proves the app saw both the recipient and the
	// amount, which it could only do via the accumulated text.
	if !strings.Contains(scr.Text, "Send GHS 250 to 0241234567?") {
		t.Errorf("confirmation = %q", scr.Text)
	}

	scr = s.input(t, scr.SessionID, "1")
	if scr.Status != "COMPLETED" {
		t.Errorf("status = %s, want COMPLETED", scr.Status)
	}
}
