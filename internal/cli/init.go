package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yeboahd24/ussd-lab/internal/config"
)

type initFlags struct {
	project     string
	callback    string
	serviceCode string
	force       bool
}

// projectNameSanitiser strips characters the config validator rejects, so a
// directory called "My Fintech App!" still yields a usable default.
var projectNameSanitiser = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func newInitCmd(env Env, global *globalFlags) *cobra.Command {
	flags := &initFlags{}

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create a USSD Lab project in the current directory",
		Long: "Writes a ussd.yaml describing how USSD Lab should reach your\n" +
			"application, plus a short README. Your application itself stays\n" +
			"wherever it already lives -- USSD Lab does not scaffold it.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInit(env, global, flags)
		},
	}

	cmd.Flags().StringVar(&flags.project, "project", "",
		"project name (default: the current directory name)")
	cmd.Flags().StringVar(&flags.callback, "callback", "http://localhost:8000/ussd",
		"URL of your USSD application")
	cmd.Flags().StringVar(&flags.serviceCode, "service-code", "*124#",
		"USSD short code this project answers")
	cmd.Flags().BoolVar(&flags.force, "force", false,
		"overwrite an existing ussd.yaml")

	return cmd
}

func runInit(env Env, global *globalFlags, flags *initFlags) error {
	out := env.Stdout
	color := supportsColor(out)
	s := style{color}

	path := global.configPath
	if path == "" {
		path = config.DefaultFilename
	}

	// Refuse to clobber an existing project by default. Overwriting a config
	// that points at someone's real application is not recoverable from the
	// CLI, so it must be an explicit choice.
	if _, err := os.Stat(path); err == nil && !flags.force {
		return fmt.Errorf("%s already exists\n\nPass --force to overwrite it", path)
	}

	project := flags.project
	if project == "" {
		project = defaultProjectName()
	}

	contents := renderConfig(project, flags.callback, flags.serviceCode)

	// Validate before writing. Writing a file that the very next command
	// rejects would be a poor first experience, and catches a bad --callback
	// or --service-code at the point the user can still see why.
	if _, err := config.Parse(strings.NewReader(contents)); err != nil {
		return fmt.Errorf("the requested settings are not valid:\n%w", err)
	}

	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	// A starter test, so `ussd test` has something to run immediately.
	testDir := filepath.Join(filepath.Dir(path), "tests")
	wroteTest := false
	if _, err := os.Stat(testDir); os.IsNotExist(err) {
		if err := os.MkdirAll(testDir, 0o755); err == nil {
			testPath := filepath.Join(testDir, "main-menu.yaml")
			if err := os.WriteFile(testPath, []byte(renderStarterTest(flags.serviceCode)), 0o644); err == nil {
				wroteTest = true
			}
		}
	}

	readmePath := filepath.Join(filepath.Dir(path), "README.md")
	wroteReadme := false
	if _, err := os.Stat(readmePath); os.IsNotExist(err) {
		if err := os.WriteFile(readmePath, []byte(renderReadme(project, flags.serviceCode, flags.callback)), 0o644); err == nil {
			wroteReadme = true
		}
	}

	fmt.Fprintf(out, "\n%s\n\n", s.apply(ansiBold, "Created "+path))
	fmt.Fprintf(out, "  Project:       %s\n", project)
	fmt.Fprintf(out, "  Service code:  %s\n", flags.serviceCode)
	fmt.Fprintf(out, "  Callback:      %s\n", flags.callback)
	var also []string
	if wroteTest {
		also = append(also, "tests/main-menu.yaml")
	}
	if wroteReadme {
		also = append(also, "README.md")
	}
	if len(also) > 0 {
		fmt.Fprintf(out, "\n%s\n", s.apply(ansiDim, "Also wrote "+strings.Join(also, " and ")))
	}
	fmt.Fprintf(out, "\nNext:\n")
	fmt.Fprintf(out, "  1. Start your application so it answers POST %s\n", flags.callback)
	fmt.Fprintf(out, "  2. Run %s\n", s.apply(ansiBlue, "ussd dev"))
	fmt.Fprintf(out, "  3. Scan the QR code with a phone on the same Wi-Fi\n")
	fmt.Fprintf(out, "\nOr run %s to check your menus without a phone.\n\n",
		s.apply(ansiBlue, "ussd test"))

	return nil
}

// renderStarterTest writes a test that passes for any application answering
// the first dial with a CON screen, so a new project is green immediately.
func renderStarterTest(serviceCode string) string {
	return fmt.Sprintf(`# A declarative USSD test.
#
# Run every test in this directory with:
#
#     ussd test
#
# Tests use the same session engine as the interactive simulator, so a passing
# suite means the same thing a phone would.

name: Main Menu

dial: %q

# Each step sends one user input. Assertions under "expect" apply to the screen
# that input produces.
#
# steps:
#   - input: "1"
#     expect:
#       - contains: "Enter amount"

# Assertions here apply to the final screen.
assertions:
  - type: CON
    status: ACTIVE
`, serviceCode)
}

// defaultProjectName derives a valid project name from the working directory.
func defaultProjectName() string {
	wd, err := os.Getwd()
	if err != nil {
		return "my-ussd-app"
	}

	name := projectNameSanitiser.ReplaceAllString(filepath.Base(wd), "-")
	name = strings.Trim(name, "-._")

	if name == "" || !isValidStart(name) {
		return "my-ussd-app"
	}
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}

func isValidStart(s string) bool {
	c := s[0]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func renderConfig(project, callback, serviceCode string) string {
	return fmt.Sprintf(`# USSD Lab project configuration.
#
# USSD Lab forwards every USSD request to the callback below and nothing else.
# It is the only address the simulator will ever contact.

project: %s

application:
  # Your application must answer POST requests here with either
  #   CON <text>   to show a screen and wait for input
  #   END <text>   to show a final screen and end the session
  callback: %s

ussd:
  # The short code this project answers. Dialling anything else is rejected,
  # just as it would be on a real network.
  service_code: %q

  # Seconds of user think-time allowed between screens.
  session_timeout: 120

simulator:
  # Port the simulator listens on. It always binds every interface so a phone
  # on the same Wi-Fi can reach it.
  port: %d

  # Address advertised in the QR code. Leave blank to detect it automatically;
  # set it if auto-detection picks the wrong interface.
  host: ""
`, project, callback, serviceCode, config.DefaultPort)
}

func renderReadme(project, serviceCode, callback string) string {
	return fmt.Sprintf(`# %s

A USSD project managed with [USSD Lab](https://github.com/yeboahd24/ussd-lab).

## Running

1. Start your application so that it answers:

       POST %s

2. Start the simulator:

       ussd dev

3. Scan the QR code with a phone on the same Wi-Fi network, then dial %s.

The phone may stay in airplane mode as long as Wi-Fi is on. No cellular
network, shortcode or aggregator is involved.

## The protocol

USSD Lab posts JSON to your callback:

    {
      "request_id":   "req_...",
      "session_id":   "sess_...",
      "service_code": "%s",
      "phone_number": "233240000001",
      "network":      "SIMULATOR",
      "text":         "1*0241234567"
    }

`+"`text`"+` accumulates everything the user has entered so far, joined by `+"`*`"+`.
It is empty on the first request. Your application stays stateless: work out
where you are in the menu from `+"`text`"+` alone.

Answer with plain text:

    CON Enter amount:          # show this screen, wait for input
    END Transaction successful. # show this screen, end the session

No SDK is required. Any language that can read JSON and print a string works.
`, project, callback, serviceCode, serviceCode)
}
