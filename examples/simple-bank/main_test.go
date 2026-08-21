package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// route is a pure function from input history to screen, so the entire
// application is testable without a server. That is the practical payoff of
// the protocol's stateless design (ADR-002).
func TestRoute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		text     string
		wantType string
		contains string
	}{
		{"first dial", "", "CON", "1. Send Money"},
		{"check balance", "2", "END", "GHS 1,000"},
		{"exit", "3", "END", "Goodbye"},
		{"invalid top-level choice", "9", "END", "Invalid choice"},

		{"send money: recipient prompt", "1", "CON", "Enter recipient number:"},
		{"send money: amount prompt", "1*0241234567", "CON", "Enter amount:"},
		{"send money: confirmation", "1*0241234567*100", "CON", "Send GHS 100 to 0241234567?"},
		{"send money: confirmed", "1*0241234567*100*1", "END", "Transaction successful."},
		{"send money: cancelled", "1*0241234567*100*2", "END", "Transaction cancelled."},

		{"send money: bad recipient", "1*abc", "END", "does not look like a phone number"},
		{"send money: short recipient", "1*123", "END", "does not look like a phone number"},
		{"send money: bad amount", "1*0241234567*abc", "END", "not a valid amount"},
		{"send money: zero amount", "1*0241234567*0", "END", "not a valid amount"},
		{"send money: negative amount", "1*0241234567*-5", "END", "not a valid amount"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := route(inputs(tt.text))

			if !strings.HasPrefix(got, tt.wantType+" ") {
				t.Errorf("route(%q) = %q, want a %s response", tt.text, got, tt.wantType)
			}
			if !strings.Contains(got, tt.contains) {
				t.Errorf("route(%q) = %q, want to contain %q", tt.text, got, tt.contains)
			}
		})
	}
}

// Every screen must begin with a valid keyword, or USSD Lab rejects it as a
// malformed response.
func TestRoute_AlwaysWellFormed(t *testing.T) {
	t.Parallel()

	texts := []string{
		"", "1", "2", "3", "9", "*", "1*", "1*1*1*1*1*1",
		"1*0241234567", "1*0241234567*100", "1*0241234567*100*1",
		"1*0241234567*100*2", "1*0241234567*100*99",
		strings.Repeat("1*", 50),
	}

	for _, text := range texts {
		t.Run(text, func(t *testing.T) {
			got := route(inputs(text))

			if !strings.HasPrefix(got, "CON ") && !strings.HasPrefix(got, "END ") {
				t.Errorf("route(%q) = %q, which is neither CON nor END", text, got)
			}
			if len(strings.TrimSpace(got)) <= 4 {
				t.Errorf("route(%q) = %q has a keyword but no message", text, got)
			}
		})
	}
}

func TestHandler(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/ussd",
		strings.NewReader(`{"session_id":"sess_1","text":"2"}`))

	newHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); !strings.HasPrefix(body, "END ") {
		t.Errorf("body = %q, want an END response", body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
}

func TestHandler_RejectsBadJSON(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/ussd", strings.NewReader(`{not json`))

	newHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// Unknown fields must be ignored so the protocol can gain fields without
// breaking existing applications.
func TestHandler_IgnoresUnknownFields(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/ussd", strings.NewReader(
		`{"session_id":"sess_1","text":"2","metadata":{"provider":"future"},"brand_new":1}`))

	newHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "GHS 1,000") {
		t.Errorf("body = %q", rec.Body.String())
	}
}
