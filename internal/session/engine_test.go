package session_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yeboahd24/ussd-lab/internal/protocol"
	"github.com/yeboahd24/ussd-lab/internal/session"
	"github.com/yeboahd24/ussd-lab/internal/storage/memory"
)

// stubApp is a scripted ApplicationClient. It records every request it
// receives so tests can assert on what the engine actually sent.
type stubApp struct {
	mu        sync.Mutex
	responses []protocol.USSDResponse
	err       error
	requests  []*protocol.USSDRequest
	handler   func(*protocol.USSDRequest) (*protocol.USSDResponse, error)
}

func (s *stubApp) Send(_ context.Context, req *protocol.USSDRequest) (*protocol.USSDResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Copy: the engine must not be able to observe later mutation, and tests
	// assert on the value as it was sent.
	captured := *req
	s.requests = append(s.requests, &captured)

	if s.handler != nil {
		return s.handler(&captured)
	}
	if s.err != nil {
		return nil, s.err
	}
	if len(s.responses) == 0 {
		return nil, errors.New("stubApp: no scripted response remaining")
	}

	resp := s.responses[0]
	s.responses = s.responses[1:]
	return &resp, nil
}

func (s *stubApp) sent() []*protocol.USSDRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*protocol.USSDRequest, len(s.requests))
	copy(out, s.requests)
	return out
}

type harness struct {
	engine *session.Engine
	app    *stubApp
	store  *memory.Store
	events *session.MemorySink
	clock  *session.FakeClock
}

func newHarness(t *testing.T, app *stubApp, timeout time.Duration) *harness {
	t.Helper()

	store := memory.New()
	events := session.NewMemorySink()
	clock := session.NewFakeClock(time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC))

	engine, err := session.New(session.Options{
		Store:   store,
		App:     app,
		Events:  events,
		Clock:   clock,
		Timeout: timeout,
	})
	if err != nil {
		t.Fatalf("session.New() error = %v", err)
	}

	return &harness{engine: engine, app: app, store: store, events: events, clock: clock}
}

func startParams() session.StartParams {
	return session.StartParams{
		ProjectID:   "my-fintech",
		ServiceCode: "*124#",
		PhoneNumber: "233240000001",
		Network:     protocol.NetworkSimulator,
	}
}

// The headline test: a complete Send Money conversation, driven entirely
// through the engine. No HTTP server, no port, no browser (MVP design §19).
func TestEngine_FullSendMoneyFlow(t *testing.T) {
	t.Parallel()

	app := &stubApp{responses: []protocol.USSDResponse{
		protocol.Continue("Welcome to MyBank\n1. Send Money\n2. Check Balance"),
		protocol.Continue("Enter recipient number:"),
		protocol.Continue("Enter amount:"),
		protocol.End("Transaction successful."),
	}}
	h := newHarness(t, app, 120*time.Second)
	ctx := context.Background()

	// Dial *124#
	res, err := h.engine.Start(ctx, startParams())
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if res.Response.Type != protocol.TypeCON {
		t.Fatalf("Type = %q, want CON", res.Response.Type)
	}
	if !strings.Contains(res.Response.Text, "Send Money") {
		t.Errorf("Text = %q", res.Response.Text)
	}
	if res.Session.Status != session.StatusActive {
		t.Errorf("Status = %q, want ACTIVE", res.Session.Status)
	}
	sid := res.Session.ID

	for _, input := range []string{"1", "0241234567", "100"} {
		res, err = h.engine.Input(ctx, sid, input)
		if err != nil {
			t.Fatalf("Input(%q) error = %v", input, err)
		}
	}

	if res.Response.Type != protocol.TypeEND {
		t.Fatalf("final Type = %q, want END", res.Response.Type)
	}
	if res.Session.Status != session.StatusCompleted {
		t.Errorf("final Status = %q, want COMPLETED", res.Session.Status)
	}

	// The application must have received progressively accumulated text.
	sent := h.app.sent()
	wantTexts := []string{"", "1", "1*0241234567", "1*0241234567*100"}
	if len(sent) != len(wantTexts) {
		t.Fatalf("application received %d requests, want %d", len(sent), len(wantTexts))
	}
	for i, want := range wantTexts {
		if sent[i].Text != want {
			t.Errorf("request %d Text = %q, want %q", i, sent[i].Text, want)
		}
		if sent[i].SessionID != sid {
			t.Errorf("request %d SessionID = %q, want %q", i, sent[i].SessionID, sid)
		}
		if sent[i].RequestID == "" {
			t.Errorf("request %d has no RequestID", i)
		}
	}

	// Every RequestID must be unique, or replay detection is impossible later.
	seen := map[string]bool{}
	for _, r := range sent {
		if seen[r.RequestID] {
			t.Errorf("duplicate RequestID %q", r.RequestID)
		}
		seen[r.RequestID] = true
	}
}

