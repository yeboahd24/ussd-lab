package testrunner

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/yeboahd24/ussd-lab/internal/protocol"
	"github.com/yeboahd24/ussd-lab/internal/session"
)

// DefaultPhoneNumber is used when a spec does not name one.
const DefaultPhoneNumber = "233240000001"

// Options configures a Runner.
type Options struct {
	// Engine is the same engine type the interactive simulator uses.
	Engine *session.Engine

	ProjectID   string
	ServiceCode string

	// Phone is the default subscriber number for specs that omit one.
	Phone string
}

// Runner executes specs against a session engine.
type Runner struct {
	engine      *session.Engine
	projectID   string
	serviceCode string
	phone       string
}

// New validates and wires a Runner.
func New(opts Options) (*Runner, error) {
	if opts.Engine == nil {
		return nil, fmt.Errorf("testrunner: Engine is required")
	}
	if opts.ProjectID == "" {
		return nil, fmt.Errorf("testrunner: ProjectID is required")
	}
	if opts.ServiceCode == "" {
		return nil, fmt.Errorf("testrunner: ServiceCode is required")
	}
	if opts.Phone == "" {
		opts.Phone = DefaultPhoneNumber
	}

	return &Runner{
		engine:      opts.Engine,
		projectID:   opts.ProjectID,
		serviceCode: opts.ServiceCode,
		phone:       opts.Phone,
	}, nil
}

// Exchange is one turn of a conversation, retained for failure reporting.
//
// A failing assertion is far easier to act on when the whole transcript is
// visible, so the runner records every turn whether or not the test passes.
type Exchange struct {
	// Input is empty for the initial dial.
	Input    string
	Response protocol.USSDResponse
	Status   session.Status
}

// Failure is one unmet expectation.
type Failure struct {
	// Step is the 1-based step index, or 0 for the initial dial, or -1 for a
	// final assertion.
	Step    int
	Message string
}

// Where describes the point in the conversation a failure occurred.
func (f Failure) Where() string {
	switch {
	case f.Step < 0:
		return "final"
	case f.Step == 0:
		return "dial"
	default:
		return fmt.Sprintf("step %d", f.Step)
	}
}

// Result is the outcome of one spec.
type Result struct {
	Spec       *Spec
	Passed     bool
	Failures   []Failure
	Transcript []Exchange
	SessionID  string
	Duration   time.Duration

	// Err is set when the test could not be completed at all -- the
	// application was unreachable, for instance -- as opposed to an assertion
	// simply not holding. The distinction matters: one is the developer's bug,
	// the other may be their environment.
	Err error
}

// Run executes one spec.
func (r *Runner) Run(ctx context.Context, spec *Spec) Result {
	start := time.Now()
	res := Result{Spec: spec, Passed: true}

	dial := spec.Dial
	if dial == "" {
		dial = r.serviceCode
	}
	phone := spec.Phone
	if phone == "" {
		phone = r.phone
	}

	startRes, err := r.engine.Start(ctx, session.StartParams{
		ProjectID:   r.projectID,
		ServiceCode: dial,
		PhoneNumber: phone,
		Network:     protocol.NetworkSimulator,
	})
	if err != nil {
		res.Passed = false
		res.Err = fmt.Errorf("dialling %s: %w", dial, err)
		res.Duration = time.Since(start)
		return res
	}

	res.SessionID = startRes.Session.ID
	current := screen{Response: startRes.Response, Status: startRes.Session.Status}
	res.Transcript = append(res.Transcript, Exchange{
		Response: current.Response, Status: current.Status,
	})

	for _, msg := range checkAll(specDialAssertions(spec), current) {
		res.fail(0, msg)
	}

	for i, step := range spec.Steps {
		stepNum := i + 1

		inputRes, err := r.engine.Input(ctx, res.SessionID, step.Input)
		if err != nil {
			res.Passed = false

			// A session that ended earlier than the spec expects is a genuine
			// test failure with a clear cause, not an infrastructure error.
			if errors.Is(err, session.ErrSessionNotActive) {
				res.fail(stepNum, fmt.Sprintf(
					"session had already ended (status %s), so input %q could not be sent",
					current.Status, step.Input))
				break
			}
			if errors.Is(err, session.ErrInvalidInput) {
				res.fail(stepNum, fmt.Sprintf("input %q was rejected: %v", step.Input, err))
				break
			}

			res.Err = fmt.Errorf("step %d (input %q): %w", stepNum, step.Input, err)
			break
		}

		current = screen{Response: inputRes.Response, Status: inputRes.Session.Status}
		res.Transcript = append(res.Transcript, Exchange{
			Input: step.Input, Response: current.Response, Status: current.Status,
		})

		for _, msg := range checkAll(step.Expect, current) {
			res.fail(stepNum, msg)
		}
	}

	// Final assertions only run if the conversation completed; asserting on a
	// screen the test never reached produces misleading noise.
	if res.Err == nil {
		for _, msg := range checkAll(spec.Assertions, current) {
			res.fail(-1, msg)
		}
	}

	// Leave no ACTIVE session behind: suites share one store, and a stale
	// session would be visible to `ussd logs` as though a user abandoned it.
	if current.Status == session.StatusActive {
		_ = r.engine.Cancel(ctx, res.SessionID)
	}

	res.Duration = time.Since(start)
	return res
}

// specDialAssertions returns assertions attached to the initial screen. The
// format has no dedicated field for these yet; the hook exists so adding one
// later does not change Run.
func specDialAssertions(_ *Spec) []Assertion { return nil }

func (r *Result) fail(step int, message string) {
	r.Passed = false
	r.Failures = append(r.Failures, Failure{Step: step, Message: message})
}

// Suite is the outcome of running many specs.
type Suite struct {
	Results  []Result
	Duration time.Duration
}

// Passed reports whether every test passed.
func (s Suite) Passed() bool {
	for _, r := range s.Results {
		if !r.Passed {
			return false
		}
	}
	return true
}

// Counts returns how many tests passed and failed.
func (s Suite) Counts() (passed, failed int) {
	for _, r := range s.Results {
		if r.Passed {
			passed++
		} else {
			failed++
		}
	}
	return passed, failed
}

// RunAll executes specs in order.
//
// Tests run sequentially rather than in parallel. A developer's application may
// hold global state, and a suite whose results depend on interleaving would be
// worse than useless.
func (r *Runner) RunAll(ctx context.Context, specs []*Spec) Suite {
	start := time.Now()
	suite := Suite{Results: make([]Result, 0, len(specs))}

	for _, spec := range specs {
		suite.Results = append(suite.Results, r.Run(ctx, spec))
	}

	suite.Duration = time.Since(start)
	return suite
}
