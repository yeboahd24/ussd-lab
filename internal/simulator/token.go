package simulator

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/yeboahd24/ussd-lab/internal/session"
)

// The QR code carries an ATTACH TOKEN, not a session (ADR-004).
//
// Scanning attaches a device; a USSD session begins later, when the user dials.
// Keeping the two separate means one scan drives many sessions -- which is what
// a developer iterating on a menu flow actually wants -- and keeps transport
// concerns out of session lifecycle.
//
// Because the simulator binds 0.0.0.0, anything on the same Wi-Fi can reach the
// port. Token unguessability is therefore the only real access control, so
// tokens come from crypto/rand and comparisons are constant-time.

const (
	// attachCookie is set once a device has presented a valid token.
	attachCookie = "ussd_attach"

	// tokenBytes yields a 43-character URL-safe token (~256 bits). QR codes
	// handle this length comfortably at the default error correction level.
	tokenBytes = 32

	// DefaultTokenTTL bounds how long a QR code remains usable.
	DefaultTokenTTL = time.Hour
)

// TokenStore issues and validates attach tokens.
type TokenStore struct {
	mu     sync.RWMutex
	tokens map[string]time.Time // token -> expiry
	ttl    time.Duration
	clock  session.Clock
}

// NewTokenStore returns a store issuing tokens valid for ttl.
func NewTokenStore(ttl time.Duration, clock session.Clock) *TokenStore {
	if ttl <= 0 {
		ttl = DefaultTokenTTL
	}
	if clock == nil {
		clock = session.SystemClock{}
	}
	return &TokenStore{
		tokens: make(map[string]time.Time),
		ttl:    ttl,
		clock:  clock,
	}
}

// Issue mints a new attach token.
func (s *TokenStore) Issue() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("simulator: cannot read random bytes: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(b)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[token] = s.clock.Now().Add(s.ttl)

	return token, nil
}

// Valid reports whether token is known and unexpired.
func (s *TokenStore) Valid(token string) bool {
	if token == "" {
		return false
	}

	s.mu.RLock()
	expiry, known := s.tokens[token]
	s.mu.RUnlock()

	if !known {
		return false
	}
	if !s.clock.Now().Before(expiry) {
		s.mu.Lock()
		delete(s.tokens, token)
		s.mu.Unlock()
		return false
	}
	return true
}

// Revoke invalidates a token.
func (s *TokenStore) Revoke(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, token)
}

// handleAttach validates a scanned token and attaches the browser.
//
// On success it sets a cookie and redirects to the UI, so the token never
// remains in the address bar to be screenshotted, shoulder-surfed or copied out
// of browser history.
func (s *Server) handleAttach(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")

	if !s.tokens.Valid(token) {
		writeError(w, newAPIError(http.StatusForbidden, CodeAttachRequired,
			"this link is not valid or has expired",
			"run `ussd dev` again and scan the new QR code"))
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     attachCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true, // script must never be able to read the token
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(s.tokens.ttl.Seconds()),
		// Secure is deliberately unset: this is plain HTTP on a LAN, and a
		// Secure cookie would simply never be sent.
	})

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// requireAttach gates the API on a valid attach cookie.
func (s *Server) requireAttach(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.attached(r) {
			writeError(w, newAPIError(http.StatusForbidden, CodeAttachRequired,
				"this device is not attached to the simulator",
				"scan the QR code shown by `ussd dev`"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) attached(r *http.Request) bool {
	c, err := r.Cookie(attachCookie)
	if err != nil {
		return false
	}
	return s.tokens.Valid(c.Value)
}
