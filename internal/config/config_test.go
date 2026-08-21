package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validYAML = `
project: my-fintech
application:
  callback: http://localhost:8000/ussd
ussd:
  service_code: "*124#"
  session_timeout: 120
`

func TestParse_Valid(t *testing.T) {
	t.Parallel()

	cfg, err := Parse(strings.NewReader(validYAML))
	if err != nil {
		t.Fatalf("Parse() error = %v, want nil", err)
	}
	if cfg.Project != "my-fintech" {
		t.Errorf("Project = %q, want %q", cfg.Project, "my-fintech")
	}
	if cfg.Application.Callback != "http://localhost:8000/ussd" {
		t.Errorf("Callback = %q", cfg.Application.Callback)
	}
	if cfg.USSD.ServiceCode != "*124#" {
		t.Errorf("ServiceCode = %q", cfg.USSD.ServiceCode)
	}
	if cfg.USSD.SessionTimeout != 120 {
		t.Errorf("SessionTimeout = %d, want 120", cfg.USSD.SessionTimeout)
	}
}

func TestParse_DefaultsSessionTimeout(t *testing.T) {
	t.Parallel()

	const y = `
project: demo
application:
  callback: http://localhost:8000/ussd
ussd:
  service_code: "*124#"
`
	cfg, err := Parse(strings.NewReader(y))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cfg.USSD.SessionTimeout != DefaultSessionTimeout {
		t.Errorf("SessionTimeout = %d, want default %d",
			cfg.USSD.SessionTimeout, DefaultSessionTimeout)
	}
}

// Strict decoding turns a silent misconfiguration into a loud failure.
func TestParse_RejectsUnknownField(t *testing.T) {
	t.Parallel()

	const y = `
project: demo
application:
  callbak: http://localhost:8000/ussd
ussd:
  service_code: "*124#"
`
	if _, err := Parse(strings.NewReader(y)); err == nil {
		t.Fatal("Parse() error = nil, want error for misspelled field")
	}
}

func TestParse_Invalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		yaml      string
		wantField string
	}{
		{
			name:      "missing project",
			yaml:      "application:\n  callback: http://localhost:8000/ussd\nussd:\n  service_code: \"*124#\"\n",
			wantField: "project",
		},
		{
			name:      "project with path traversal",
			yaml:      "project: \"../../etc\"\napplication:\n  callback: http://x:1/u\nussd:\n  service_code: \"*124#\"\n",
			wantField: "project",
		},
		{
			name:      "missing callback",
			yaml:      "project: demo\nussd:\n  service_code: \"*124#\"\n",
			wantField: "application.callback",
		},
		{
			name:      "file scheme callback",
			yaml:      "project: demo\napplication:\n  callback: file:///etc/passwd\nussd:\n  service_code: \"*124#\"\n",
			wantField: "application.callback",
		},
		{
			name:      "javascript scheme callback",
			yaml:      "project: demo\napplication:\n  callback: \"javascript:alert(1)\"\nussd:\n  service_code: \"*124#\"\n",
			wantField: "application.callback",
		},
		{
			name:      "relative callback",
			yaml:      "project: demo\napplication:\n  callback: /ussd\nussd:\n  service_code: \"*124#\"\n",
			wantField: "application.callback",
		},
		{
			name:      "callback with credentials",
			yaml:      "project: demo\napplication:\n  callback: http://user:pass@localhost:8000/ussd\nussd:\n  service_code: \"*124#\"\n",
			wantField: "application.callback",
		},
		{
			name:      "callback with fragment",
			yaml:      "project: demo\napplication:\n  callback: \"http://localhost:8000/ussd#frag\"\nussd:\n  service_code: \"*124#\"\n",
			wantField: "application.callback",
		},
		{
			name:      "missing service code",
			yaml:      "project: demo\napplication:\n  callback: http://localhost:8000/ussd\n",
			wantField: "ussd.service_code",
		},
		{
			name:      "malformed service code",
			yaml:      "project: demo\napplication:\n  callback: http://localhost:8000/ussd\nussd:\n  service_code: \"124\"\n",
			wantField: "ussd.service_code",
		},
		{
			name:      "negative timeout",
			yaml:      "project: demo\napplication:\n  callback: http://localhost:8000/ussd\nussd:\n  service_code: \"*124#\"\n  session_timeout: -1\n",
			wantField: "ussd.session_timeout",
		},
		{
			name:      "absurd timeout",
			yaml:      "project: demo\napplication:\n  callback: http://localhost:8000/ussd\nussd:\n  service_code: \"*124#\"\n  session_timeout: 999999\n",
			wantField: "ussd.session_timeout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := Parse(strings.NewReader(tt.yaml))
			if err == nil {
				t.Fatalf("Parse() error = nil, want error mentioning %q", tt.wantField)
			}

			var verrs ValidationErrors
			if !errors.As(err, &verrs) {
				t.Fatalf("Parse() error = %v (%T), want ValidationErrors", err, err)
			}

			for _, ve := range verrs {
				if ve.Field == tt.wantField {
					return
				}
			}
			t.Errorf("no ValidationError for field %q; got %v", tt.wantField, err)
		})
	}
}

