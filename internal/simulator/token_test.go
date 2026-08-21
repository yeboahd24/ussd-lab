package simulator_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yeboahd24/ussd-lab/internal/session"
	"github.com/yeboahd24/ussd-lab/internal/simulator"
)

func TestToken_IssueAndValidate(t *testing.T) {
	t.Parallel()

	clock := session.NewFakeClock(time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC))
	store := simulator.NewTokenStore(time.Hour, clock)

	token, err := store.Issue()
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if !store.Valid(token) {
		t.Error("a freshly issued token is not valid")
	}
	if store.Valid("not-a-real-token") {
		t.Error("an unknown token validated")
	}
	if store.Valid("") {
		t.Error("an empty token validated")
	}
}

func TestToken_Expires(t *testing.T) {
	t.Parallel()

	clock := session.NewFakeClock(time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC))
	store := simulator.NewTokenStore(30*time.Minute, clock)

	token, _ := store.Issue()
	clock.Advance(31 * time.Minute)

	if store.Valid(token) {
		t.Error("an expired token still validated")
	}
}

func TestToken_Revoke(t *testing.T) {
	t.Parallel()

	store := simulator.NewTokenStore(time.Hour, nil)
	token, _ := store.Issue()

	store.Revoke(token)
	if store.Valid(token) {
		t.Error("a revoked token still validated")
	}
}

// Tokens are the only access control on a LAN-exposed port, so they must be
// unguessable and never repeat.
func TestToken_UniqueAndLong(t *testing.T) {
	t.Parallel()

	store := simulator.NewTokenStore(time.Hour, nil)
	seen := map[string]bool{}

	for i := 0; i < 500; i++ {
		token, err := store.Issue()
		if err != nil {
			t.Fatalf("Issue() error = %v", err)
		}
		if seen[token] {
			t.Fatalf("duplicate token %q", token)
		}
		seen[token] = true

		if len(token) < 40 {
			t.Fatalf("token %q is only %d characters; too short to resist guessing",
				token, len(token))
		}
	}
}

// An unattached device must not be able to touch the API.
func TestAttach_APIIsGated(t *testing.T) {
	t.Parallel()

	f := newFixture(t, &stubApp{})
	f.cookie = nil // simulate a device that never scanned the QR code

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/dial", `{"service_code":"*124#"}`},
		{http.MethodPost, "/api/input", `{"session_id":"sess_x","text":"1"}`},
		{http.MethodPost, "/api/cancel", `{"session_id":"sess_x"}`},
		{http.MethodGet, "/api/info", ``},
		{http.MethodGet, "/api/session/sess_x", ``},
	} {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			f.handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403", rec.Code)
			}
			if got := errorCode(t, rec); got != simulator.CodeAttachRequired {
				t.Errorf("code = %s, want %s", got, simulator.CodeAttachRequired)
			}
		})
	}
}

func TestAttach_RejectsBadToken(t *testing.T) {
	t.Parallel()

	f := newFixture(t, &stubApp{})

	for _, token := range []string{"nope", "", strings.Repeat("a", 43)} {
		t.Run(token, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/s/"+token, nil)
			rec := httptest.NewRecorder()
			f.handler.ServeHTTP(rec, req)

			if rec.Code == http.StatusSeeOther {
				t.Errorf("token %q was accepted", token)
			}
		})
	}
}

// The cookie must not be readable by script, and the token must not survive in
// the address bar after attaching.
func TestAttach_CookieHardening(t *testing.T) {
	t.Parallel()

	f := newFixture(t, &stubApp{})

	if f.cookie == nil {
		t.Fatal("no attach cookie")
	}
	if !f.cookie.HttpOnly {
		t.Error("attach cookie is not HttpOnly; script could read the token")
	}
	if f.cookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite = %v, want Strict", f.cookie.SameSite)
	}
	if f.cookie.Path != "/" {
		t.Errorf("Path = %q, want /", f.cookie.Path)
	}
}

func TestAttach_RedirectsAwayFromToken(t *testing.T) {
	t.Parallel()

	f := newFixture(t, &stubApp{})

	token, _ := f.srv.IssueToken()
	req := httptest.NewRequest(http.MethodGet, "/s/"+token, nil)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Errorf("Location = %q, want /: the token must not stay in the URL bar", loc)
	}
}

// Liveness stays open so a developer can curl it, and reveals nothing.
func TestAttach_HealthzIsOpen(t *testing.T) {
	t.Parallel()

	f := newFixture(t, &stubApp{})
	f.cookie = nil

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}
