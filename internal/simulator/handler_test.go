package simulator_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yeboahd24/ussd-lab/internal/protocol"
	"github.com/yeboahd24/ussd-lab/internal/session"
	"github.com/yeboahd24/ussd-lab/internal/simulator"
	"github.com/yeboahd24/ussd-lab/internal/storage/memory"
)

type stubApp struct {
	responses []protocol.USSDResponse
	err       error
	i         int
}

func (s *stubApp) Send(_ context.Context, _ *protocol.USSDRequest) (*protocol.USSDResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.i >= len(s.responses) {
		return nil, errors.New("stubApp: out of scripted responses")
	}
	resp := s.responses[s.i]
	s.i++
	return &resp, nil
}

type fixture struct {
	handler http.Handler
	clock   *session.FakeClock
	srv     *simulator.Server
	cookie  *http.Cookie
}

func newFixture(t *testing.T, app protocol.ApplicationClient) *fixture {
	t.Helper()

	clock := session.NewFakeClock(time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC))
	engine, err := session.New(session.Options{
		Store:   memory.New(),
		App:     app,
		Clock:   clock,
		Timeout: 120 * time.Second,
	})
	if err != nil {
		t.Fatalf("session.New() error = %v", err)
	}

	srv, err := simulator.New(simulator.Options{
		Engine:      engine,
		ProjectID:   "my-fintech",
		ServiceCode: "*124#",
		BindAddr:    "127.0.0.1:0",
	})
	if err != nil {
		t.Fatalf("simulator.New() error = %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown() })

	f := &fixture{handler: srv.Handler(), clock: clock, srv: srv}
	f.attach(t)
	return f
}

// attach performs the QR-scan handshake so the fixture can call the API.
func (f *fixture) attach(t *testing.T) {
	t.Helper()

	token, err := f.srv.IssueToken()
	if err != nil {
		t.Fatalf("IssueToken() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/s/"+token, nil)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("attach status = %d, want 303: %s", rec.Code, rec.Body)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "ussd_attach" {
			f.cookie = c
			return
		}
	}
	t.Fatal("attach did not set the ussd_attach cookie")
}

func (f *fixture) post(t *testing.T, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if f.cookie != nil {
		req.AddCookie(f.cookie)
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

func decodeScreen(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("response is not JSON: %v\n%s", err, rec.Body.String())
	}
	return m
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()

	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("error response is not JSON: %v\n%s", err, rec.Body.String())
	}
	return body.Error.Code
}

func TestDial_ReturnsFirstScreen(t *testing.T) {
	t.Parallel()

	f := newFixture(t, &stubApp{responses: []protocol.USSDResponse{
		protocol.Continue("Welcome to MyBank\n1. Send Money"),
	}})

	rec := f.post(t, "/api/dial", `{"service_code":"*124#"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}

	got := decodeScreen(t, rec)
	if got["type"] != "CON" {
		t.Errorf("type = %v, want CON", got["type"])
	}
	if got["status"] != "ACTIVE" {
		t.Errorf("status = %v, want ACTIVE", got["status"])
	}
	if id, _ := got["session_id"].(string); !strings.HasPrefix(id, "sess_") {
		t.Errorf("session_id = %v", got["session_id"])
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("missing X-Content-Type-Options: nosniff")
	}
}

// Dialling a code the project does not answer fails, exactly as it would on a
// real network -- and prevents the browser injecting arbitrary service codes.
func TestDial_UnknownServiceCode(t *testing.T) {
	t.Parallel()

	f := newFixture(t, &stubApp{})

	rec := f.post(t, "/api/dial", `{"service_code":"*999#"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if got := errorCode(t, rec); got != simulator.CodeUnknownServiceCode {
		t.Errorf("code = %s, want %s", got, simulator.CodeUnknownServiceCode)
	}
}

func TestDial_PhoneNumberValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		phone string
		ok    bool
	}{
		{"omitted uses default", "", true},
		{"valid", "233240000001", true},
		{"too short", "123", false},
		{"too long", "1234567890123456", false},
		{"letters", "23324000000a", false},
		{"injection attempt", "'; DROP TABLE--", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t, &stubApp{responses: []protocol.USSDResponse{
				protocol.Continue("Menu"),
			}})

			body := fmt.Sprintf(`{"service_code":"*124#","phone_number":%q}`, tt.phone)
			rec := f.post(t, "/api/dial", body)

			if tt.ok && rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200: %s", rec.Code, rec.Body)
			}
			if !tt.ok && rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
		})
	}
}

