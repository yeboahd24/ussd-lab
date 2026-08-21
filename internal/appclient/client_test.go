package appclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yeboahd24/ussd-lab/internal/protocol"
)

func testRequest() *protocol.USSDRequest {
	return &protocol.USSDRequest{
		RequestID:   "req_1",
		SessionID:   "sess_1",
		ServiceCode: "*124#",
		PhoneNumber: "233240000001",
		Network:     protocol.NetworkSimulator,
		Text:        "1",
		Timestamp:   time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC),
	}
}

// newClient builds a client pointed at url, failing the test on error.
func newClient(t *testing.T, url string, opts ...func(*Options)) *HTTPClient {
	t.Helper()

	o := Options{CallbackURL: url, Timeout: 2 * time.Second}
	for _, fn := range opts {
		fn(&o)
	}

	c, err := New(o)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return c
}

func TestSend_ParsesCON(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "CON Enter recipient number:")
	}))
	defer srv.Close()

	got, err := newClient(t, srv.URL).Send(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if got.Type != protocol.TypeCON {
		t.Errorf("Type = %q, want CON", got.Type)
	}
	if got.Text != "Enter recipient number:" {
		t.Errorf("Text = %q", got.Text)
	}
	if got.IsFinal() {
		t.Error("CON response reported as final")
	}
}

func TestSend_ParsesEND(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "END Transaction successful.\n")
	}))
	defer srv.Close()

	got, err := newClient(t, srv.URL).Send(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if !got.IsFinal() {
		t.Error("END response not reported as final")
	}
	if got.Text != "Transaction successful." {
		t.Errorf("Text = %q", got.Text)
	}
}

// The wire format is a public contract; assert on the actual bytes sent.
func TestSend_RequestWireFormat(t *testing.T) {
	t.Parallel()

	type captured struct {
		method      string
		contentType string
		body        map[string]any
	}
	got := make(chan captured, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		got <- captured{
			method:      r.Method,
			contentType: r.Header.Get("Content-Type"),
			body:        body,
		}
		fmt.Fprint(w, "END ok")
	}))
	defer srv.Close()

	if _, err := newClient(t, srv.URL).Send(context.Background(), testRequest()); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	c := <-got

	if c.method != http.MethodPost {
		t.Errorf("method = %s, want POST", c.method)
	}
	if c.contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", c.contentType)
	}

	want := map[string]string{
		"request_id":   "req_1",
		"session_id":   "sess_1",
		"service_code": "*124#",
		"phone_number": "233240000001",
		"network":      "SIMULATOR",
		"text":         "1",
	}
	for k, v := range want {
		if c.body[k] != v {
			t.Errorf("body[%q] = %v, want %q", k, c.body[k], v)
		}
	}
	if _, ok := c.body["timestamp"]; !ok {
		t.Error("body missing timestamp")
	}
	// Metadata is omitempty: absent rather than null when unused.
	if _, ok := c.body["metadata"]; ok {
		t.Error("empty metadata should be omitted from the payload")
	}
}

func TestSend_Unavailable(t *testing.T) {
	t.Parallel()

	// Start then immediately stop a server to obtain a closed port.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	_, err := newClient(t, url).Send(context.Background(), testRequest())
	if err == nil {
		t.Fatal("Send() error = nil, want unavailable")
	}

	code, ok := CodeOf(err)
	if !ok || code != CodeUnavailable {
		t.Errorf("code = %v, want %s (err: %v)", code, CodeUnavailable, err)
	}
}

func TestSend_Timeout(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer func() {
		close(release)
		srv.Close()
	}()

	client := newClient(t, srv.URL, func(o *Options) {
		o.Timeout = 50 * time.Millisecond
	})

	_, err := client.Send(context.Background(), testRequest())
	if err == nil {
		t.Fatal("Send() error = nil, want timeout")
	}

	code, ok := CodeOf(err)
	if !ok || code != CodeTimeout {
		t.Errorf("code = %v, want %s (err: %v)", code, CodeTimeout, err)
	}
}

