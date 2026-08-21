// Package e2e drives the whole stack over real HTTP: a real simulator server on
// a real socket, the real session engine, the real HTTP application client, and
// a stub standing in for the developer's application.
//
// Nothing here is mocked except the developer's own code. There is no browser
// and no phone, so the test is deterministic and runs in CI -- manual phone
// testing is an acceptance activity, never the thing that catches a regression.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yeboahd24/ussd-lab/internal/appclient"
	"github.com/yeboahd24/ussd-lab/internal/session"
	"github.com/yeboahd24/ussd-lab/internal/simulator"
	"github.com/yeboahd24/ussd-lab/internal/storage/memory"
)

// bankApp is a minimal developer application implementing the *124# menu from
// the design documents. It is deliberately written the way a developer would
// write one: stateless, deriving position from the accumulated text.
func bankApp() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		var parts []string
		if req.Text != "" {
			parts = strings.Split(req.Text, "*")
		}

		w.Header().Set("Content-Type", "text/plain")

		switch {
		case len(parts) == 0:
			fmt.Fprint(w, "CON Welcome to MyBank\n1. Send Money\n2. Check Balance\n3. Exit")
		case parts[0] == "2":
			fmt.Fprint(w, "END Your balance is GHS 1,000")
		case parts[0] == "3":
			fmt.Fprint(w, "END Goodbye")
		case parts[0] == "1":
			switch len(parts) {
			case 1:
				fmt.Fprint(w, "CON Enter recipient number:")
			case 2:
				fmt.Fprint(w, "CON Enter amount:")
			case 3:
				fmt.Fprintf(w, "CON Send GHS %s to %s?\n1. Confirm\n2. Cancel", parts[2], parts[1])
			default:
				if parts[3] == "1" {
					fmt.Fprint(w, "END Transaction successful.")
				} else {
					fmt.Fprint(w, "END Transaction cancelled.")
				}
			}
		default:
			fmt.Fprint(w, "END Invalid choice.")
		}
	})
}

type stack struct {
	baseURL string
	client  *http.Client
}

// attach performs the QR-scan handshake. The client's cookie jar then carries
// the attach cookie for every subsequent call, exactly as a phone browser does.
func (s *stack) attach(t *testing.T, srv *simulator.Server) {
	t.Helper()

	token, err := srv.IssueToken()
	if err != nil {
		t.Fatalf("IssueToken() error = %v", err)
	}

	resp, err := s.client.Get(s.baseURL + "/s/" + token)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("attach status = %d, want 200 after redirect", resp.StatusCode)
	}
}

// newStack wires the real components together and serves on an ephemeral port.
func newStack(t *testing.T, appHandler http.Handler, timeout time.Duration) *stack {
	t.Helper()

	devApp := httptest.NewServer(appHandler)
	t.Cleanup(devApp.Close)

	client, err := appclient.New(appclient.Options{
		CallbackURL: devApp.URL + "/ussd",
		Timeout:     2 * time.Second,
	})
	if err != nil {
		t.Fatalf("appclient.New() error = %v", err)
	}

	engine, err := session.New(session.Options{
		Store:   memory.New(),
		App:     client,
		Events:  session.NewMemorySink(),
		Timeout: timeout,
	})
	if err != nil {
		t.Fatalf("session.New() error = %v", err)
	}

	srv, err := simulator.New(simulator.Options{
		Engine:      engine,
		ProjectID:   "my-fintech",
		ServiceCode: "*124#",
		BindAddr:    "127.0.0.1:0", // ephemeral: parallel-safe, no port conflicts
	})
	if err != nil {
		t.Fatalf("simulator.New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = srv.Shutdown()
	})

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New() error = %v", err)
	}

	s := &stack{
		baseURL: "http://" + srv.Addr().String(),
		client:  &http.Client{Timeout: 5 * time.Second, Jar: jar},
	}
	s.attach(t, srv)
	return s
}

type screen struct {
	SessionID string `json:"session_id"`
	Type      string `json:"type"`
	Text      string `json:"text"`
	Status    string `json:"status"`
}