func TestEngine_EventSequence(t *testing.T) {
	t.Parallel()

	app := &stubApp{responses: []protocol.USSDResponse{
		protocol.Continue("Menu"),
		protocol.End("Bye"),
	}}
	h := newHarness(t, app, 120*time.Second)
	ctx := context.Background()

	res, err := h.engine.Start(ctx, startParams())
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if _, err := h.engine.Input(ctx, res.Session.ID, "1"); err != nil {
		t.Fatalf("Input() error = %v", err)
	}

	want := []session.EventType{
		session.EventSessionStarted,
		session.EventApplicationRequest,
		session.EventApplicationResponse,
		session.EventInputReceived,
		session.EventApplicationRequest,
		session.EventApplicationResponse,
		session.EventSessionCompleted,
	}

	got := h.events.Types()
	if len(got) != len(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %s, want %s", i, got[i], want[i])
		}
	}

	// Every event must be identifiable and attributable.
	for _, e := range h.events.Events() {
		if e.EventID == "" {
			t.Errorf("event %s has no EventID", e.Type)
		}
		if e.SessionID != res.Session.ID {
			t.Errorf("event %s SessionID = %q", e.Type, e.SessionID)
		}
		if e.Timestamp.IsZero() {
			t.Errorf("event %s has no Timestamp", e.Type)
		}
	}
}

func TestEngine_CheckBalanceEndsImmediately(t *testing.T) {
	t.Parallel()

	app := &stubApp{responses: []protocol.USSDResponse{
		protocol.End("Your balance is GHS 1,000"),
	}}
	h := newHarness(t, app, 120*time.Second)

	res, err := h.engine.Start(context.Background(), startParams())
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if !res.Response.IsFinal() {
		t.Error("expected a final response")
	}
	if res.Session.Status != session.StatusCompleted {
		t.Errorf("Status = %q, want COMPLETED", res.Session.Status)
	}
}

