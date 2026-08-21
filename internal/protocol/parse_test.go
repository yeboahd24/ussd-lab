package protocol

import (
	"errors"
	"strings"
	"testing"
)

func TestParseResponse_Valid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		raw      string
		wantType ResponseType
		wantText string
	}{
		{"simple CON", "CON Enter amount:", TypeCON, "Enter amount:"},
		{"simple END", "END Transaction successful.", TypeEND, "Transaction successful."},
		{"trailing newline", "CON Enter amount:\n", TypeCON, "Enter amount:"},
		{"trailing CRLF", "END Done.\r\n", TypeEND, "Done."},
		{"leading whitespace", "  CON Enter amount:", TypeCON, "Enter amount:"},
		{"multiple spaces after keyword", "CON    Enter amount:", TypeCON, "Enter amount:"},
		{"newline after keyword", "CON\nWelcome", TypeCON, "Welcome"},
		{
			name:     "multi-line menu preserved",
			raw:      "CON Welcome to MyBank\n1. Send Money\n2. Check Balance",
			wantType: TypeCON,
			wantText: "Welcome to MyBank\n1. Send Money\n2. Check Balance",
		},
		{"text containing CON", "CON CONfirm payment", TypeCON, "CONfirm payment"},
		{"unicode text", "END Akwaaba éèê", TypeEND, "Akwaaba éèê"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseResponse([]byte(tt.raw))
			if err != nil {
				t.Fatalf("ParseResponse(%q) error = %v, want nil", tt.raw, err)
			}
			if got.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", got.Type, tt.wantType)
			}
			if got.Text != tt.wantText {
				t.Errorf("Text = %q, want %q", got.Text, tt.wantText)
			}
		})
	}
}

func TestParseResponse_Invalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		raw      string
		wantCode ParseErrorCode
	}{
		{"empty", "", CodeEmptyResponse},
		{"whitespace only", "   \n\t ", CodeEmptyResponse},
		{"keyword only CON", "CON", CodeEmptyText},
		{"keyword only END", "END", CodeEmptyText},
		{"keyword with trailing space only", "CON   ", CodeEmptyText},
		{"lowercase con", "con Enter amount:", CodeUnknownType},
		{"mixed case Con", "Con Enter amount:", CodeUnknownType},
		{"unknown keyword", "MAYBE Enter amount:", CodeUnknownType},
		{"missing keyword", "Enter amount:", CodeUnknownType},
		{"html error page", "<html><body>500</body></html>", CodeUnknownType},
		{"json response", `{"type":"CON","text":"hi"}`, CodeUnknownType},
		{"no space after keyword", "CONEnter amount:", CodeUnknownType},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := ParseResponse([]byte(tt.raw))
			if err == nil {
				t.Fatalf("ParseResponse(%q) error = nil, want %s", tt.raw, tt.wantCode)
			}

			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("error = %v (%T), want *ParseError", err, err)
			}
			if pe.Code != tt.wantCode {
				t.Errorf("Code = %s, want %s (err: %v)", pe.Code, tt.wantCode, err)
			}
		})
	}
}

func TestParseResponse_TooLarge(t *testing.T) {
	t.Parallel()

	raw := "CON " + strings.Repeat("x", MaxResponseBytes)

	_, err := ParseResponse([]byte(raw))

	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("error = %v, want *ParseError", err)
	}
	if pe.Code != CodeTooLarge {
		t.Errorf("Code = %s, want %s", pe.Code, CodeTooLarge)
	}
}

func TestParseResponse_InvalidUTF8(t *testing.T) {
	t.Parallel()

	raw := []byte{'C', 'O', 'N', ' ', 0xff, 0xfe, 0xfd}

	_, err := ParseResponse(raw)

	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("error = %v, want *ParseError", err)
	}
	if pe.Code != CodeInvalidEncoding {
		t.Errorf("Code = %s, want %s", pe.Code, CodeInvalidEncoding)
	}
}