// A caller-imposed deadline must classify as a timeout, not as unavailable.
func TestSend_ContextDeadline(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer func() {
		close(release)
		srv.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := newClient(t, srv.URL).Send(ctx, testRequest())
	if err == nil {
		t.Fatal("Send() error = nil, want timeout")
	}

	code, ok := CodeOf(err)
	if !ok || code != CodeTimeout {
		t.Errorf("code = %v, want %s (err: %v)", code, CodeTimeout, err)
	}
}

func TestSend_HTTPStatusError(t *testing.T) {
	t.Parallel()

	for _, status := range []int{400, 404, 500, 502} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				fmt.Fprint(w, "CON this should be ignored")
			}))
			defer srv.Close()

			_, err := newClient(t, srv.URL).Send(context.Background(), testRequest())
			if err == nil {
				t.Fatal("Send() error = nil, want HTTP status error")
			}

			code, ok := CodeOf(err)
			if !ok || code != CodeHTTPStatus {
				t.Errorf("code = %v, want %s (err: %v)", code, CodeHTTPStatus, err)
			}
		})
	}
}

func TestSend_Malformed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{"empty body", ""},
		{"html error page", "<html><body>oops</body></html>"},
		{"json instead of text", `{"type":"CON","text":"hi"}`},
		{"lowercase keyword", "con Enter amount:"},
		{"keyword only", "CON"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, tt.body)
			}))
			defer srv.Close()

			_, err := newClient(t, srv.URL).Send(context.Background(), testRequest())
			if err == nil {
				t.Fatal("Send() error = nil, want malformed")
			}

			code, ok := CodeOf(err)
			if !ok || code != CodeMalformed {
				t.Errorf("code = %v, want %s (err: %v)", code, CodeMalformed, err)
			}
		})
	}
}

func TestSend_TooLarge(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "CON "+strings.Repeat("x", protocol.MaxResponseBytes+100))
	}))
	defer srv.Close()

	_, err := newClient(t, srv.URL).Send(context.Background(), testRequest())
	if err == nil {
		t.Fatal("Send() error = nil, want too large")
	}

	code, ok := CodeOf(err)
	if !ok || code != CodeTooLarge {
		t.Errorf("code = %v, want %s (err: %v)", code, CodeTooLarge, err)
	}
}

// Following a redirect would send USSD traffic, including user input, to a
// host the developer never configured.
func TestSend_RedirectIsBlocked(t *testing.T) {
	t.Parallel()

	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "END you should never get here")
	}))
	defer elsewhere.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusFound)
	}))
	defer srv.Close()

	_, err := newClient(t, srv.URL).Send(context.Background(), testRequest())
	if err == nil {
		t.Fatal("Send() error = nil, want redirect blocked")
	}

	code, ok := CodeOf(err)
	if !ok || code != CodeRedirectBlocked {
		t.Errorf("code = %v, want %s (err: %v)", code, CodeRedirectBlocked, err)
	}
}

func TestSend_InvalidRequestNotSent(t *testing.T) {
	t.Parallel()

	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		fmt.Fprint(w, "END ok")
	}))
	defer srv.Close()

	req := testRequest()
	req.SessionID = ""

	_, err := newClient(t, srv.URL).Send(context.Background(), req)
	if err == nil {
		t.Fatal("Send() error = nil, want invalid request")
	}

	code, ok := CodeOf(err)
	if !ok || code != CodeInvalidRequest {
		t.Errorf("code = %v, want %s", code, CodeInvalidRequest)
	}
	if called {
		t.Error("an invalid request was still sent to the application")
	}
}

func TestSend_LatencyInjection(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "END ok")
	}))
	defer srv.Close()

	const latency = 80 * time.Millisecond
	client := newClient(t, srv.URL, func(o *Options) { o.Latency = latency })

	start := time.Now()
	if _, err := client.Send(context.Background(), testRequest()); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if elapsed := time.Since(start); elapsed < latency {
		t.Errorf("elapsed = %v, want at least %v", elapsed, latency)
	}
}

func TestNew_RequiresCallbackURL(t *testing.T) {
	t.Parallel()

	if _, err := New(Options{}); err == nil {
		t.Error("New() error = nil, want error for missing CallbackURL")
	}
}

// Every classified error should carry an actionable hint.
func TestErrors_HaveHints(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "garbage")
	}))
	defer srv.Close()

	_, err := newClient(t, srv.URL).Send(context.Background(), testRequest())

	var ae *Error
	if !asAppError(err, &ae) {
		t.Fatalf("error = %v, want *Error", err)
	}
	if ae.Hint == "" {
		t.Error("malformed-response error carries no hint")
	}
}

func asAppError(err error, target **Error) bool {
	if e, ok := err.(*Error); ok {
		*target = e
		return true
	}
	return false
}