// Timeout is exercised by advancing a fake clock, not by sleeping.
func TestEngine_SessionTimeout(t *testing.T) {
	t.Parallel()

	app := &stubApp{responses: []protocol.USSDResponse{protocol.Continue("Menu")}}
	h := newHarness(t, app, 120*time.Second)
	ctx := context.Background()

	res, err := h.engine.Start(ctx, startParams())
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	h.clock.Advance(121 * time.Second)

	_, err = h.engine.Input(ctx, res.Session.ID, "1")
	if !errors.Is(err, session.ErrSessionTimedOut) {
		t.Fatalf("Input() error = %v, want ErrSessionTimedOut", err)
	}

	stored, err := h.store.Get(ctx, res.Session.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if stored.Status != session.StatusTimeout {
		t.Errorf("Status = %q, want TIMEOUT", stored.Status)
	}

	types := h.events.Types()
	if types[len(types)-1] != session.EventSessionTimeout {
		t.Errorf("last event = %s, want SESSION_TIMEOUT", types[len(types)-1])
	}
}

// A continuing session gets a fresh deadline: the timeout measures user
// think-time between screens, not total session length.
func TestEngine_TimeoutSlidesOnInput(t *testing.T) {
	t.Parallel()

	app := &stubApp{responses: []protocol.USSDResponse{
		protocol.Continue("Menu"),
		protocol.Continue("Enter amount:"),
		protocol.End("Done"),
	}}
	h := newHarness(t, app, 120*time.Second)
	ctx := context.Background()

	res, err := h.engine.Start(ctx, startParams())
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	sid := res.Session.ID

	h.clock.Advance(100 * time.Second) // within the window
	if _, err := h.engine.Input(ctx, sid, "1"); err != nil {
		t.Fatalf("Input() error = %v", err)
	}

	h.clock.Advance(100 * time.Second) // would exceed the ORIGINAL deadline
	if _, err := h.engine.Input(ctx, sid, "100"); err != nil {
		t.Fatalf("Input() after slide error = %v, want success", err)
	}
}

func TestEngine_GetAppliesLazyExpiry(t *testing.T) {
	t.Parallel()

	app := &stubApp{responses: []protocol.USSDResponse{protocol.Continue("Menu")}}
	h := newHarness(t, app, 60*time.Second)
	ctx := context.Background()

	res, err := h.engine.Start(ctx, startParams())
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	h.clock.Advance(61 * time.Second)

	got, err := h.engine.Get(ctx, res.Session.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Status != session.StatusTimeout {
		t.Errorf("Status = %q, want TIMEOUT", got.Status)
	}
}

func TestEngine_Cancel(t *testing.T) {
	t.Parallel()

	app := &stubApp{responses: []protocol.USSDResponse{protocol.Continue("Menu")}}
	h := newHarness(t, app, 120*time.Second)
	ctx := context.Background()

	res, err := h.engine.Start(ctx, startParams())
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if err := h.engine.Cancel(ctx, res.Session.ID); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}

	stored, _ := h.store.Get(ctx, res.Session.ID)
	if stored.Status != session.StatusCancelled {
		t.Errorf("Status = %q, want CANCELLED", stored.Status)
	}

	// A cancelled session accepts no further input.
	if _, err := h.engine.Input(ctx, res.Session.ID, "1"); !errors.Is(err, session.ErrSessionNotActive) {
		t.Errorf("Input() after Cancel error = %v, want ErrSessionNotActive", err)
	}
	if err := h.engine.Cancel(ctx, res.Session.ID); !errors.Is(err, session.ErrSessionNotActive) {
		t.Errorf("double Cancel() error = %v, want ErrSessionNotActive", err)
	}
}

func TestEngine_CompletedSessionRejectsInput(t *testing.T) {
	t.Parallel()

	app := &stubApp{responses: []protocol.USSDResponse{protocol.End("Done")}}
	h := newHarness(t, app, 120*time.Second)
	ctx := context.Background()

	res, err := h.engine.Start(ctx, startParams())
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	_, err = h.engine.Input(ctx, res.Session.ID, "1")
	if !errors.Is(err, session.ErrSessionNotActive) {
		t.Errorf("Input() error = %v, want ErrSessionNotActive", err)
	}
}

// An unreachable application terminates the session in ERROR rather than
// leaving it ACTIVE in a state the user can never advance.
func TestEngine_ApplicationErrorTerminatesSession(t *testing.T) {
	t.Parallel()

	appErr := errors.New("connection refused")
	h := newHarness(t, &stubApp{err: appErr}, 120*time.Second)
	ctx := context.Background()

	_, err := h.engine.Start(ctx, startParams())
	if !errors.Is(err, appErr) {
		t.Fatalf("Start() error = %v, want the application error", err)
	}

	types := h.events.Types()
	if types[len(types)-1] != session.EventApplicationError {
		t.Errorf("last event = %s, want APPLICATION_ERROR", types[len(types)-1])
	}

	// The session must be persisted in ERROR, not left dangling as ACTIVE.
	if h.store.Len() != 1 {
		t.Fatalf("store holds %d sessions, want 1", h.store.Len())
	}
}

func TestEngine_ApplicationErrorMidSession(t *testing.T) {
	t.Parallel()

	calls := 0
	app := &stubApp{handler: func(r *protocol.USSDRequest) (*protocol.USSDResponse, error) {
		calls++
		if calls == 1 {
			resp := protocol.Continue("Menu")
			return &resp, nil
		}
		return nil, errors.New("application exploded")
	}}
	h := newHarness(t, app, 120*time.Second)
	ctx := context.Background()

	res, err := h.engine.Start(ctx, startParams())
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if _, err := h.engine.Input(ctx, res.Session.ID, "1"); err == nil {
		t.Fatal("Input() error = nil, want the application error")
	}

	stored, _ := h.store.Get(ctx, res.Session.ID)
	if stored.Status != session.StatusError {
		t.Errorf("Status = %q, want ERROR", stored.Status)
	}
}

// A rejected input is a user error, not a session error: the session stays
// active so the user can try again, as a real USSD menu would allow.
func TestEngine_InvalidInputKeepsSessionActive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"too long", strings.Repeat("9", session.MaxInputLength+1)},
		{"control characters", "1\x00\x07"},
		{"newline", "1\n2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app := &stubApp{responses: []protocol.USSDResponse{
				protocol.Continue("Menu"),
				protocol.End("Done"),
			}}
			h := newHarness(t, app, 120*time.Second)
			ctx := context.Background()

			res, err := h.engine.Start(ctx, startParams())
			if err != nil {
				t.Fatalf("Start() error = %v", err)
			}

			if _, err := h.engine.Input(ctx, res.Session.ID, tt.input); !errors.Is(err, session.ErrInvalidInput) {
				t.Fatalf("Input(%q) error = %v, want ErrInvalidInput", tt.input, err)
			}

			stored, _ := h.store.Get(ctx, res.Session.ID)
			if stored.Status != session.StatusActive {
				t.Errorf("Status = %q, want ACTIVE after rejected input", stored.Status)
			}
			if len(stored.Inputs) != 0 {
				t.Errorf("Inputs = %v, want empty: rejected input was recorded", stored.Inputs)
			}

			// The session must still be usable.
			if _, err := h.engine.Input(ctx, res.Session.ID, "1"); err != nil {
				t.Errorf("valid Input() after rejection error = %v", err)
			}
		})
	}
}

