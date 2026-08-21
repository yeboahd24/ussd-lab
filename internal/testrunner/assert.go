package testrunner

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/yeboahd24/ussd-lab/internal/protocol"
	"github.com/yeboahd24/ussd-lab/internal/session"
)

// screen is what an assertion is evaluated against.
type screen struct {
	Response protocol.USSDResponse
	Status   session.Status
}

// check evaluates every condition set on a. It returns all failures rather than
// the first, so one run reports everything wrong with a screen.
func (a Assertion) check(s screen) []string {
	var failures []string

	text := s.Response.Text

	if a.Contains != "" && !strings.Contains(text, a.Contains) {
		failures = append(failures, fmt.Sprintf(
			"expected screen to contain %q", a.Contains))
	}

	if a.NotContains != "" && strings.Contains(text, a.NotContains) {
		failures = append(failures, fmt.Sprintf(
			"expected screen NOT to contain %q", a.NotContains))
	}

	if a.Equals != "" && text != a.Equals {
		failures = append(failures, fmt.Sprintf(
			"expected screen to equal %q", a.Equals))
	}

	if a.Matches != "" {
		// Already validated at load time, so a compile error here is
		// impossible; ignoring it keeps the happy path readable.
		re, err := regexp.Compile(a.Matches)
		if err == nil && !re.MatchString(text) {
			failures = append(failures, fmt.Sprintf(
				"expected screen to match /%s/", a.Matches))
		}
	}

	if a.Type != "" && string(s.Response.Type) != a.Type {
		failures = append(failures, fmt.Sprintf(
			"expected a %s response, got %s", a.Type, s.Response.Type))
	}

	if a.Status != "" && string(s.Status) != a.Status {
		failures = append(failures, fmt.Sprintf(
			"expected session status %s, got %s", a.Status, s.Status))
	}

	return failures
}

// checkAll evaluates a list of assertions against one screen.
func checkAll(assertions []Assertion, s screen) []string {
	var failures []string
	for _, a := range assertions {
		failures = append(failures, a.check(s)...)
	}
	return failures
}
