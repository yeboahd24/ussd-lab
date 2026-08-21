// Package testrunner executes declarative USSD test definitions.
//
// The runner drives the SAME session engine as the interactive simulator, and
// depends on internal/session rather than internal/simulator: no HTTP server,
// no port, no browser. That is a requirement, not an optimisation -- if
// automated tests took a different path through the system than a phone does,
// a green suite would stop meaning anything (MVP design §19).
package testrunner

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Spec is one declarative test, as written in tests/*.yaml:
//
//	name: Send Money
//	dial: "*124#"
//	steps:
//	  - input: "1"
//	  - input: "0241234567"
//	    expect:
//	      - contains: "Enter amount"
//	assertions:
//	  - type: END
//	    contains: "Transaction successful"
type Spec struct {
	// Name identifies the test in output. Required.
	Name string `yaml:"name"`

	// Dial is the service code to dial. Defaults to the project's configured
	// code, so most tests need not repeat it.
	Dial string `yaml:"dial"`

	// Phone overrides the simulated subscriber number.
	Phone string `yaml:"phone"`

	Steps []Step `yaml:"steps"`

	// Assertions apply to the final screen.
	Assertions []Assertion `yaml:"assertions"`

	// Path records where the spec was loaded from, for error messages.
	Path string `yaml:"-"`
}

// Step is one user input, optionally with assertions on the screen it produces.
type Step struct {
	Input  string      `yaml:"input"`
	Expect []Assertion `yaml:"expect"`
}

// Assertion is a set of conditions on one screen. Every field that is set must
// hold, so a single assertion can check type and content together.
type Assertion struct {
	Contains    string `yaml:"contains"`
	NotContains string `yaml:"not_contains"`
	Equals      string `yaml:"equals"`
	Matches     string `yaml:"matches"`

	// Type is CON or END.
	Type string `yaml:"type"`

	// Status is the session status: ACTIVE, COMPLETED, CANCELLED, TIMEOUT,
	// ERROR.
	Status string `yaml:"status"`
}

// isEmpty reports whether an assertion checks nothing, which is almost always a
// mistake in the YAML rather than an intention.
func (a Assertion) isEmpty() bool {
	return a.Contains == "" && a.NotContains == "" && a.Equals == "" &&
		a.Matches == "" && a.Type == "" && a.Status == ""
}

// Validate reports problems with a spec before any of it is executed, so a typo
// fails immediately rather than halfway through a suite.
func (s *Spec) Validate() error {
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if len(s.Steps) == 0 && len(s.Assertions) == 0 {
		return fmt.Errorf("test has no steps and no assertions")
	}

	for i, step := range s.Steps {
		for j, a := range step.Expect {
			if err := validateAssertion(a); err != nil {
				return fmt.Errorf("steps[%d].expect[%d]: %w", i, j, err)
			}
		}
	}
	for i, a := range s.Assertions {
		if err := validateAssertion(a); err != nil {
			return fmt.Errorf("assertions[%d]: %w", i, err)
		}
	}
	return nil
}

func validateAssertion(a Assertion) error {
	if a.isEmpty() {
		return fmt.Errorf("assertion checks nothing")
	}
	if a.Matches != "" {
		if _, err := regexp.Compile(a.Matches); err != nil {
			return fmt.Errorf("matches: invalid regular expression: %w", err)
		}
	}
	if a.Type != "" && a.Type != "CON" && a.Type != "END" {
		return fmt.Errorf("type: %q is not CON or END", a.Type)
	}
	return nil
}

// Parse reads and validates one spec.
func Parse(r io.Reader) (*Spec, error) {
	var spec Spec

	dec := yaml.NewDecoder(r)
	// Strict: an unknown key is a typo, and a silently ignored assertion is a
	// test that passes for the wrong reason.
	dec.KnownFields(true)

	if err := dec.Decode(&spec); err != nil {
		if err == io.EOF {
			return nil, fmt.Errorf("test file is empty")
		}
		return nil, fmt.Errorf("parse yaml: %w", err)
	}

	if err := spec.Validate(); err != nil {
		return nil, err
	}
	return &spec, nil
}

// Load reads one spec from a file.
func Load(path string) (*Spec, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	spec, err := Parse(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	spec.Path = path
	return spec, nil
}

// ErrNoTests is returned when a directory contains no test files.
type ErrNoTests struct{ Dir string }

func (e *ErrNoTests) Error() string {
	return fmt.Sprintf("no test files found in %s", e.Dir)
}

// LoadDir reads every *.yaml and *.yml file in dir, sorted by filename so the
// suite runs in a predictable order.
func LoadDir(dir string) ([]*Spec, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &ErrNoTests{Dir: dir}
		}
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}

	var paths []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".yaml", ".yml":
			paths = append(paths, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(paths)

	if len(paths) == 0 {
		return nil, &ErrNoTests{Dir: dir}
	}

	specs := make([]*Spec, 0, len(paths))
	for _, p := range paths {
		spec, err := Load(p)
		if err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

// LoadFS is the fs.FS form of LoadDir, used in tests.
func LoadFS(fsys fs.FS, dir string) ([]*Spec, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}

	var specs []*Spec
	for _, e := range entries {
		if e.IsDir() || (filepath.Ext(e.Name()) != ".yaml" && filepath.Ext(e.Name()) != ".yml") {
			continue
		}
		f, err := fsys.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		spec, err := Parse(f)
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		spec.Path = e.Name()
		specs = append(specs, spec)
	}
	return specs, nil
}