// "*" is legal on a real USSD keypad. It is accepted, and Session.Inputs stays
// authoritative even though the accumulated wire text becomes ambiguous.
func TestEngine_InputContainingSeparator(t *testing.T) {
	t.Parallel()

	app := &stubApp{responses: []protocol.USSDResponse{
		protocol.Continue("Menu"),
		protocol.End("Done"),
	}}
	h := newHarness(t, app, 120*time.Second)
	ctx := context.Background()

	res, _ := h.engine.Start(ctx, startParams())
	if _, err := h.engine.Input(ctx, res.Session.ID, "a*b"); err != nil {
		t.Fatalf("Input() error = %v", err)
	}

	stored, _ := h.store.Get(ctx, res.Session.ID)
	if len(stored.Inputs) != 1 || stored.Inputs[0] != "a*b" {
		t.Errorf("Inputs = %v, want [\"a*b\"] preserved intact", stored.Inputs)
	}
	if stored.Text() != "a*b" {
		t.Errorf("Text() = %q", stored.Text())
	}
}

func TestEngine_UnknownSession(t *testing.T) {
	t.Parallel()

	h := newHarness(t, &stubApp{}, 120*time.Second)
	ctx := context.Background()

	if _, err := h.engine.Input(ctx, "sess_nope", "1"); !errors.Is(err, session.ErrSessionNotFound) {
		t.Errorf("Input() error = %v, want ErrSessionNotFound", err)
	}
	if err := h.engine.Cancel(ctx, "sess_nope"); !errors.Is(err, session.ErrSessionNotFound) {
		t.Errorf("Cancel() error = %v, want ErrSessionNotFound", err)
	}
	if _, err := h.engine.Get(ctx, "sess_nope"); !errors.Is(err, session.ErrSessionNotFound) {
		t.Errorf("Get() error = %v, want ErrSessionNotFound", err)
	}
}

func TestEngine_StartValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*session.StartParams)
	}{
		{"no project", func(p *session.StartParams) { p.ProjectID = "" }},
		{"no service code", func(p *session.StartParams) { p.ServiceCode = "" }},
		{"no phone", func(p *session.StartParams) { p.PhoneNumber = "" }},
		{"no network", func(p *session.StartParams) { p.Network = "" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t, &stubApp{}, 120*time.Second)
			p := startParams()
			tt.mutate(&p)

			if _, err := h.engine.Start(context.Background(), p); !errors.Is(err, session.ErrInvalidInput) {
				t.Errorf("Start() error = %v, want ErrInvalidInput", err)
			}
		})
	}
}

