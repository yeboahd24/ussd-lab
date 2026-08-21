package simulator

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/yeboahd24/ussd-lab/internal/protocol"
	"github.com/yeboahd24/ussd-lab/internal/session"
)

// This file is the ADAPTER SEAM (ADR-005).
//
// It holds exactly the responsibilities a future ProviderAdapter will own:
// decoding a transport-specific request into normalized values, and encoding a
// normalized result back into a transport-specific response. Routing, limits
// and lifecycle live in handler.go and server.go.
//
// No ProviderAdapter interface is declared yet. An interface derived from one
// implementation encodes that implementation's accidents as requirements; the
// simulator, having no signature verification, no provider session IDs and a
// wire format it chose for itself, is the least representative sample
// available. When a real provider arrives, these functions lift into methods.

// MaxRequestBytes bounds an inbound request body. The simulator listens on the
// LAN, so the body is untrusted input from anything on the network.
const MaxRequestBytes = 4 << 10 // 4 KiB

// DefaultPhoneNumber is used when the simulator UI does not supply one. It
// matches the examples in the design documents.
const DefaultPhoneNumber = "233240000001"

// dialRequest is a request to start a session -- the equivalent of dialling.
type dialRequest struct {
	ServiceCode string `json:"service_code"`
	PhoneNumber string `json:"phone_number"`
}

// inputRequest is a request to advance an existing session.
type inputRequest struct {
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
}

// sessionRef identifies a session for cancel/lookup.
type sessionRef struct {
	SessionID string `json:"session_id"`
}

// screen is what the phone UI renders.
//
// Note what it does NOT contain: no callback URL, no project identifier, no
// application detail. The browser receives only what it needs to draw a screen
// and name the session it belongs to.
type screen struct {
	SessionID string `json:"session_id"`
	Type      string `json:"type"`
	Text      string `json:"text"`
	Status    string `json:"status"`
}

// contentType is the response encoding this transport uses. A provider adapter
// would return whatever its provider requires.
func contentType() string { return "application/json; charset=utf-8" }

// decodeJSON reads a size-limited JSON body.
//
// Unknown fields are rejected: a browser sending an unexpected field is either
// a bug or an attempt to influence something it should not control, and both
// are worth surfacing.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBytes)

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		switch {
		case err == io.EOF:
			return newAPIError(http.StatusBadRequest, CodeBadRequest,
				"request body is empty", "")
		case asMaxBytes(err, &maxErr):
			return newAPIError(http.StatusRequestEntityTooLarge, CodeBadRequest,
				fmt.Sprintf("request body exceeds %d bytes", MaxRequestBytes), "")
		default:
			return newAPIError(http.StatusBadRequest, CodeBadRequest,
				"request body is not valid JSON", "")
		}
	}

	// A second value in the stream means the client sent more than one object.
	if dec.More() {
		return newAPIError(http.StatusBadRequest, CodeBadRequest,
			"request body must contain a single JSON object", "")
	}
	return nil
}

// normalizeDial validates a dial request against project configuration and
// produces engine parameters.
//
// The service code is checked against the CONFIGURED code rather than accepted
// as given. This is both fidelity -- dialling an unregistered code fails on a
// real network -- and security: the browser must not be able to introduce
// arbitrary values into the session record (MVP design §22).
func (s *Server) normalizeDial(req dialRequest) (session.StartParams, error) {
	code := strings.TrimSpace(req.ServiceCode)
	if code == "" {
		return session.StartParams{}, newAPIError(http.StatusBadRequest,
			CodeBadRequest, "service_code is required", "")
	}
	if code != s.serviceCode {
		return session.StartParams{}, newAPIError(http.StatusNotFound,
			CodeUnknownServiceCode,
			fmt.Sprintf("%s is not registered", code),
			fmt.Sprintf("this project answers %s (set in ussd.yaml)", s.serviceCode))
	}

	phone := strings.TrimSpace(req.PhoneNumber)
	if phone == "" {
		phone = DefaultPhoneNumber
	}
	if err := validatePhoneNumber(phone); err != nil {
		return session.StartParams{}, err
	}

	return session.StartParams{
		ProjectID:   s.projectID,
		ServiceCode: code,
		PhoneNumber: phone,
		Network:     protocol.NetworkSimulator,
	}, nil
}

// validatePhoneNumber accepts digits only, in a plausible MSISDN length range.
func validatePhoneNumber(phone string) error {
	const minLen, maxLen = 6, 15

	if len(phone) < minLen || len(phone) > maxLen {
		return newAPIError(http.StatusBadRequest, CodeBadRequest,
			fmt.Sprintf("phone_number must be %d-%d digits", minLen, maxLen), "")
	}
	for _, r := range phone {
		if r < '0' || r > '9' {
			return newAPIError(http.StatusBadRequest, CodeBadRequest,
				"phone_number must contain digits only", "")
		}
	}
	return nil
}

// encodeScreen writes a result as the phone UI's view model.
func encodeScreen(w http.ResponseWriter, sess *session.Session, resp protocol.USSDResponse) {
	writeJSON(w, http.StatusOK, screen{
		SessionID: sess.ID,
		Type:      string(resp.Type),
		Text:      resp.Text,
		Status:    string(sess.Status),
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", contentType())
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
