package testrunner_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeboahd24/ussd-lab/internal/testrunner"
)

func TestParse_Valid(t *testing.T) {
	t.Parallel()

	spec, err := testrunner.Parse(strings.NewReader(`
name: Send Money
dial: "*124#"
phone: "233240000009"
steps:
  - input: "1"
    expect:
      - contains: "recipient"
  - input: "100"
assertions:
  - type: END
    contains: "successful"
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if spec.Name != "Send Money" {
		t.Errorf("Name = %q", spec.Name)
	}
	if spec.Dial != "*124#" {
		t.Errorf("Dial = %q", spec.Dial)
	}
	if spec.Phone != "233240000009" {
		t.Errorf("Phone = %q", spec.Phone)
	}
	if len(spec.Steps) != 2 {
		t.Fatalf("got %d steps, want 2", len(spec.Steps))
	}
	if len(spec.Steps[0].Expect) != 1 {
		t.Errorf("step 1 has %d expectations, want 1", len(spec.Steps[0].Expect))
	}
}

func TestParse_Invalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		yaml    string
		wantSub string
	}{
		{"empty", "", "empty"},
		{"no name", "steps:\n  - input: \"1\"\n", "name is required"},
		{"nothing to do", "name: Empty\n", "no steps and no assertions"},
		{
			name:    "assertion checks nothing",
			yaml:    "name: X\nassertions:\n  - {}\n",
			wantSub: "checks nothing",
		},
		{
			name:    "bad regex",
			yaml:    "name: X\nassertions:\n  - matches: \"[unclosed\"\n",
			wantSub: "invalid regular expression",
		},
		{
			name:    "bad response type",
			yaml:    "name: X\nassertions:\n  - type: MAYBE\n",
			wantSub: "not CON or END",
		},
		{
			// A silently ignored key is a test that passes for the wrong reason.
			name:    "unknown key",
			yaml:    "name: X\nassertion:\n  - contains: \"a\"\n",
			wantSub: "field assertion not found",
		},
		{
			name:    "typo in assertion key",
			yaml:    "name: X\nassertions:\n  - contain: \"a\"\n",
			wantSub: "field contain not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := testrunner.Parse(strings.NewReader(tt.yaml))
			if err == nil {
				t.Fatalf("Parse() error = nil, want an error mentioning %q", tt.wantSub)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("error = %v, want it to mention %q", err, tt.wantSub)
			}
		})
	}
}

func TestLoadDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	write("b-second.yaml", "name: Second\nassertions:\n  - type: CON\n")
	write("a-first.yml", "name: First\nassertions:\n  - type: CON\n")
	write("notes.txt", "ignored")

	specs, err := testrunner.LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir() error = %v", err)
	}
	if len(specs) != 2 {
		t.Fatalf("got %d specs, want 2", len(specs))
	}

	// Sorted by filename, so a suite runs in a predictable order.
	if specs[0].Name != "First" || specs[1].Name != "Second" {
		t.Errorf("order = %s, %s; want First, Second", specs[0].Name, specs[1].Name)
	}
	if specs[0].Path == "" {
		t.Error("Path not recorded; error messages need it")
	}
}

func TestLoadDir_Missing(t *testing.T) {
	t.Parallel()

	_, err := testrunner.LoadDir(filepath.Join(t.TempDir(), "nope"))

	var noTests *testrunner.ErrNoTests
	if !asNoTests(err, &noTests) {
		t.Fatalf("error = %v, want *ErrNoTests", err)
	}
}

func TestLoadDir_EmptyDir(t *testing.T) {
	t.Parallel()

	_, err := testrunner.LoadDir(t.TempDir())

	var noTests *testrunner.ErrNoTests
	if !asNoTests(err, &noTests) {
		t.Fatalf("error = %v, want *ErrNoTests", err)
	}
}

// A malformed file must name itself, or the developer cannot find it.
func TestLoadDir_ReportsOffendingFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "broken.yaml"),
		[]byte("name: X\nassertions:\n  - type: MAYBE\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := testrunner.LoadDir(dir)
	if err == nil {
		t.Fatal("error = nil, want a failure")
	}
	if !strings.Contains(err.Error(), "broken.yaml") {
		t.Errorf("error = %v, want it to name broken.yaml", err)
	}
}

func asNoTests(err error, target **testrunner.ErrNoTests) bool {
	if e, ok := err.(*testrunner.ErrNoTests); ok {
		*target = e
		return true
	}
	return false
}

// The example project's own tests must be valid: they are the reference for
// the format.
func TestExampleTestsAreValid(t *testing.T) {
	t.Parallel()

	specs, err := testrunner.LoadDir("../../examples/simple-bank/tests")
	if err != nil {
		t.Fatalf("example tests do not load: %v", err)
	}
	if len(specs) < 4 {
		t.Errorf("got %d example tests, want at least 4", len(specs))
	}
}
