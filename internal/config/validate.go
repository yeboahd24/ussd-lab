package config

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

// MaxSessionTimeout bounds ussd.session_timeout. Real USSD sessions are short;
// an unbounded value here would defeat session expiry entirely.
const MaxSessionTimeout = 3600

// projectNamePattern constrains the project identifier.
//
// The project name reaches log lines and, later, storage keys. Constraining it
// to a conservative identifier set avoids path traversal and log injection
// rather than relying on every downstream consumer to escape it.
var projectNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// hostnamePattern is a conservative hostname check for simulator.host.
var hostnamePattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)

// serviceCodePattern matches USSD short codes such as *124# or *123*1#.
var serviceCodePattern = regexp.MustCompile(`^\*[0-9]+(\*[0-9]+)*#$`)

// ValidationError is a single field-level configuration problem.
type ValidationError struct {
	Field   string
	Message string
}

func (e ValidationError) Error() string {
	if e.Field == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

// ValidationErrors aggregates every problem found in one pass, so the developer
// can fix all of them at once instead of one per run.
type ValidationErrors []ValidationError

func (e ValidationErrors) Error() string {
	if len(e) == 1 {
		return "invalid configuration: " + e[0].Error()
	}
	parts := make([]string, 0, len(e))
	for _, ve := range e {
		parts = append(parts, "  - "+ve.Error())
	}
	return fmt.Sprintf("invalid configuration (%d problems):\n%s",
		len(e), strings.Join(parts, "\n"))
}

// Validate reports every problem with the configuration.
func (c *Config) Validate() error {
	var errs ValidationErrors

	add := func(field, msg string) {
		errs = append(errs, ValidationError{Field: field, Message: msg})
	}

	// project
	switch {
	case strings.TrimSpace(c.Project) == "":
		add("project", "is required")
	case !projectNamePattern.MatchString(c.Project):
		add("project", "must be 1-64 characters of letters, digits, '.', '_' or '-', starting with a letter or digit")
	}

	// application.callback
	if err := validateCallback(c.Application.Callback); err != nil {
		add("application.callback", err.Error())
	}

	// ussd.service_code
	switch {
	case strings.TrimSpace(c.USSD.ServiceCode) == "":
		add("ussd.service_code", "is required")
	case !serviceCodePattern.MatchString(c.USSD.ServiceCode):
		add("ussd.service_code", `must look like a USSD short code, for example "*124#"`)
	}

	// ussd.session_timeout
	switch {
	case c.USSD.SessionTimeout < 0:
		add("ussd.session_timeout", "must be positive")
	case c.USSD.SessionTimeout > MaxSessionTimeout:
		add("ussd.session_timeout", fmt.Sprintf("must not exceed %d seconds", MaxSessionTimeout))
	}

	// simulator.port
	if c.Simulator.Port < 1 || c.Simulator.Port > 65535 {
		add("simulator.port", "must be between 1 and 65535")
	}

	// simulator.host
	if h := strings.TrimSpace(c.Simulator.Host); h != "" {
		if err := validateHost(h); err != nil {
			add("simulator.host", err.Error())
		}
	}

	if len(errs) > 0 {
		return errs
	}
	return nil
}

// validateHost checks an advertised address. It is a bare host or IP, never a
// URL: accepting a URL here would invite confusion with the callback setting.
func validateHost(h string) error {
	if strings.Contains(h, "://") || strings.Contains(h, "/") {
		return fmt.Errorf("must be a bare host or IP address, not a URL")
	}
	if strings.ContainsAny(h, " \t") {
		return fmt.Errorf("must not contain whitespace")
	}
	if net.ParseIP(h) == nil && !hostnamePattern.MatchString(h) {
		return fmt.Errorf("is not a valid IP address or hostname")
	}
	return nil
}

// validateCallback enforces the security boundary from MVP design §23: the
// simulator forwards to exactly one explicitly configured HTTP endpoint and
// must never be coercible into a general-purpose proxy.
func validateCallback(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("is required, for example http://localhost:8000/ussd")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("is not a valid URL: %v", err)
	}

	if !u.IsAbs() {
		return fmt.Errorf("must be an absolute URL including the scheme, for example http://localhost:8000/ussd")
	}

	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return fmt.Errorf("scheme %q is not supported; use http or https", u.Scheme)
	}

	if u.Host == "" {
		return fmt.Errorf("must include a host, for example http://localhost:8000/ussd")
	}

	// Credentials in a URL end up in logs and process listings.
	if u.User != nil {
		return fmt.Errorf("must not embed credentials; configure authentication in the application instead")
	}

	// A fragment is never transmitted in an HTTP request, so its presence
	// signals a misunderstanding rather than a working configuration.
	if u.Fragment != "" {
		return fmt.Errorf("must not contain a '#' fragment")
	}

	return nil
}
