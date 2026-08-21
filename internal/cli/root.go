// Package cli implements the ussd command tree.
//
// The CLI is an outermost layer: it may depend on inner packages, never the
// reverse. See docs/adr/001-modular-monolith.md.
package cli

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/yeboahd24/ussd-lab/internal/config"
	"github.com/yeboahd24/ussd-lab/internal/logging"
)

// BuildInfo carries version metadata injected at link time.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

// Env holds the CLI's injected dependencies. Passing these explicitly keeps
// commands testable and avoids package-level mutable state.
type Env struct {
	Build  BuildInfo
	Stdout io.Writer
	Stderr io.Writer
}

// globalFlags are bound on the root command and shared by subcommands.
type globalFlags struct {
	configPath string
	verbose    bool
	logFormat  string
}

// Execute runs the command tree and returns a process exit code.
//
// Returning a code rather than calling os.Exit keeps this function testable.
func Execute(env Env) int {
	if env.Stdout == nil {
		env.Stdout = os.Stdout
	}
	if env.Stderr == nil {
		env.Stderr = os.Stderr
	}

	root := newRootCmd(env)
	root.SetOut(env.Stdout)
	root.SetErr(env.Stderr)

	if err := root.Execute(); err != nil {
		// Cobra has already printed the message; avoid duplicating it.
		return 1
	}
	return 0
}

func newRootCmd(env Env) *cobra.Command {
	flags := &globalFlags{}

	cmd := &cobra.Command{
		Use:   "ussd",
		Short: "Build and test USSD applications locally",
		Long: "USSD Lab runs a local USSD simulator so you can build and test\n" +
			"USSD applications without a shortcode, aggregator or provider sandbox.",
		SilenceUsage:  true,
		SilenceErrors: false,
		// Print help when invoked with no subcommand.
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	cmd.PersistentFlags().StringVarP(&flags.configPath, "config", "c",
		config.DefaultFilename, "path to the project configuration file")
	cmd.PersistentFlags().BoolVarP(&flags.verbose, "verbose", "v",
		false, "enable debug logging")
	cmd.PersistentFlags().StringVar(&flags.logFormat, "log-format",
		string(logging.FormatText), "log format: text or json")

	cmd.AddCommand(newInitCmd(env, flags))
	cmd.AddCommand(newLogsCmd(env, flags))
	cmd.AddCommand(newTestCmd(env, flags))
	cmd.AddCommand(newVersionCmd(env))
	cmd.AddCommand(newDevCmd(env, flags))

	return cmd
}

// newLogger builds the logger implied by the global flags.
func (f *globalFlags) newLogger(w io.Writer) *slog.Logger {
	level := slog.LevelInfo
	if f.verbose {
		level = slog.LevelDebug
	}

	format := logging.FormatText
	if logging.Format(f.logFormat) == logging.FormatJSON {
		format = logging.FormatJSON
	}

	return logging.New(logging.Options{
		Level:  level,
		Format: format,
		Output: w,
	})
}

// loadConfig resolves the project configuration, converting a missing file
// into actionable guidance rather than a bare filesystem error.
func (f *globalFlags) loadConfig() (*config.Config, error) {
	cfg, err := config.Load(f.configPath)
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf(
				"no %s found in this directory\n\nRun 'ussd init' to create one",
				f.configPath)
		}
		return nil, err
	}
	return cfg, nil
}

func isNotFound(err error) bool {
	return errors.Is(err, config.ErrNotFound)
}