func TestInput_AdvancesSession(t *testing.T) {
	t.Parallel()

	f := newFixture(t, &stubApp{responses: []protocol.USSDResponse{
		protocol.Continue("Menu"),
		protocol.End("Done"),
	}})

	dial := decodeScreen(t, f.post(t, "/api/dial", `{"service_code":"*124#"}`))
	sid := dial["session_id"].(string)

	rec := f.post(t, "/api/input", fmt.Sprintf(`{"session_id":%q,"text":"1"}`, sid))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}

	got := decodeScreen(t, rec)
	if got["type"] != "END" {
		t.Errorf("type = %v, want END", got["type"])
	}
	if got["status"] != "COMPLETED" {
		t.Errorf("status = %v, want COMPLETED", got["status"])
	}
}

// Each error class must map to its own status code, so the UI and the test
// runner can tell them apart.
func TestErrorMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		setup      func(t *testing.T) (*fixture, string)
		path, body string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "unknown session",
			setup:      func(t *testing.T) (*fixture, string) { return newFixture(t, &stubApp{}), "" },
			path:       "/api/input",
			body:       `{"session_id":"sess_nope","text":"1"}`,
			wantStatus: http.StatusNotFound,
			wantCode:   simulator.CodeSessionNotFound,
		},
		{
			name:       "malformed json",
			setup:      func(t *testing.T) (*fixture, string) { return newFixture(t, &stubApp{}), "" },
			path:       "/api/dial",
			body:       `{not json`,
			wantStatus: http.StatusBadRequest,
			wantCode:   simulator.CodeBadRequest,
		},
		{
			name:       "empty body",
			setup:      func(t *testing.T) (*fixture, string) { return newFixture(t, &stubApp{}), "" },
			path:       "/api/dial",
			body:       ``,
			wantStatus: http.StatusBadRequest,
			wantCode:   simulator.CodeBadRequest,
		},
		{
			name:       "unknown field",
			setup:      func(t *testing.T) (*fixture, string) { return newFixture(t, &stubApp{}), "" },
			path:       "/api/dial",
			body:       `{"service_code":"*124#","callback":"http://evil.example.com"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   simulator.CodeBadRequest,
		},
		{
			name:       "missing session id",
			setup:      func(t *testing.T) (*fixture, string) { return newFixture(t, &stubApp{}), "" },
			path:       "/api/input",
			body:       `{"text":"1"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   simulator.CodeBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f, _ := tt.setup(t)
			rec := f.post(t, tt.path, tt.body)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d: %s", rec.Code, tt.wantStatus, rec.Body)
			}
			if got := errorCode(t, rec); got != tt.wantCode {
				t.Errorf("code = %s, want %s", got, tt.wantCode)
			}
		})
	}
}

// A failure inside the developer's application must be reported as a gateway
// error, distinct from a simulator error, so the developer knows where to look.
func TestApplicationFailure_IsAGatewayError(t *testing.T) {
	t.Parallel()

	f := newFixture(t, &stubApp{err: errors.New("connection refused")})

	rec := f.post(t, "/api/dial", `{"service_code":"*124#"}`)
	if rec.Code < 500 {
		t.Fatalf("status = %d, want a 5xx", rec.Code)
	}
	if rec.Code == http.StatusInternalServerError {
		t.Errorf("status = 500: an application failure must not look like a simulator failure")
	}
}

