package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/yeboahd24/ussd-lab/internal/testrunner"
)

// DefaultTestDir is where declarative tests live in a project.
const DefaultTestDir = "tests"

type testFlags struct {
	dir     string
	filter  string
	store   string
	dbPath  string
	verbose bool
}

func newTestCmd(env Env, global *globalFlags) *cobra.Command {
	flags := &testFlags{}

	cmd := &cobra.Command{
		Use:   "test",
		Short: "Run declarative USSD tests",
		Long: "Runs every test in the tests/ directory against your application.\n\n" +
			"Tests use the same session engine as the interactive simulator, so\n" +
			"a passing suite means the same thing a phone would.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTest(cmd.Context(), env, global, flags)
		},
	}

	cmd.Flags().StringVar(&flags.dir, "dir", DefaultTestDir, "directory containing test files")
	cmd.Flags().StringVarP(&flags.filter, "filter", "f", "", "run only tests whose name contains this text")
	cmd.Flags().StringVar(&flags.store, "store", "memory", "session store: memory or sqlite")
	cmd.Flags().StringVar(&flags.dbPath, "db", "ussd-lab.db", "database file when --store=sqlite")
	cmd.Flags().BoolVarP(&flags.verbose, "verbose", "V", false, "print the full transcript of every test")

	return cmd
}

func runTest(ctx context.Context, env Env, global *globalFlags, flags *testFlags) error {
	out := env.Stdout
	s := style{supportsColor(out)}

	cfg, err := global.loadConfig()
	if err != nil {
		return err
	}

	specs, err := testrunner.LoadDir(flags.dir)
	if err != nil {
		var noTests *testrunner.ErrNoTests
		if errors.As(err, &noTests) {
			return fmt.Errorf(
				"no tests found in %s/\n\nCreate %s/send-money.yaml to get started",
				flags.dir, flags.dir)
		}
		return err
	}

	if flags.filter != "" {
		specs = filterSpecs(specs, flags.filter)
		if len(specs) == 0 {
			return fmt.Errorf("no tests match %q", flags.filter)
		}
	}

	engine, cleanup, err := buildEngine(ctx, cfg, engineOptions{
		store:  flags.store,
		dbPath: flags.dbPath,
	})
	if err != nil {
		return err
	}
	defer cleanup()

	runner, err := testrunner.New(testrunner.Options{
		Engine:      engine,
		ProjectID:   cfg.Project,
		ServiceCode: cfg.USSD.ServiceCode,
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "\n%s  %s\n\n",
		s.apply(ansiBold, "Running"),
		s.apply(ansiDim, fmt.Sprintf("%d tests against %s", len(specs), cfg.Application.Callback)))

	suite := runner.RunAll(ctx, specs)
	printSuite(out, s, suite, flags.verbose)

	// A non-zero exit code is what makes `ussd test` usable in CI.
	if !suite.Passed() {
		_, failed := suite.Counts()
		return fmt.Errorf("%d of %d tests failed", failed, len(suite.Results))
	}
	return nil
}

func filterSpecs(specs []*testrunner.Spec, filter string) []*testrunner.Spec {
	lower := strings.ToLower(filter)

	var out []*testrunner.Spec
	for _, sp := range specs {
		if strings.Contains(strings.ToLower(sp.Name), lower) {
			out = append(out, sp)
		}
	}
	return out
}

func printSuite(out io.Writer, s style, suite testrunner.Suite, verbose bool) {
	for _, res := range suite.Results {
		printResult(out, s, res, verbose)
	}

	passed, failed := suite.Counts()

	fmt.Fprintln(out)
	summary := fmt.Sprintf("%d passed", passed)
	if failed > 0 {
		summary += fmt.Sprintf(", %d failed", failed)
	}
	fmt.Fprintf(out, "%s  %s\n\n",
		s.apply(colorFor(failed == 0), summary),
		s.apply(ansiDim, roundDuration(suite.Duration).String()))
}

func printResult(out io.Writer, s style, res testrunner.Result, verbose bool) {
	mark := s.apply(ansiGreen, "✓")
	if !res.Passed {
		mark = s.apply(ansiRed, "✗")
	}

	fmt.Fprintf(out, "%s %-32s %s\n", mark, res.Spec.Name,
		s.apply(ansiDim, roundDuration(res.Duration).String()))

	// An error means the test could not run at all -- the application was
	// unreachable, say -- which is a different problem from a failed
	// assertion and is reported as such.
	if res.Err != nil {
		fmt.Fprintf(out, "    %s %v\n", s.apply(ansiRed, "error:"), res.Err)
	}

	for _, f := range res.Failures {
		fmt.Fprintf(out, "    %s %s\n",
			s.apply(ansiRed, f.Where()+":"), f.Message)
	}

	// The transcript is what makes a failure actionable: it shows what the
	// application actually said, not merely that an expectation was unmet.
	if verbose || !res.Passed {
		printTranscript(out, s, res)
	}
}

func printTranscript(out io.Writer, s style, res testrunner.Result) {
	if len(res.Transcript) == 0 {
		return
	}

	fmt.Fprintf(out, "    %s\n", s.apply(ansiDim, "transcript:"))
	for _, ex := range res.Transcript {
		if ex.Input != "" {
			fmt.Fprintf(out, "      %s %s\n", s.apply(ansiBlue, "USER →"), ex.Input)
		}
		fmt.Fprintf(out, "      %s %s %s\n",
			s.apply(ansiDim, "APP  →"), ex.Response.Type, oneLine(ex.Response.Text))
	}
	fmt.Fprintln(out)
}

func colorFor(ok bool) string {
	if ok {
		return ansiGreen
	}
	return ansiRed
}

// roundDuration keeps timings readable; nanosecond precision is noise here.
//
// Sub-millisecond durations round to the microsecond rather than to ten of
// them: a very fast test would otherwise display as "0s", which reads as a
// test that did not run.
func roundDuration(d time.Duration) time.Duration {
	switch {
	case d >= time.Millisecond:
		return d.Round(time.Millisecond)
	case d >= time.Microsecond:
		return d.Round(time.Microsecond)
	default:
		// Sub-microsecond durations are shown unrounded. Rounding them would
		// produce "0s", and reporting a floor value instead would be a small
		// lie about a measurement we actually have.
		return d
	}
}