// All problems are reported in one pass so the developer fixes them together.
func TestValidate_ReportsAllProblems(t *testing.T) {
	t.Parallel()

	cfg := &Config{}
	err := cfg.Validate()

	var verrs ValidationErrors
	if !errors.As(err, &verrs) {
		t.Fatalf("Validate() error = %v, want ValidationErrors", err)
	}
	if len(verrs) < 3 {
		t.Errorf("got %d problems (%v), want at least 3", len(verrs), verrs)
	}
}

func TestParse_ValidServiceCodes(t *testing.T) {
	t.Parallel()

	for _, code := range []string{"*124#", "*123*1#", "*1#", "*920*45#"} {
		t.Run(code, func(t *testing.T) {
			cfg := &Config{
				Project:     "demo",
				Application: Application{Callback: "http://localhost:8000/ussd"},
				USSD:        USSD{ServiceCode: code, SessionTimeout: 60},
				Simulator:   Simulator{Port: DefaultPort},
			}
			if err := cfg.Validate(); err != nil {
				t.Errorf("Validate() error = %v, want nil for %q", err, code)
			}
		})
	}
}

func TestLoad_NotFound(t *testing.T) {
	t.Parallel()

	_, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Load() error = %v, want ErrNotFound", err)
	}
}

func TestLoad_ReadsFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, DefaultFilename)
	if err := os.WriteFile(path, []byte(validYAML), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Project != "my-fintech" {
		t.Errorf("Project = %q", cfg.Project)
	}
}

func TestParse_DefaultsPort(t *testing.T) {
	t.Parallel()

	cfg, err := Parse(strings.NewReader(validYAML))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cfg.Simulator.Port != DefaultPort {
		t.Errorf("Port = %d, want %d", cfg.Simulator.Port, DefaultPort)
	}
	if cfg.Simulator.Host != "" {
		t.Errorf("Host = %q, want empty (auto-detect)", cfg.Simulator.Host)
	}
}

func TestParse_SimulatorSection(t *testing.T) {
	t.Parallel()

	const y = `
project: demo
application:
  callback: http://localhost:8000/ussd
ussd:
  service_code: "*124#"
simulator:
  host: 192.168.1.20
  port: 9000
`
	cfg, err := Parse(strings.NewReader(y))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cfg.Simulator.Host != "192.168.1.20" {
		t.Errorf("Host = %q", cfg.Simulator.Host)
	}
	if cfg.Simulator.Port != 9000 {
		t.Errorf("Port = %d, want 9000", cfg.Simulator.Port)
	}
}

func TestParse_InvalidSimulator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		yaml      string
		wantField string
	}{
		{
			name:      "port too high",
			yaml:      "project: demo\napplication:\n  callback: http://x:1/u\nussd:\n  service_code: \"*124#\"\nsimulator:\n  port: 70000\n",
			wantField: "simulator.port",
		},
		{
			name:      "negative port",
			yaml:      "project: demo\napplication:\n  callback: http://x:1/u\nussd:\n  service_code: \"*124#\"\nsimulator:\n  port: -1\n",
			wantField: "simulator.port",
		},
		{
			name:      "host as URL",
			yaml:      "project: demo\napplication:\n  callback: http://x:1/u\nussd:\n  service_code: \"*124#\"\nsimulator:\n  host: http://192.168.1.20\n",
			wantField: "simulator.host",
		},
		{
			name:      "host with path",
			yaml:      "project: demo\napplication:\n  callback: http://x:1/u\nussd:\n  service_code: \"*124#\"\nsimulator:\n  host: 192.168.1.20/foo\n",
			wantField: "simulator.host",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := Parse(strings.NewReader(tt.yaml))
			if err == nil {
				t.Fatalf("Parse() error = nil, want error for %s", tt.wantField)
			}

			var verrs ValidationErrors
			if !errors.As(err, &verrs) {
				t.Fatalf("error = %v, want ValidationErrors", err)
			}
			for _, ve := range verrs {
				if ve.Field == tt.wantField {
					return
				}
			}
			t.Errorf("no error for %q; got %v", tt.wantField, err)
		})
	}
}

func TestValidate_AcceptsHostnameAndIP(t *testing.T) {
	t.Parallel()

	for _, host := range []string{"192.168.1.20", "localhost", "my-laptop.local", "::1"} {
		t.Run(host, func(t *testing.T) {
			cfg := &Config{
				Project:     "demo",
				Application: Application{Callback: "http://localhost:8000/ussd"},
				USSD:        USSD{ServiceCode: "*124#", SessionTimeout: 60},
				Simulator:   Simulator{Host: host, Port: DefaultPort},
			}
			if err := cfg.Validate(); err != nil {
				t.Errorf("Validate() error = %v, want nil for host %q", err, host)
			}
		})
	}
}