func (s *stack) post(t *testing.T, path, body string) (int, string) {
	t.Helper()

	resp, err := s.client.Post(s.baseURL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, string(raw)
}

func (s *stack) dial(t *testing.T, code string) screen {
	t.Helper()

	status, body := s.post(t, "/api/dial", fmt.Sprintf(`{"service_code":%q}`, code))
	if status != http.StatusOK {
		t.Fatalf("dial status = %d: %s", status, body)
	}
	return decode(t, body)
}

func (s *stack) input(t *testing.T, sid, text string) screen {
	t.Helper()

	status, body := s.post(t, "/api/input",
		fmt.Sprintf(`{"session_id":%q,"text":%q}`, sid, text))
	if status != http.StatusOK {
		t.Fatalf("input %q status = %d: %s", text, status, body)
	}
	return decode(t, body)
}

func decode(t *testing.T, body string) screen {
	t.Helper()

	var s screen
	if err := json.NewDecoder(bytes.NewReader([]byte(body))).Decode(&s); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return s
}

// The headline acceptance test: a complete Send Money transaction over real
// HTTP, end to end.
func TestE2E_SendMoney(t *testing.T) {
	t.Parallel()

	s := newStack(t, bankApp(), 120*time.Second)

	scr := s.dial(t, "*124#")
	if scr.Type != "CON" || !strings.Contains(scr.Text, "Send Money") {
		t.Fatalf("first screen = %+v", scr)
	}

	for _, step := range []struct{ input, wantType, wantContains string }{
		{"1", "CON", "Enter recipient number:"},
		{"0241234567", "CON", "Enter amount:"},
		{"100", "CON", "Send GHS 100 to 0241234567?"},
		{"1", "END", "Transaction successful."},
	} {
		scr = s.input(t, scr.SessionID, step.input)
		if scr.Type != step.wantType {
			t.Fatalf("after %q: type = %s, want %s", step.input, scr.Type, step.wantType)
		}
		if !strings.Contains(scr.Text, step.wantContains) {
			t.Fatalf("after %q: text = %q, want to contain %q",
				step.input, scr.Text, step.wantContains)
		}
	}

	if scr.Status != "COMPLETED" {
		t.Errorf("final status = %s, want COMPLETED", scr.Status)
	}
}

func TestE2E_CheckBalance(t *testing.T) {
	t.Parallel()

	s := newStack(t, bankApp(), 120*time.Second)

	scr := s.dial(t, "*124#")
	scr = s.input(t, scr.SessionID, "2")

	if scr.Type != "END" {
		t.Fatalf("type = %s, want END", scr.Type)
	}
	if !strings.Contains(scr.Text, "GHS 1,000") {
		t.Errorf("text = %q", scr.Text)
	}
	if scr.Status != "COMPLETED" {
		t.Errorf("status = %s, want COMPLETED", scr.Status)
	}
}

// Multiple simultaneous sessions must not interfere -- MVP acceptance
// criterion "run multiple sessions" (design §28).
func TestE2E_ConcurrentSessions(t *testing.T) {
	t.Parallel()

	s := newStack(t, bankApp(), 120*time.Second)

	const n = 10
	done := make(chan string, n)

	for i := 0; i < n; i++ {
		go func(i int) {
			scr := s.dial(t, "*124#")
			scr = s.input(t, scr.SessionID, "1")
			scr = s.input(t, scr.SessionID, fmt.Sprintf("024000000%d", i))
			scr = s.input(t, scr.SessionID, "100")
			scr = s.input(t, scr.SessionID, "1")

			if scr.Type != "END" {
				t.Errorf("session %d: type = %s, want END", i, scr.Type)
			}
			done <- scr.SessionID
		}(i)
	}

	seen := map[string]bool{}
	for i := 0; i < n; i++ {
		id := <-done
		if seen[id] {
			t.Errorf("duplicate session id %q across concurrent sessions", id)
		}
		seen[id] = true
	}
}

// When the developer's application is down, the failure must be attributed to
// the application -- not reported as a simulator error.
func TestE2E_ApplicationDown(t *testing.T) {
	t.Parallel()

	// A handler that is never served: point the client at a closed port.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	client, err := appclient.New(appclient.Options{
		CallbackURL: deadURL + "/ussd",
		Timeout:     time.Second,
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
		Engine: engine, ProjectID: "p", ServiceCode: "*124#", BindAddr: "127.0.0.1:0",
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
		client:  &http.Client{Timeout: 5 * time.Second, Jar: jar},
	}
	s.attach(t, srv)

	status, body := s.post(t, "/api/dial", `{"service_code":"*124#"}`)
	if status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", status, body)
	}
	if !strings.Contains(body, "APPLICATION_UNAVAILABLE") {
		t.Errorf("body = %s, want APPLICATION_UNAVAILABLE", body)
	}
	// The hint must tell the developer what to do.
	if !strings.Contains(body, "ussd.yaml") {
		t.Errorf("body = %s, want an actionable hint", body)
	}
}

