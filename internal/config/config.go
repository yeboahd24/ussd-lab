// Package config loads and validates the ussd.yaml project configuration.
//
// Configuration is the sole source of truth for the callback URL that the
// simulator is permitted to contact. Nothing may widen that set at runtime —
// see docs/adr/004-local-networking-and-qr-tokens.md.
package config

import (
	"errors"
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

// DefaultFilename is the conventional project configuration file name.
const DefaultFilename = "ussd.yaml"

// DefaultSessionTimeout is applied when ussd.session_timeout is omitted.
const DefaultSessionTimeout = 120

// DefaultPort is the simulator's listening port.
const DefaultPort = 7345

// Config mirrors the project configuration described in the MVP design (§16).
//
//	project: my-fintech
//	application:
//	  callback: http://localhost:8000/ussd
//	ussd:
//	  service_code: "*124#"
//	  session_timeout: 120
type Config struct {
	Project     string      `yaml:"project"`
	Application Application `yaml:"application"`
	USSD        USSD        `yaml:"ussd"`
	Simulator   Simulator   `yaml:"simulator"`
}

// Simulator configures the local LAN server.
//
// The server always binds 0.0.0.0 so a phone on the same Wi-Fi can reach it
// (MVP design §8). Host is only the address ADVERTISED in the QR code and the
// terminal -- it overrides automatic interface detection, which is unreliable
// on machines with many virtual interfaces (ADR-004).
type Simulator struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

// Application describes the developer's own USSD application.
type Application struct {
	// Callback is the single HTTP endpoint the simulator forwards to.
	Callback string `yaml:"callback"`
}

// USSD holds protocol-level project settings.
type USSD struct {
	ServiceCode string `yaml:"service_code"`
	// SessionTimeout is the session lifetime in seconds.
	SessionTimeout int `yaml:"session_timeout"`
}

// ErrNotFound is returned when the configuration file does not exist.
var ErrNotFound = errors.New("configuration file not found")

// Load reads, parses, defaults and validates the configuration at path.
//
// A returned Config is always safe to use: callers need not re-check fields.
func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return nil, fmt.Errorf("open config %s: %w", path, err)
	}
	defer f.Close()

	cfg, err := Parse(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Parse decodes, defaults and validates configuration from r.
//
// Parsing is strict: unknown fields are rejected so that a typo such as
// "callbak:" fails loudly instead of silently leaving Callback empty.
func Parse(r io.Reader) (*Config, error) {
	var cfg Config

	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)

	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, ValidationErrors{{Field: "", Message: "configuration is empty"}}
		}
		return nil, fmt.Errorf("parse yaml: %w", err)
	}

	cfg.applyDefaults()

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// applyDefaults fills optional fields. It never overrides an explicit value,
// including an explicitly invalid one -- defaulting must not mask a mistake.
func (c *Config) applyDefaults() {
	if c.USSD.SessionTimeout == 0 {
		c.USSD.SessionTimeout = DefaultSessionTimeout
	}
	if c.Simulator.Port == 0 {
		c.Simulator.Port = DefaultPort
	}
}
