package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yeboahd24/ussd-lab/internal/protocol"
	"github.com/yeboahd24/ussd-lab/internal/session"
	"github.com/yeboahd24/ussd-lab/internal/testrunner"
)

func spec(name string) *testrunner.Spec { return &testrunner.Spec{Name: name} }

func passing(name string) testrunner.Result {
	return testrunner.Result{
		Spec: spec(name), Passed: true, Duration: 3 * time.Millisecond,
		Transcript: []testrunner.Exchange{
			{Response: protocol.End("Done"), Status: session.StatusCompleted},
		},
	}
}

func TestPrintSuite_AllPassing(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	printSuite(&out, style{}, testrunner.Suite{
		Results:  []testrunner.Result{passing("Send Money"), passing("Check Balance")},
		Duration: 8 * time.Millisecond,
	}, false)

	got := out.String()
	if !strings.Contains(got, "✓ Send Money") {
		t.Errorf("missing pass marker:\n%s", got)
	}
	if !strings.Contains(got, "2 passed") {
		t.Errorf("missing summary:\n%s", got)
	}
	if strings.Contains(got, "failed") {
		t.Errorf("reported failures where there were none:\n%s", got)
	}
	// A passing test should not dump its transcript unless asked.
	if strings.Contains(got, "transcript:") {
		t.Errorf("transcript printed for a passing test without --verbose:\n%s", got)
	}
}

func TestPrintSuite_Verbose(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	printSuite(&out, style{}, testrunner.Suite{
		Results: []testrunner.Result{passing("Send Money")},
	}, true)

	if !strings.Contains(out.String(), "transcript:") {
		t.Errorf("--verbose did not print the transcript:\n%s", out.String())
	}
}

// A failing test must show what the application actually said; knowing an
// assertion failed is not enough to act on.
func TestPrintSuite_FailureShowsTranscript(t *testing.T) {
	t.Parallel()

	res := testrunner.Result{
		Spec:   spec("Broken"),
		Passed: false,
		Failures: []testrunner.Failure{
			{Step: -1, Message: `expected screen to contain "pending"`},
			{Step: 2, Message: "expected a CON response, got END"},
		},
		Transcript: []testrunner.Exchange{
			{Response: protocol.Continue("Welcome"), Status: session.StatusActive},
			{Input: "1", Response: protocol.End("Done"), Status: session.StatusCompleted},
		},
	}

	var out bytes.Buffer
	printSuite(&out, style{}, testrunner.Suite{Results: []testrunner.Result{res}}, false)

	got := out.String()
	for _, want := range []string{
		"✗ Broken", "final:", "step 2:", "transcript:",
		"USER → 1", "APP  → END", "0 passed, 1 failed",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\n%s", want, got)
		}
	}
}

// An unreachable application is an error, not an assertion failure, and the
// output must say so.
func TestPrintSuite_ErrorIsLabelled(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	printSuite(&out, style{}, testrunner.Suite{Results: []testrunner.Result{{
		Spec: spec("Balance"), Passed: false,
		Err: errors.New("connection refused"),
	}}}, false)

	got := out.String()
	if !strings.Contains(got, "error:") {
		t.Errorf("an unreachable application was not labelled as an error:\n%s", got)
	}
	if !strings.Contains(got, "connection refused") {
		t.Errorf("cause not shown:\n%s", got)
	}
}

func TestPrintSuite_NoColorEscapes(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	printSuite(&out, style{enabled: false}, testrunner.Suite{
		Results: []testrunner.Result{passing("A")},
	}, false)

	if strings.Contains(out.String(), "\x1b[") {
		t.Error("colour escapes emitted with colour disabled")
	}
}

func TestFilterSpecs(t *testing.T) {
	t.Parallel()

	specs := []*testrunner.Spec{spec("Send Money"), spec("Check Balance"), spec("Exit")}

	tests := []struct {
		filter string
		want   int
	}{
		{"money", 1}, // case-insensitive
		{"Check", 1},
		{"e", 3}, // matches all three
		{"nothing", 0},
	}

	for _, tt := range tests {
		t.Run(tt.filter, func(t *testing.T) {
			if got := len(filterSpecs(specs, tt.filter)); got != tt.want {
				t.Errorf("filterSpecs(%q) = %d specs, want %d", tt.filter, got, tt.want)
			}
		})
	}
}

func TestRoundDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   time.Duration
		want string
	}{
		{1234 * time.Nanosecond, "1µs"},
		{12345 * time.Microsecond, "12ms"},
		{1500 * time.Millisecond, "1.5s"},
	}

	for _, tt := range tests {
		if got := roundDuration(tt.in).String(); got != tt.want {
			t.Errorf("roundDuration(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}

	// The property that matters: a test that ran must never display as "0s",
	// which reads as a test that did not run at all.
	for _, d := range []time.Duration{
		1 * time.Nanosecond, 500 * time.Nanosecond,
		1 * time.Microsecond, 999 * time.Microsecond,
	} {
		if got := roundDuration(d).String(); got == "0s" {
			t.Errorf("roundDuration(%v) rendered as %q", d, got)
		}
	}
}
