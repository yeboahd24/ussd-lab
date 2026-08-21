package simulator_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yeboahd24/ussd-lab/internal/protocol"
)

func (f *fixture) get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, path, nil)
	if f.cookie != nil {
		req.AddCookie(f.cookie)
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

func TestUI_ServesIndex(t *testing.T) {
	t.Parallel()

	f := newFixture(t, &stubApp{})

	rec := f.get(t, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "USSD Lab") {
		t.Error("body does not look like the simulator UI")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}

	// net/http canonicalises /index.html to /; assert that rather than
	// pretending both serve content directly.
	rec = f.get(t, "/index.html")
	if rec.Code != http.StatusMovedPermanently {
		t.Errorf("/index.html status = %d, want 301", rec.Code)
	}
}

func TestUI_ServesAssets(t *testing.T) {
	t.Parallel()

	f := newFixture(t, &stubApp{})

	tests := []struct{ path, wantType, wantContains string }{
		{"/app.js", "javascript", "/api/dial"},
		{"/style.css", "text/css", "--accent"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := f.get(t, tt.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, tt.wantType) {
				t.Errorf("Content-Type = %q, want %q", ct, tt.wantType)
			}
			if !strings.Contains(rec.Body.String(), tt.wantContains) {
				t.Errorf("body missing %q", tt.wantContains)
			}
		})
	}
}

// Assets must never be cacheable: a phone holding a stale app.js that
// disagrees with the server is a confusing failure to diagnose.
func TestUI_AssetsAreNotCached(t *testing.T) {
	t.Parallel()

	f := newFixture(t, &stubApp{})

	if got := f.get(t, "/app.js").Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

// The asset server is rooted at an embedded FS, so no URL can reach the
// developer's filesystem (MVP design §22).
func TestUI_NoFilesystemEscape(t *testing.T) {
	t.Parallel()

	f := newFixture(t, &stubApp{})

	paths := []string{
		"/../go.mod",
		"/../../etc/passwd",
		"/%2e%2e/go.mod",
		"/..%2fgo.mod",
		"/./../../ussd.yaml",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			rec := f.get(t, path)

			body := rec.Body.String()
			if strings.Contains(body, "module github.com") {
				t.Fatalf("%s leaked go.mod", path)
			}
			if strings.Contains(body, "root:x:") {
				t.Fatalf("%s leaked /etc/passwd", path)
			}
		})
	}
}

// Mounting the UI must not shadow the API: "GET /" is a catch-all.
func TestUI_DoesNotShadowAPI(t *testing.T) {
	t.Parallel()

	f := newFixture(t, &stubApp{responses: []protocol.USSDResponse{
		protocol.Continue("Menu"),
	}})

	rec := f.post(t, "/api/dial", `{"service_code":"*124#"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := decodeScreen(t, rec); got["type"] != "CON" {
		t.Errorf("type = %v, want CON", got["type"])
	}

	// /healthz must still be JSON, not the index page.
	rec = f.get(t, "/healthz")
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("/healthz Content-Type = %q, want JSON", ct)
	}
}

// The phone may be in airplane mode with only Wi-Fi (MVP design §9), so the UI
// must not reference anything off the LAN.
func TestUI_NoExternalReferences(t *testing.T) {
	t.Parallel()

	f := newFixture(t, &stubApp{})

	forbidden := []string{
		"https://", "http://cdn", "//cdn.", "fonts.googleapis", "unpkg.com",
		"jsdelivr", "cdnjs",
	}

	for _, path := range []string{"/index.html", "/app.js", "/style.css"} {
		t.Run(path, func(t *testing.T) {
			body := f.get(t, path).Body.String()
			for _, needle := range forbidden {
				if strings.Contains(body, needle) {
					t.Errorf("%s references %q; the UI must work offline", path, needle)
				}
			}
		})
	}
}

// Application output is untrusted. The UI must render it as text, which means
// no innerHTML anywhere in the script.
func TestUI_DoesNotUseInnerHTML(t *testing.T) {
	t.Parallel()

	f := newFixture(t, &stubApp{})
	body := f.get(t, "/app.js").Body.String()

	for _, sink := range []string{"innerHTML", "outerHTML", "document.write", "eval("} {
		if strings.Contains(body, sink) {
			t.Errorf("app.js uses %s; application text must be rendered via textContent", sink)
		}
	}
}