// An application that answers with something other than CON/END must be
// reported as malformed, with the offending output visible.
func TestE2E_MalformedApplicationResponse(t *testing.T) {
	t.Parallel()

	s := newStack(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html><body>Internal Server Error</body></html>")
	}), 120*time.Second)

	status, body := s.post(t, "/api/dial", `{"service_code":"*124#"}`)
	if status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", status, body)
	}
	if !strings.Contains(body, "MALFORMED_RESPONSE") {
		t.Errorf("body = %s, want MALFORMED_RESPONSE", body)
	}
}

// A slow application must be reported as a timeout, distinct from being down.
func TestE2E_ApplicationTimeout(t *testing.T) {
	t.Parallel()

	// The handler must outlive the client's 2s timeout but still return on its
	// own. Blocking on a channel closed in cleanup would deadlock:
	// httptest.Server.Close waits for in-flight handlers, and cleanups run
	// last-registered-first.
	s := newStack(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(3 * time.Second):
		case <-r.Context().Done():
		}
	}), 120*time.Second)

	status, body := s.post(t, "/api/dial", `{"service_code":"*124#"}`)
	if status != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504: %s", status, body)
	}
	if !strings.Contains(body, "APPLICATION_TIMEOUT") {
		t.Errorf("body = %s, want APPLICATION_TIMEOUT", body)
	}
}

// Session expiry over the real stack, with a short configured timeout.
func TestE2E_SessionExpiry(t *testing.T) {
	t.Parallel()

	s := newStack(t, bankApp(), 50*time.Millisecond)

	scr := s.dial(t, "*124#")

	// A real deadline is genuinely elapsing here; keeping it tiny keeps the
	// test fast. Engine-level timeout logic is covered deterministically with a
	// fake clock in internal/session.
	time.Sleep(120 * time.Millisecond)

	status, body := s.post(t, "/api/input",
		fmt.Sprintf(`{"session_id":%q,"text":"1"}`, scr.SessionID))

	if status != http.StatusGone {
		t.Fatalf("status = %d, want 410: %s", status, body)
	}
	if !strings.Contains(body, "SESSION_TIMEOUT") {
		t.Errorf("body = %s, want SESSION_TIMEOUT", body)
	}
}

// The full input history reaches the application accumulated and in order.
func TestE2E_TextAccumulation(t *testing.T) {
	t.Parallel()

	received := make(chan string, 10)

	s := newStack(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		received <- req.Text

		if strings.Count(req.Text, "*") >= 2 {
			fmt.Fprint(w, "END done")
			return
		}
		fmt.Fprint(w, "CON next")
	}), 120*time.Second)

	scr := s.dial(t, "*124#")
	for _, in := range []string{"1", "0241234567", "100"} {
		scr = s.input(t, scr.SessionID, in)
	}

	want := []string{"", "1", "1*0241234567", "1*0241234567*100"}
	for i, w := range want {
		got := <-received
		if got != w {
			t.Errorf("request %d text = %q, want %q", i, got, w)
		}
	}
}
