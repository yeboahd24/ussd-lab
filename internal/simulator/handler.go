package simulator

import (
	"log/slog"
	"net/http"

	"github.com/yeboahd24/ussd-lab/internal/session"
)

// routes builds the HTTP surface.
//
// Every endpoint is a fixed path. There is deliberately no route that accepts a
// destination URL, a project identifier or a callback from the caller: the
// simulator forwards only to the endpoint named in validated configuration, so
// it cannot be turned into a proxy into the developer's machine
// (MVP design §23).
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	// The API is gated on a valid attach cookie, obtained by scanning the QR
	// code. The simulator binds every interface, so anything on the same Wi-Fi
	// can reach the port; token unguessability is the access control (ADR-004).
	api := http.NewServeMux()
	api.HandleFunc("POST /api/dial", s.handleDial)
	api.HandleFunc("POST /api/input", s.handleInput)
	api.HandleFunc("POST /api/cancel", s.handleCancel)
	api.HandleFunc("GET /api/session/{id}", s.handleGetSession)
	api.HandleFunc("GET /api/info", s.handleInfo)
	mux.Handle("/api/", s.requireAttach(api))

	// Attaching a device.
	mux.HandleFunc("GET /s/{token}", s.handleAttach)

	// Liveness only. It reveals nothing about the project, so it needs no
	// attach cookie and is safe for a developer to curl.
	mux.HandleFunc("GET /healthz", s.handleHealth)

	// The phone UI. Mounted last because "GET /" is the catch-all; Go's
	// ServeMux prefers the more specific /api patterns above regardless of
	// registration order, but keeping it last matches how the routes read.
	if err := mountUI(mux); err != nil {
		// Assets are embedded at build time. A failure here means the binary
		// itself is malformed, which is not a per-request condition.
		panic(err)
	}

	return s.recoverPanic(mux)
}

// handleDial starts a session: the equivalent of entering *124# and pressing
// call. The application receives a request with empty Text.
func (s *Server) handleDial(w http.ResponseWriter, r *http.Request) {
	var req dialRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, err)
		return
	}

	params, err := s.normalizeDial(req)
	if err != nil {
		writeError(w, err)
		return
	}

	res, err := s.engine.Start(r.Context(), params)
	if err != nil {
		s.log.Debug("dial failed",
			slog.String("service_code", params.ServiceCode),
			slog.String("phone_number", params.PhoneNumber),
			slog.String("error", err.Error()))
		writeError(w, err)
		return
	}

	encodeScreen(w, res.Session, res.Response)
}

// handleInput advances an active session by one user entry.
func (s *Server) handleInput(w http.ResponseWriter, r *http.Request) {
	var req inputRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, err)
		return
	}

	if req.SessionID == "" {
		writeError(w, newAPIError(http.StatusBadRequest, CodeBadRequest,
			"session_id is required", ""))
		return
	}

	res, err := s.engine.Input(r.Context(), req.SessionID, req.Text)
	if err != nil {
		// Note: req.Text is not logged. It may be a PIN.
		s.log.Debug("input failed",
			slog.String("session_id", req.SessionID),
			slog.String("error", err.Error()))
		writeError(w, err)
		return
	}

	encodeScreen(w, res.Session, res.Response)
}

// handleCancel ends an active session at the user's request.
func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	var req sessionRef
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, err)
		return
	}

	if req.SessionID == "" {
		writeError(w, newAPIError(http.StatusBadRequest, CodeBadRequest,
			"session_id is required", ""))
		return
	}

	if err := s.engine.Cancel(r.Context(), req.SessionID); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, screen{
		SessionID: req.SessionID,
		Status:    string(session.StatusCancelled),
	})
}

// handleGetSession reports session state, applying lazy expiry.
func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, newAPIError(http.StatusBadRequest, CodeBadRequest,
			"session id is required", ""))
		return
	}

	sess, err := s.engine.Get(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, sessionView{
		SessionID:   sess.ID,
		ServiceCode: sess.ServiceCode,
		PhoneNumber: sess.PhoneNumber,
		Network:     sess.Network,
		Status:      string(sess.Status),
		InputCount:  len(sess.Inputs),
		CreatedAt:   sess.CreatedAt,
		UpdatedAt:   sess.UpdatedAt,
		ExpiresAt:   sess.ExpiresAt,
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleInfo tells an attached UI which service code to prefill. It sits behind
// the attach gate because the project name and short code are project detail,
// not liveness.
func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"project":      s.projectID,
		"service_code": s.serviceCode,
	})
}

// recoverPanic keeps a panic in one handler from killing the process.
//
// The CLI and the HTTP server share a process (ADR-001), so an unrecovered
// panic would take down the developer's whole session, not just one request.
func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic in handler",
					slog.String("path", r.URL.Path),
					slog.Any("panic", rec))
				writeError(w, newAPIError(http.StatusInternalServerError,
					CodeInternal, "the simulator encountered an unexpected error", ""))
			}
		}()
		next.ServeHTTP(w, r)
	})
}
