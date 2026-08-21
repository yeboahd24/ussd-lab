// Package simulator is the local HTTP transport for USSD Lab.
//
// It is a TRANSPORT implementation and nothing more: it decodes requests,
// hands normalized values to the session engine, and renders what comes back.
// It contains no session logic and no business logic (MVP design §30).
//
// Conceptually this is the first of what will eventually be several transports
// -- the simulator, then real provider adapters. The parse/normalize/format
// functions that a provider adapter will own live in adapter.go; see ADR-005
// for why no ProviderAdapter interface is declared yet.
package simulator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/yeboahd24/ussd-lab/internal/session"
)

// Server timeouts. USSD is synchronous and short-lived; a request that has not
// completed in these windows is stuck, and holding the connection open only
// consumes resources.
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 15 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 60 * time.Second
	shutdownGrace     = 5 * time.Second
)

// Options configures a Server.
type Options struct {
	// Engine owns session lifecycle. Required.
	Engine *session.Engine

	// ProjectID and ServiceCode come from validated configuration. They are
	// held here, never accepted from a request (ADR-004).
	ProjectID   string
	ServiceCode string

	// BindAddr is the listen address. Defaults to 0.0.0.0:7345 so a phone on
	// the same Wi-Fi can reach it (MVP design §8). Use "127.0.0.1:0" in tests
	// for an ephemeral port.
	BindAddr string

	// TokenTTL bounds how long a QR code stays usable. Zero uses
	// DefaultTokenTTL.
	TokenTTL time.Duration

	// Clock is injected so token expiry is testable without sleeping.
	Clock session.Clock

	Logger *slog.Logger
}

// sessionView is the JSON shape of session state.
type sessionView struct {
	SessionID   string    `json:"session_id"`
	ServiceCode string    `json:"service_code"`
	PhoneNumber string    `json:"phone_number"`
	Network     string    `json:"network"`
	Status      string    `json:"status"`
	InputCount  int       `json:"input_count"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// Server is the simulator's HTTP transport.
type Server struct {
	engine      *session.Engine
	projectID   string
	serviceCode string
	tokens      *TokenStore
	log         *slog.Logger

	listener net.Listener
	http     *http.Server
}

// DefaultBindAddr binds every interface so a phone on the LAN can connect.
const DefaultBindAddr = "0.0.0.0:7345"

// New builds a Server and binds its listener.
//
// Binding happens here rather than in Serve so that the caller can read the
// actual address before starting to serve -- essential when the port is 0, and
// what lets the CLI print a URL it knows is correct.
func New(opts Options) (*Server, error) {
	if opts.Engine == nil {
		return nil, fmt.Errorf("simulator: Engine is required")
	}
	if opts.ProjectID == "" {
		return nil, fmt.Errorf("simulator: ProjectID is required")
	}
	if opts.ServiceCode == "" {
		return nil, fmt.Errorf("simulator: ServiceCode is required")
	}
	if opts.BindAddr == "" {
		opts.BindAddr = DefaultBindAddr
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}

	ln, err := net.Listen("tcp", opts.BindAddr)
	if err != nil {
		return nil, fmt.Errorf("simulator: listen on %s: %w", opts.BindAddr, err)
	}

	s := &Server{
		engine:      opts.Engine,
		projectID:   opts.ProjectID,
		serviceCode: opts.ServiceCode,
		tokens:      NewTokenStore(opts.TokenTTL, opts.Clock),
		log:         opts.Logger,
		listener:    ln,
	}

	s.http = &http.Server{
		Handler:           s.routes(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		ErrorLog:          slog.NewLogLogger(opts.Logger.Handler(), slog.LevelDebug),
	}

	return s, nil
}

// Addr reports the bound address, with the real port resolved.
func (s *Server) Addr() net.Addr { return s.listener.Addr() }

// Port reports the bound TCP port.
func (s *Server) Port() int {
	if tcp, ok := s.listener.Addr().(*net.TCPAddr); ok {
		return tcp.Port
	}
	return 0
}

// Serve blocks until the context is cancelled or the server fails.
//
// Cancelling ctx triggers a graceful shutdown, so in-flight USSD requests get a
// chance to finish rather than being cut off mid-session.
func (s *Server) Serve(ctx context.Context) error {
	errCh := make(chan error, 1)

	go func() {
		s.log.Info("simulator listening",
			slog.String("addr", s.listener.Addr().String()),
			slog.String("project", s.projectID),
			slog.String("service_code", s.serviceCode))

		if err := s.http.Serve(s.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return s.Shutdown()
	}
}

// Shutdown stops the server gracefully.
func (s *Server) Shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()

	if err := s.http.Shutdown(ctx); err != nil {
		return fmt.Errorf("simulator: shutdown: %w", err)
	}
	return nil
}

// Handler exposes the routes for testing without binding a port.
func (s *Server) Handler() http.Handler { return s.routes() }

// IssueToken mints an attach token for the QR code.
func (s *Server) IssueToken() (string, error) { return s.tokens.Issue() }