func TestSessionTimeout_MapsToGone(t *testing.T) {
	t.Parallel()

	f := newFixture(t, &stubApp{responses: []protocol.USSDResponse{
		protocol.Continue("Menu"),
	}})

	dial := decodeScreen(t, f.post(t, "/api/dial", `{"service_code":"*124#"}`))
	sid := dial["session_id"].(string)

	f.clock.Advance(121 * time.Second)

	rec := f.post(t, "/api/input", fmt.Sprintf(`{"session_id":%q,"text":"1"}`, sid))
	if rec.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410", rec.Code)
	}
	if got := errorCode(t, rec); got != simulator.CodeSessionTimeout {
		t.Errorf("code = %s, want %s", got, simulator.CodeSessionTimeout)
	}
}

func TestCancel(t *testing.T) {
	t.Parallel()

	f := newFixture(t, &stubApp{responses: []protocol.USSDResponse{
		protocol.Continue("Menu"),
	}})

	dial := decodeScreen(t, f.post(t, "/api/dial", `{"service_code":"*124#"}`))
	sid := dial["session_id"].(string)

	rec := f.post(t, "/api/cancel", fmt.Sprintf(`{"session_id":%q}`, sid))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if got := decodeScreen(t, rec); got["status"] != "CANCELLED" {
		t.Errorf("status = %v, want CANCELLED", got["status"])
	}

	// A cancelled session refuses further input.
	rec = f.post(t, "/api/input", fmt.Sprintf(`{"session_id":%q,"text":"1"}`, sid))
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
}

func TestGetSession(t *testing.T) {
	t.Parallel()

	f := newFixture(t, &stubApp{responses: []protocol.USSDResponse{
		protocol.Continue("Menu"),
	}})

	dial := decodeScreen(t, f.post(t, "/api/dial", `{"service_code":"*124#"}`))
	sid := dial["session_id"].(string)

	req := httptest.NewRequest(http.MethodGet, "/api/session/"+sid, nil)
	req.AddCookie(f.cookie)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}

	got := decodeScreen(t, rec)
	if got["status"] != "ACTIVE" {
		t.Errorf("status = %v", got["status"])
	}
	if got["service_code"] != "*124#" {
		t.Errorf("service_code = %v", got["service_code"])
	}
}

// The body limit protects a LAN-exposed server from a large POST.
func TestRequestBodyLimit(t *testing.T) {
	t.Parallel()

	f := newFixture(t, &stubApp{})

	huge := fmt.Sprintf(`{"service_code":"*124#","phone_number":%q}`,
		strings.Repeat("9", simulator.MaxRequestBytes*2))

	rec := f.post(t, "/api/dial", huge)
	if rec.Code != http.StatusRequestEntityTooLarge && rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 413 or 400", rec.Code)
	}
}

func TestHealthz(t *testing.T) {
	t.Parallel()

	f := newFixture(t, &stubApp{})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := decodeScreen(t, rec); got["status"] != "ok" {
		t.Errorf("status = %v", got["status"])
	}

	// Liveness must not leak project detail to an unattached caller.
	if body := rec.Body.String(); strings.Contains(body, "my-fintech") ||
		strings.Contains(body, "*124#") {
		t.Errorf("/healthz leaks project detail: %s", body)
	}
}

// There must be no route that accepts a destination from the caller.
func TestNoProxyRoute(t *testing.T) {
	t.Parallel()

	f := newFixture(t, &stubApp{})

	for _, path := range []string{
		"/proxy?url=http://evil.example.com",
		"/api/proxy",
		"/api/dial?callback=http://evil.example.com",
	} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(nil))
			rec := httptest.NewRecorder()
			f.handler.ServeHTTP(rec, req)

			if rec.Code == http.StatusOK {
				t.Errorf("%s returned 200; the simulator must not proxy", path)
			}
		})
	}
}

func TestNew_Validation(t *testing.T) {
	t.Parallel()

	if _, err := simulator.New(simulator.Options{}); err == nil {
		t.Error("New() without Engine error = nil, want error")
	}
}

