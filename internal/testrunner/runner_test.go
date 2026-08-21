package testrunner_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yeboahd24/ussd-lab/internal/protocol"
	"github.com/yeboahd24/ussd-lab/internal/session"
	"github.com/yeboahd24/ussd-lab/internal/storage/memory"
	"github.com/yeboahd24/ussd-lab/internal/testrunner"
)

// bankApp is a scripted stand-in for a developer's application.
type bankApp struct{ err error }

func (a *bankApp) Send(_ context.Context, req *protocol.USSDRequest) (*protocol.USSDResponse, error) {
	if a.err != nil {
		return nil, a.err
	}

	var in []string
	if req.Text != "" {
		in = strings.Split(req.Text, "*")
	}

	var resp protocol.USSDResponse
	switch {
	case len(in) == 0:
		resp = protocol.Continue("Welcome to MyBank\n1. Send Money\n2. Check Balance")
	case in[0] == "2":
		resp = protocol.End("Your balance is GHS 1,000")
	case in[0] == "1":
		switch len(in) {
		case 1:
			resp = protocol.Continue("Enter recipient number:")
		case 2:
			resp = protocol.Continue("Enter amount:")
		default:
			resp = protocol.End("Transaction successful.")
		}
	default:
		resp = protocol.End("Invalid choice.")
	}
	return &resp, nil
}

func newRunner(t *testing.T, app protocol.ApplicationClient) *testrunner.Runner {
	t.Helper()

	engine, err := session.New(session.Options{
		Store: memory.New(), App: app, Timeout: 120 * time.Second,
	})
	if err != nil {
		t.Fatalf("session.New() error = %v", err)
	}

	r, err := testrunner.New(testrunner.Options{
		Engine: engine, ProjectID: "test", ServiceCode: "*124#",
	})
	if err != nil {
		t.Fatalf("testrunner.New() error = %v", err)
	}
	return r
}

func mustParse(t *testing.T, yaml string) *testrunner.Spec {
	t.Helper()

	spec, err := testrunner.Parse(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	return spec
}

func TestRun_Passing(t *testing.T) {
	t.Parallel()

	spec := mustParse(t, `
name: Send Money
steps:
  - input: "1"
    expect:
      - contains: "Enter recipient number:"
  - input: "0241234567"
    expect:
      - contains: "Enter amount:"
  - input: "100"
assertions:
  - type: END
    contains: "Transaction successful."
    status: COMPLETED
`)

	res := newRunner(t, &bankApp{}).Run(context.Background(), spec)

	if !res.Passed {
		t.Fatalf("test failed: %+v", res.Failures)
	}
	if res.Err != nil {
		t.Errorf("Err = %v", res.Err)
	}
	if len(res.Transcript) != 4 {
		t.Errorf("transcript has %d exchanges, want 4", len(res.Transcript))
	}
	if res.SessionID == "" {
		t.Error("no session id recorded")
	}
}

func TestRun_AssertionKinds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		assertion  string
		wantPassed bool
	}{
		{"contains", `contains: "GHS 1,000"`, true},
		{"contains miss", `contains: "GHS 9,999"`, false},
		{"not_contains", `not_contains: "overdrawn"`, true},
		{"not_contains hit", `not_contains: "balance"`, false},
		{"equals", `equals: "Your balance is GHS 1,000"`, true},
		{"equals miss", `equals: "something else"`, false},
		{"matches", `matches: "GHS [0-9,]+"`, true},
		{"matches miss", `matches: "^USD"`, false},
		{"type END", `type: END`, true},
		{"type CON", `type: CON`, false},
		{"status COMPLETED", `status: COMPLETED`, true},
		{"status ACTIVE", `status: ACTIVE`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			spec := mustParse(t, `
name: Balance
steps:
  - input: "2"
assertions:
  - `+tt.assertion+"\n")

			res := newRunner(t, &bankApp{}).Run(context.Background(), spec)

			if res.Passed != tt.wantPassed {
				t.Errorf("Passed = %v, want %v (failures: %+v)",
					res.Passed, tt.wantPassed, res.Failures)
			}
		})
	}
}

// Every unmet condition should be reported, not just the first: one run should
// tell the developer everything that is wrong.
func TestRun_ReportsAllFailures(t *testing.T) {
	t.Parallel()

	spec := mustParse(t, `
name: Multiple problems
steps:
  - input: "2"
assertions:
  - type: CON
    contains: "nonexistent"
    status: ACTIVE
`)

	res := newRunner(t, &bankApp{}).Run(context.Background(), spec)

	if res.Passed {
		t.Fatal("expected failure")
	}
	if len(res.Failures) != 3 {
		t.Errorf("got %d failures, want 3: %+v", len(res.Failures), res.Failures)
	}
}

