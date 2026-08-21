package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/yeboahd24/ussd-lab/internal/protocol"
)

// Engine errors, distinct so callers can respond differently to each
// (brief §20: errors must be explicit).
var (
	// ErrSessionNotActive: the session exists but has already terminated.
	ErrSessionNotActive = errors.New("session is not active")

	// ErrSessionTimedOut: the session expired before this input arrived.
	ErrSessionTimedOut = errors.New("session timed out")

	// ErrInvalidInput: the user input was rejected before dispatch.
	ErrInvalidInput = errors.New("invalid input")

	// ErrApplicationFailure wraps every error originating in the developer's
	// application.
	//
	// The engine is the only component that knows an error came from the
	// application rather than from USSD Lab itself. Transports must not have to
	// type-assert a concrete client to work that out -- otherwise a transport
	// paired with a different ApplicationClient would report the developer's
	// bug as a simulator bug (provider design §49).
	ErrApplicationFailure = errors.New("application failure")
)

// MaxInputLength bounds a single user entry. A real USSD screen accepts far
// less; this is a safety limit, not a fidelity limit.
const MaxInputLength = 182

// DefaultSessionTimeout matches the configuration default.
const DefaultSessionTimeout = 120 * time.Second

// Options configures an Engine. Dependencies are explicit rather than
// package-level, so tests can substitute each one independently.
type Options struct {
	Store   SessionStore
	App     protocol.ApplicationClient
	Events  EventSink
	Clock   Clock
	Timeout time.Duration
	Logger  *slog.Logger
}

// Engine owns session lifecycle. It is safe for concurrent use to the extent
// its Store is; sessions themselves are independent.
type Engine struct {
	store   SessionStore
	app     protocol.ApplicationClient
	events  EventSink
	clock   Clock
	timeout time.Duration
	log     *slog.Logger
}

// New validates and wires an Engine.
func New(opts Options) (*Engine, error) {
	if opts.Store == nil {
		return nil, fmt.Errorf("session: Store is required")
	}
	if opts.App == nil {
		return nil, fmt.Errorf("session: App is required")
	}

	if opts.Events == nil {
		opts.Events = NopSink{}
	}
	if opts.Clock == nil {
		opts.Clock = SystemClock{}
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultSessionTimeout
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}

	return &Engine{
		store:   opts.Store,
		app:     opts.App,
		events:  opts.Events,
		clock:   opts.Clock,
		timeout: opts.Timeout,
		log:     opts.Logger,
	}, nil
}

// StartParams describes a new session.
type StartParams struct {
	ProjectID   string
	ServiceCode string
	PhoneNumber string
	Network     string
}

// Result pairs the updated session with the screen to display.
type Result struct {
	Session  *Session
	Response protocol.USSDResponse
}

// Start creates a session and performs the initial application call, which
// carries an empty Text -- the USSD equivalent of dialling the short code.
func (e *Engine) Start(ctx context.Context, p StartParams) (*Result, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}

	now := e.clock.Now()
	s := &Session{
		ID:          NewSessionID(),
		ProjectID:   p.ProjectID,
		ServiceCode: p.ServiceCode,
		PhoneNumber: p.PhoneNumber,
		Network:     p.Network,
		Status:      StatusActive,
		Inputs:      nil,
		CreatedAt:   now,
		UpdatedAt:   now,
		ExpiresAt:   now.Add(e.timeout),
	}

	if err := e.store.Create(ctx, s); err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	// The payload carries everything needed to reconstruct a session summary
	// from the event log alone, so history needs no second write path.
	e.emit(ctx, s, EventSessionStarted, map[string]any{
		"service_code": s.ServiceCode,
		"network":      s.Network,
		"phone_number": s.PhoneNumber,
	})

	return e.dispatch(ctx, s)
}

// Input applies one user entry to an active session.
func (e *Engine) Input(ctx context.Context, sessionID, input string) (*Result, error) {
	s, err := e.store.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	// Expiry is evaluated lazily, on access. A background sweeper would need a
	// List method on every store implementation to achieve the same thing; for
	// a session that is never touched again, expiring it has no observable
	// effect anyway.
	if s.Status == StatusActive && s.IsExpired(e.clock.Now()) {
		if err := e.terminate(ctx, s, StatusTimeout, EventSessionTimeout, nil); err != nil {
			return nil, err
		}
		return nil, ErrSessionTimedOut
	}

	if s.Status.IsTerminal() {
		return nil, fmt.Errorf("%w: status is %s", ErrSessionNotActive, s.Status)
	}

	if err := validateInput(input); err != nil {
		// A rejected input is a user error, not a session error: the session
		// stays active so the user can try again, exactly as a real USSD menu
		// would allow.
		return nil, err
	}

	s.Inputs = append(s.Inputs, input)
	s.UpdatedAt = e.clock.Now()

	e.emit(ctx, s, EventInputReceived, map[string]any{"text": input})

	return e.dispatch(ctx, s)
}

// Cancel ends an active session at the user's request.
func (e *Engine) Cancel(ctx context.Context, sessionID string) error {
	s, err := e.store.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	if s.Status.IsTerminal() {
		return fmt.Errorf("%w: status is %s", ErrSessionNotActive, s.Status)
	}
	return e.terminate(ctx, s, StatusCancelled, EventSessionCancelled, nil)
}

