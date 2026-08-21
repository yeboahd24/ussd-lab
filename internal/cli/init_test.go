package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeboahd24/ussd-lab/internal/config"
)

// runCLI executes the command tree with args, capturing output.
func runCLI(t *testing.T, dir string, args ...string) (code int, stdout, stderr string) {
	t.Helper()

	// Commands write relative to the working directory, so tests run in a
	// temp dir. os.Chdir is process-wide, hence no t.Parallel() in these tests.
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir(%s): %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	var out, errBuf bytes.Buffer
	env := Env{
		Build:  BuildInfo{Version: "test", Commit: "abc123", Date: "2026-08-21"},
		Stdout: &out,
		Stderr: &errBuf,
	}

	root := newRootCmd(env)
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(args)

	if err := root.Execute(); err != nil {
		return 1, out.String(), errBuf.String()
	}
	return 0, out.String(), errBuf.String()
}

func TestInit_WritesUsableConfig(t *testing.T) {
	dir := t.TempDir()

	code, stdout, stderr := runCLI(t, dir, "init")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "Created ussd.yaml") {
		t.Errorf("stdout = %q", stdout)
	}

	// The generated file must be accepted by the very validator that will read
	// it -- a scaffold that fails on the next command is worse than none.
	cfg, err := config.Load(filepath.Join(dir, config.DefaultFilename))
	if err != nil {
		t.Fatalf("generated config is invalid: %v", err)
	}
	if cfg.USSD.ServiceCode != "*124#" {
		t.Errorf("ServiceCode = %q", cfg.USSD.ServiceCode)
	}
	if cfg.Application.Callback != "http://localhost:8000/ussd" {
		t.Errorf("Callback = %q", cfg.Application.Callback)
	}
	if cfg.Simulator.Port != config.DefaultPort {
		t.Errorf("Port = %d", cfg.Simulator.Port)
	}
}

func TestInit_DerivesProjectNameFromDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my-fintech")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	if code, _, stderr := runCLI(t, dir, "init"); code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}

	cfg, err := config.Load(filepath.Join(dir, config.DefaultFilename))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Project != "my-fintech" {
		t.Errorf("Project = %q, want my-fintech", cfg.Project)
	}
}

// A directory name the config validator would reject must still produce a
// working project.
func TestInit_SanitisesAwkwardDirectoryName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "My Fintech App!")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	if code, _, stderr := runCLI(t, dir, "init"); code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}

	if _, err := config.Load(filepath.Join(dir, config.DefaultFilename)); err != nil {
		t.Errorf("generated config is invalid: %v", err)
	}
}

// Overwriting a config that points at someone's real application is not
// recoverable from the CLI, so it must be explicit.
func TestInit_RefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, config.DefaultFilename)

	if err := os.WriteFile(path, []byte("project: existing\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	code, _, stderr := runCLI(t, dir, "init")
	if code == 0 {
		t.Fatal("exit = 0, want failure when ussd.yaml exists")
	}
	if !strings.Contains(stderr, "--force") {
		t.Errorf("stderr = %q, want a mention of --force", stderr)
	}

	// The existing file must be untouched.
	got, _ := os.ReadFile(path)
	if string(got) != "project: existing\n" {
		t.Error("the existing config was modified")
	}
}

func TestInit_ForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, config.DefaultFilename)

	if err := os.WriteFile(path, []byte("project: existing\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if code, _, stderr := runCLI(t, dir, "init", "--force"); code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}
	if _, err := config.Load(path); err != nil {
		t.Errorf("config after --force is invalid: %v", err)
	}
}

func TestInit_CustomFlags(t *testing.T) {
	dir := t.TempDir()

	code, _, stderr := runCLI(t, dir, "init",
		"--project", "acme",
		"--callback", "http://localhost:9999/ussd",
		"--service-code", "*789*1#")
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}

	cfg, err := config.Load(filepath.Join(dir, config.DefaultFilename))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Project != "acme" {
		t.Errorf("Project = %q", cfg.Project)
	}
	if cfg.USSD.ServiceCode != "*789*1#" {
		t.Errorf("ServiceCode = %q", cfg.USSD.ServiceCode)
	}
}

// Bad settings must fail before anything is written, not after.
func TestInit_RejectsInvalidSettings(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"file callback", []string{"init", "--callback", "file:///etc/passwd"}},
		{"relative callback", []string{"init", "--callback", "/ussd"}},
		{"malformed service code", []string{"init", "--service-code", "124"}},
		{"callback with credentials", []string{"init", "--callback", "http://u:p@localhost:8000/x"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()

			code, _, _ := runCLI(t, dir, tt.args...)
			if code == 0 {
				t.Fatal("exit = 0, want failure")
			}
			if _, err := os.Stat(filepath.Join(dir, config.DefaultFilename)); !os.IsNotExist(err) {
				t.Error("an invalid config was written to disk")
			}
		})
	}
}

func TestInit_WritesReadme(t *testing.T) {
	dir := t.TempDir()

	if code, _, stderr := runCLI(t, dir, "init"); code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}

	body, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatalf("README.md not written: %v", err)
	}
	for _, want := range []string{"CON ", "END ", "ussd dev", "*124#"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("README.md missing %q", want)
		}
	}
}

// An existing README must not be clobbered.
func TestInit_PreservesExistingReadme(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "README.md")

	if err := os.WriteFile(path, []byte("# mine\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if code, _, stderr := runCLI(t, dir, "init"); code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}

	got, _ := os.ReadFile(path)
	if string(got) != "# mine\n" {
		t.Error("an existing README.md was overwritten")
	}
}

func TestVersion(t *testing.T) {
	dir := t.TempDir()

	code, stdout, _ := runCLI(t, dir, "version")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	for _, want := range []string{"test", "abc123", "go1."} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	}

	code, stdout, _ = runCLI(t, dir, "version", "--short")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if strings.TrimSpace(stdout) != "test" {
		t.Errorf("--short stdout = %q, want \"test\"", stdout)
	}
}

func TestRoot_UnknownCommand(t *testing.T) {
	dir := t.TempDir()

	if code, _, _ := runCLI(t, dir, "bogus"); code == 0 {
		t.Error("exit = 0 for an unknown command")
	}
}