// A session ending sooner than the spec expects is a test failure with a clear
// cause, not an infrastructure error.
func TestRun_SessionEndsEarly(t *testing.T) {
	t.Parallel()

	spec := mustParse(t, `
name: Too many steps
steps:
  - input: "2"
  - input: "1"
assertions:
  - contains: "anything"
`)

	res := newRunner(t, &bankApp{}).Run(context.Background(), spec)

	if res.Passed {
		t.Fatal("expected failure")
	}
	if res.Err != nil {
		t.Errorf("Err = %v; an early end is a failure, not an error", res.Err)
	}

	var found bool
	for _, f := range res.Failures {
		if strings.Contains(f.Message, "already ended") {
			found = true
			if f.Where() != "step 2" {
				t.Errorf("failure located at %q, want step 2", f.Where())
			}
		}
	}
	if !found {
		t.Errorf("no 'already ended' failure: %+v", res.Failures)
	}
}

// An unreachable application is an error, not an assertion failure -- the
// distinction tells the developer whether to look at their code or their
// environment.
func TestRun_ApplicationUnreachable(t *testing.T) {
	t.Parallel()

	spec := mustParse(t, `
name: Balance
steps:
  - input: "2"
assertions:
  - contains: "GHS"
`)

	res := newRunner(t, &bankApp{err: errors.New("connection refused")}).
		Run(context.Background(), spec)

	if res.Passed {
		t.Fatal("expected failure")
	}
	if res.Err == nil {
		t.Error("Err = nil; an unreachable application must be reported as an error")
	}
}

func TestRun_StepLevelExpectations(t *testing.T) {
	t.Parallel()

	spec := mustParse(t, `
name: Wrong mid-flow expectation
steps:
  - input: "1"
    expect:
      - contains: "Enter your PIN"
`)

	res := newRunner(t, &bankApp{}).Run(context.Background(), spec)

	if res.Passed {
		t.Fatal("expected failure")
	}
	if res.Failures[0].Where() != "step 1" {
		t.Errorf("failure located at %q, want step 1", res.Failures[0].Where())
	}
}

// A test that leaves its session ACTIVE would show up later as an abandoned
// session, so the runner cancels it.
func TestRun_CleansUpActiveSession(t *testing.T) {
	t.Parallel()

	store := memory.New()
	engine, err := session.New(session.Options{
		Store: store, App: &bankApp{}, Timeout: 120 * time.Second,
	})
	if err != nil {
		t.Fatalf("session.New() error = %v", err)
	}
	r, err := testrunner.New(testrunner.Options{
		Engine: engine, ProjectID: "test", ServiceCode: "*124#",
	})
	if err != nil {
		t.Fatalf("testrunner.New() error = %v", err)
	}

	// This spec stops mid-conversation, leaving the session active.
	spec := mustParse(t, "name: Partial\nsteps:\n  - input: \"1\"\n")
	res := r.Run(context.Background(), spec)

	sess, err := store.Get(context.Background(), res.SessionID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if sess.Status == session.StatusActive {
		t.Error("the runner left an ACTIVE session behind")
	}
}

func TestRunAll(t *testing.T) {
	t.Parallel()

	specs := []*testrunner.Spec{
		mustParse(t, "name: A\nsteps:\n  - input: \"2\"\nassertions:\n  - contains: \"GHS\"\n"),
		mustParse(t, "name: B\nsteps:\n  - input: \"2\"\nassertions:\n  - contains: \"nope\"\n"),
	}

	suite := newRunner(t, &bankApp{}).RunAll(context.Background(), specs)

	if suite.Passed() {
		t.Error("Passed() = true with a failing test")
	}

	passed, failed := suite.Counts()
	if passed != 1 || failed != 1 {
		t.Errorf("counts = %d passed, %d failed; want 1 and 1", passed, failed)
	}
	if suite.Results[0].Spec.Name != "A" {
		t.Error("results are out of order")
	}
}

func TestNew_Validation(t *testing.T) {
	t.Parallel()

	if _, err := testrunner.New(testrunner.Options{}); err == nil {
		t.Error("New() without Engine error = nil, want error")
	}
}