// Get returns a session, applying lazy expiry so callers never observe an
// ACTIVE session that is in fact past its deadline.
func (e *Engine) Get(ctx context.Context, sessionID string) (*Session, error) {
	s, err := e.store.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if s.Status == StatusActive && s.IsExpired(e.clock.Now()) {
		if err := e.terminate(ctx, s, StatusTimeout, EventSessionTimeout, nil); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// dispatch calls the developer's application with the session's accumulated
// input and applies the outcome to the session.
func (e *Engine) dispatch(ctx context.Context, s *Session) (*Result, error) {
	req := &protocol.USSDRequest{
		RequestID:   NewRequestID(),
		SessionID:   s.ID,
		ServiceCode: s.ServiceCode,
		PhoneNumber: s.PhoneNumber,
		Network:     s.Network,
		Text:        s.Text(),
		Timestamp:   e.clock.Now(),
	}

	e.emit(ctx, s, EventApplicationRequest, map[string]any{
		"request_id": req.RequestID,
		"text":       req.Text,
	})

	resp, err := e.app.Send(ctx, req)
	if err != nil {
		// The application is unreachable or misbehaving. The session cannot
		// continue, so it terminates in ERROR rather than being left ACTIVE in
		// a state the user can never advance.
		//
		// The error is wrapped so callers can attribute it to the application
		// without knowing which ApplicationClient produced it. The original
		// error stays unwrappable, so a transport that DOES understand the
		// concrete client can still read its detailed code.
		appErr := fmt.Errorf("%w: %w", ErrApplicationFailure, err)

		if termErr := e.terminate(ctx, s, StatusError, EventApplicationError, map[string]any{
			"error": err.Error(),
		}); termErr != nil {
			return nil, errors.Join(appErr, termErr)
		}
		return nil, appErr
	}

	e.emit(ctx, s, EventApplicationResponse, map[string]any{
		"type": string(resp.Type),
		"text": resp.Text,
	})

	if resp.IsFinal() {
		if err := e.terminate(ctx, s, StatusCompleted, EventSessionCompleted, nil); err != nil {
			return nil, err
		}
		return &Result{Session: s.Clone(), Response: *resp}, nil
	}

	// A continuing session gets a fresh deadline: the timeout measures user
	// think-time between screens, not total session length.
	now := e.clock.Now()
	s.UpdatedAt = now
	s.ExpiresAt = now.Add(e.timeout)

	if err := e.store.Update(ctx, s); err != nil {
		return nil, fmt.Errorf("update session: %w", err)
	}

	e.logTransition(s)
	return &Result{Session: s.Clone(), Response: *resp}, nil
}

// terminate moves a session to a final status, persists it and emits an event.
func (e *Engine) terminate(
	ctx context.Context,
	s *Session,
	status Status,
	event EventType,
	payload map[string]any,
) error {
	s.Status = status
	s.UpdatedAt = e.clock.Now()

	if err := e.store.Update(ctx, s); err != nil {
		return fmt.Errorf("update session: %w", err)
	}

	e.emit(ctx, s, event, payload)
	e.logTransition(s)
	return nil
}

func (e *Engine) emit(ctx context.Context, s *Session, t EventType, payload map[string]any) {
	e.events.Emit(ctx, Event{
		EventID:   NewEventID(),
		SessionID: s.ID,
		Type:      t,
		Payload:   payload,
		Timestamp: e.clock.Now(),
	})
}

// logTransition records a status change. Note that no user input is logged
// here: input may contain a PIN, and unlike the event log -- which the
// developer explicitly opens to debug their own session -- log output is
// routinely captured and shipped elsewhere.
func (e *Engine) logTransition(s *Session) {
	e.log.Debug("session transition",
		slog.String("session_id", s.ID),
		slog.String("status", s.Status.String()),
		slog.String("phone_number", s.PhoneNumber),
		slog.Int("input_count", len(s.Inputs)),
	)
}

func (p StartParams) validate() error {
	switch {
	case p.ProjectID == "":
		return fmt.Errorf("%w: project_id is required", ErrInvalidInput)
	case p.ServiceCode == "":
		return fmt.Errorf("%w: service_code is required", ErrInvalidInput)
	case p.PhoneNumber == "":
		return fmt.Errorf("%w: phone_number is required", ErrInvalidInput)
	case p.Network == "":
		return fmt.Errorf("%w: network is required", ErrInvalidInput)
	}
	return nil
}

// validateInput screens a single user entry.
//
// Note what is NOT rejected: an input containing protocol.InputSeparator. Real
// USSD keypads permit "*", and the resulting ambiguity in the accumulated text
// is a property of the real protocol (ADR-002). Session.Inputs remains
// authoritative, so nothing is lost on our side.
func validateInput(input string) error {
	if input == "" {
		return fmt.Errorf("%w: input must not be empty", ErrInvalidInput)
	}
	if len(input) > MaxInputLength {
		return fmt.Errorf("%w: input exceeds %d characters", ErrInvalidInput, MaxInputLength)
	}
	if strings.ContainsFunc(input, func(r rune) bool {
		return unicode.IsControl(r)
	}) {
		return fmt.Errorf("%w: input contains control characters", ErrInvalidInput)
	}
	return nil
}