// An application is untrusted output. A snippet echoed to the terminal must
// not be able to carry ANSI escapes or other control characters.
func TestParseError_SnippetIsSanitized(t *testing.T) {
	t.Parallel()

	raw := "\x1b]0;pwned\x07EVIL\x1b[31m response"

	_, err := ParseResponse([]byte(raw))
	if err == nil {
		t.Fatal("expected a parse error")
	}

	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("error = %v, want *ParseError", err)
	}

	if strings.ContainsRune(pe.Snippet, '\x1b') {
		t.Errorf("snippet retained an escape character: %q", pe.Snippet)
	}
	if strings.ContainsRune(pe.Snippet, '\x07') {
		t.Errorf("snippet retained a bell character: %q", pe.Snippet)
	}
	if strings.Contains(err.Error(), "\x1b") {
		t.Errorf("error message retained an escape character: %q", err.Error())
	}
}

func TestParseError_SnippetTruncated(t *testing.T) {
	t.Parallel()

	_, err := ParseResponse([]byte(strings.Repeat("z", 500)))

	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("error = %v, want *ParseError", err)
	}
	if len(pe.Snippet) > snippetLimit+len("...") {
		t.Errorf("snippet not truncated: len = %d", len(pe.Snippet))
	}
}

func TestResponse_Helpers(t *testing.T) {
	t.Parallel()

	con := Continue("Enter amount:")
	if con.IsFinal() {
		t.Error("Continue(...).IsFinal() = true, want false")
	}
	if got, want := con.Wire(), "CON Enter amount:"; got != want {
		t.Errorf("Wire() = %q, want %q", got, want)
	}

	end := End("Done.")
	if !end.IsFinal() {
		t.Error("End(...).IsFinal() = false, want true")
	}
	if got, want := end.Wire(), "END Done."; got != want {
		t.Errorf("Wire() = %q, want %q", got, want)
	}
}

// Wire output must round-trip back through the parser unchanged, so the
// simulator and a future provider adapter cannot disagree about encoding.
func TestResponse_WireRoundTrip(t *testing.T) {
	t.Parallel()

	for _, original := range []USSDResponse{
		Continue("Enter amount:"),
		End("Transaction successful."),
		Continue("Welcome\n1. Send Money\n2. Balance"),
	} {
		got, err := ParseResponse([]byte(original.Wire()))
		if err != nil {
			t.Fatalf("ParseResponse(%q) error = %v", original.Wire(), err)
		}
		if got != original {
			t.Errorf("round trip = %+v, want %+v", got, original)
		}
	}
}

func TestRequest_Inputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		text string
		want []string
	}{
		{"", nil},
		{"1", []string{"1"}},
		{"1*0241234567", []string{"1", "0241234567"}},
		{"1*0241234567*100*1", []string{"1", "0241234567", "100", "1"}},
	}

	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			t.Parallel()

			r := &USSDRequest{Text: tt.text}
			got := r.Inputs()

			if len(got) != len(tt.want) {
				t.Fatalf("Inputs() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("Inputs()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestRequest_IsFirstRequest(t *testing.T) {
	t.Parallel()

	if !(&USSDRequest{Text: ""}).IsFirstRequest() {
		t.Error("empty Text should be the first request")
	}
	if (&USSDRequest{Text: "1"}).IsFirstRequest() {
		t.Error("non-empty Text should not be the first request")
	}
}

func TestRequest_Validate(t *testing.T) {
	t.Parallel()

	valid := func() *USSDRequest {
		return &USSDRequest{
			RequestID:   "req_1",
			SessionID:   "sess_1",
			ServiceCode: "*124#",
			PhoneNumber: "233240000001",
			Network:     NetworkSimulator,
		}
	}

	if err := valid().Validate(); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}

	tests := []struct {
		name    string
		mutate  func(*USSDRequest)
		wantSub string
	}{
		{"no request id", func(r *USSDRequest) { r.RequestID = "" }, "request_id"},
		{"no session id", func(r *USSDRequest) { r.SessionID = "" }, "session_id"},
		{"no service code", func(r *USSDRequest) { r.ServiceCode = "" }, "service_code"},
		{"no phone", func(r *USSDRequest) { r.PhoneNumber = "" }, "phone_number"},
		{"no network", func(r *USSDRequest) { r.Network = "" }, "network"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := valid()
			tt.mutate(r)

			err := r.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want error mentioning %q", tt.wantSub)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("Validate() = %v, want mention of %q", err, tt.wantSub)
			}
		})
	}
}