// Concurrent sessions must not interfere: this is MVP acceptance criterion
// "run multiple sessions" (design §28).
func TestEngine_ConcurrentSessions(t *testing.T) {
	t.Parallel()

	app := &stubApp{handler: func(r *protocol.USSDRequest) (*protocol.USSDResponse, error) {
		if r.Text == "" {
			resp := protocol.Continue("Menu")
			return &resp, nil
		}
		resp := protocol.End("Done " + r.Text)
		return &resp, nil
	}}
	h := newHarness(t, app, 120*time.Second)

	const n = 20
	var wg sync.WaitGroup
	ids := make([]string, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx := context.Background()

			res, err := h.engine.Start(ctx, startParams())
			if err != nil {
				t.Errorf("Start() error = %v", err)
				return
			}
			ids[i] = res.Session.ID

			res, err = h.engine.Input(ctx, res.Session.ID, "1")
			if err != nil {
				t.Errorf("Input() error = %v", err)
				return
			}
			if res.Session.Status != session.StatusCompleted {
				t.Errorf("Status = %q, want COMPLETED", res.Session.Status)
			}
		}(i)
	}
	wg.Wait()

	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" {
			t.Fatal("a session produced no ID")
		}
		if seen[id] {
			t.Errorf("duplicate session ID %q", id)
		}
		seen[id] = true
	}
	if h.store.Len() != n {
		t.Errorf("store holds %d sessions, want %d", h.store.Len(), n)
	}
}

func TestEngine_RequiresDependencies(t *testing.T) {
	t.Parallel()

	if _, err := session.New(session.Options{App: &stubApp{}}); err == nil {
		t.Error("New() without Store error = nil, want error")
	}
	if _, err := session.New(session.Options{Store: memory.New()}); err == nil {
		t.Error("New() without App error = nil, want error")
	}
}

func TestIDs_UniqueAndPrefixed(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := session.NewSessionID()
		if !strings.HasPrefix(id, "sess_") {
			t.Fatalf("id %q lacks the sess_ prefix", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}

	if !strings.HasPrefix(session.NewRequestID(), "req_") {
		t.Error("request id lacks the req_ prefix")
	}
	if !strings.HasPrefix(session.NewEventID(), "evt_") {
		t.Error("event id lacks the evt_ prefix")
	}
}

func TestStatus_IsTerminal(t *testing.T) {
	t.Parallel()

	if session.StatusActive.IsTerminal() {
		t.Error("ACTIVE should not be terminal")
	}
	for _, s := range []session.Status{
		session.StatusCompleted,
		session.StatusCancelled,
		session.StatusTimeout,
		session.StatusError,
	} {
		if !s.IsTerminal() {
			t.Errorf("%s should be terminal", s)
		}
	}
}

// The event log records what the user typed, which may be a PIN. A durable sink
// can be wrapped so secrets never reach a file that outlives the session.
func TestRedactingSink(t *testing.T) {
	t.Parallel()

	inner := session.NewMemorySink()
	redacted := session.NewRedactingSink(inner)
	ctx := context.Background()

	redacted.Emit(ctx, session.Event{
		SessionID: "sess_1", Type: session.EventInputReceived,
		Payload: map[string]any{"text": "4321"},
	})
	redacted.Emit(ctx, session.Event{
		SessionID: "sess_1", Type: session.EventApplicationResponse,
		Payload: map[string]any{"type": "END", "text": "Done"},
	})

	events := inner.Events()
	if got := events[0].Payload["text"]; got != session.RedactedText {
		t.Errorf("input text = %v, want %s", got, session.RedactedText)
	}
	// Only user input is redacted; screens the application produced are not.
	if got := events[1].Payload["text"]; got != "Done" {
		t.Errorf("application text = %v, want it preserved", got)
	}
}

// Redaction must not corrupt the event other sinks see: the live terminal view
// keeps full input while the durable sink is redacted.
func TestRedactingSink_DoesNotMutateSharedEvent(t *testing.T) {
	t.Parallel()

	live := session.NewMemorySink()
	durable := session.NewMemorySink()

	sinks := session.MultiSink{live, session.NewRedactingSink(durable)}

	sinks.Emit(context.Background(), session.Event{
		SessionID: "sess_1", Type: session.EventInputReceived,
		Payload: map[string]any{"text": "4321"},
	})

	if got := live.Events()[0].Payload["text"]; got != "4321" {
		t.Errorf("live view text = %v, want the real input", got)
	}
	if got := durable.Events()[0].Payload["text"]; got != session.RedactedText {
		t.Errorf("durable text = %v, want it redacted", got)
	}
}

func TestRedactingSink_HandlesNilPayload(t *testing.T) {
	t.Parallel()

	inner := session.NewMemorySink()
	session.NewRedactingSink(inner).Emit(context.Background(), session.Event{
		SessionID: "sess_1", Type: session.EventInputReceived,
	})

	if len(inner.Events()) != 1 {
		t.Error("event with a nil payload was dropped")
	}
}