// Only the methods the API declares may reach a handler.
func TestMethodRouting(t *testing.T) {
	t.Parallel()

	f := newFixture(t, &stubApp{})

	tests := []struct{ method, path string }{
		{http.MethodGet, "/api/dial"},
		{http.MethodPut, "/api/input"},
		{http.MethodDelete, "/api/cancel"},
		{http.MethodPost, "/api/session/sess_1"},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader("{}"))
			req.AddCookie(f.cookie)
			rec := httptest.NewRecorder()
			f.handler.ServeHTTP(rec, req)

			if rec.Code == http.StatusOK {
				t.Errorf("%s %s returned 200; the method is not routed",
					tt.method, tt.path)
			}
		})
	}
}

// A panic in a handler must not take down the process: the CLI and the server
// share one (ADR-001).
func TestPanicRecovery(t *testing.T) {
	t.Parallel()

	f := newFixture(t, &panicApp{})

	rec := f.post(t, "/api/dial", `{"service_code":"*124#"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if got := errorCode(t, rec); got != simulator.CodeInternal {
		t.Errorf("code = %s, want %s", got, simulator.CodeInternal)
	}
}

type panicApp struct{}

func (panicApp) Send(context.Context, *protocol.USSDRequest) (*protocol.USSDResponse, error) {
	panic("application client exploded")
}

// The response to the phone must never leak internal detail beyond the
// developer-facing hint.
func TestErrorBodyShape(t *testing.T) {
	t.Parallel()

	f := newFixture(t, &stubApp{})

	rec := f.post(t, "/api/dial", `{"service_code":"*999#"}`)

	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Hint    string `json:"hint"`
		} `json:"error"`
		Unexpected map[string]any `json:"-"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body is not JSON: %v", err)
	}
	if body.Error.Code == "" || body.Error.Message == "" {
		t.Errorf("error body incomplete: %s", rec.Body)
	}

	// No stack traces, no file paths.
	for _, leak := range []string{".go:", "goroutine", "/home/", "0x"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Errorf("error body leaks %q: %s", leak, rec.Body)
		}
	}
}

func TestSessionView_OmitsInputText(t *testing.T) {
	t.Parallel()

	f := newFixture(t, &stubApp{responses: []protocol.USSDResponse{
		protocol.Continue("Enter PIN:"),
		protocol.Continue("Thanks"),
	}})

	dial := decodeScreen(t, f.post(t, "/api/dial", `{"service_code":"*124#"}`))
	sid := dial["session_id"].(string)
	f.post(t, "/api/input", fmt.Sprintf(`{"session_id":%q,"text":"4321"}`, sid))

	rec := f.get(t, "/api/session/"+sid)

	// The session view reports how many inputs were given, never what they
	// were: an input may be a PIN.
	if strings.Contains(rec.Body.String(), "4321") {
		t.Errorf("session view leaked user input: %s", rec.Body)
	}
	if got := decodeScreen(t, rec)["input_count"]; got != float64(1) {
		t.Errorf("input_count = %v, want 1", got)
	}
}

func TestServer_PortAndAddr(t *testing.T) {
	t.Parallel()

	engine, err := session.New(session.Options{
		Store: memory.New(), App: &stubApp{}, Timeout: time.Minute,
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
	defer srv.Shutdown()

	if srv.Port() == 0 {
		t.Error("Port() = 0; an ephemeral port must be resolved at New()")
	}
	if srv.Addr() == nil {
		t.Error("Addr() = nil")
	}
}

func TestServer_ServeAndShutdown(t *testing.T) {
	t.Parallel()

	engine, err := session.New(session.Options{
		Store: memory.New(), App: &stubApp{}, Timeout: time.Minute,
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
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()

	// The server must be answering before shutdown is requested.
	url := "http://" + srv.Addr().String() + "/healthz"
	var reachable bool
	for i := 0; i < 100; i++ {
		if resp, err := http.Get(url); err == nil {
			resp.Body.Close()
			reachable = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !reachable {
		t.Fatal("server never became reachable")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve() error = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve() did not return after cancellation")
	}
}
