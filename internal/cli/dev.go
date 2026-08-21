package cli

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/yeboahd24/ussd-lab/internal/netdetect"
	"github.com/yeboahd24/ussd-lab/internal/qr"
	"github.com/yeboahd24/ussd-lab/internal/session"
	"github.com/yeboahd24/ussd-lab/internal/simulator"
	"github.com/yeboahd24/ussd-lab/internal/storage/sqlite"
)

type devFlags struct {
	host        string
	port        int
	latency     time.Duration
	store       string
	dbPath      string
	noQR        bool
	history     string
	noHistory   bool
	redactInput bool
	tokenTTL    time.Duration
}

func newDevCmd(env Env, global *globalFlags) *cobra.Command {
	flags := &devFlags{}

	cmd := &cobra.Command{
		Use:   "dev",
		Short: "Start the local USSD simulator",
		Long: "Starts the simulator on your LAN and prints a QR code.\n\n" +
			"Scan it with a phone on the same Wi-Fi network to interact with\n" +
			"your USSD application. The phone may be in airplane mode as long\n" +
			"as Wi-Fi is on -- no cellular network is involved.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDev(cmd.Context(), env, global, flags)
		},
	}

	cmd.Flags().StringVar(&flags.host, "host", "",
		"address to advertise in the QR code (overrides auto-detection)")
	cmd.Flags().IntVarP(&flags.port, "port", "p", 0,
		"port to listen on (overrides ussd.yaml)")
	cmd.Flags().DurationVar(&flags.latency, "latency", 0,
		"delay every application call, to simulate a slow network")
	cmd.Flags().StringVar(&flags.store, "store", "memory",
		"session store: memory or sqlite")
	cmd.Flags().StringVar(&flags.dbPath, "db", "ussd-lab.db",
		"database file when --store=sqlite")
	cmd.Flags().BoolVar(&flags.noQR, "no-qr", false,
		"print the URL without a QR code")
	cmd.Flags().StringVar(&flags.history, "history", DefaultHistoryPath,
		"path to the session history database")
	cmd.Flags().BoolVar(&flags.noHistory, "no-history", false,
		"do not record session history")
	cmd.Flags().BoolVar(&flags.redactInput, "redact-input", false,
		"replace user input in recorded history (use when testing PIN entry)")
	cmd.Flags().DurationVar(&flags.tokenTTL, "token-ttl", simulator.DefaultTokenTTL,
		"how long the QR code stays valid")

	return cmd
}

func runDev(ctx context.Context, env Env, global *globalFlags, flags *devFlags) error {
	out := env.Stdout

	cfg, err := global.loadConfig()
	if err != nil {
		return err
	}

	port := cfg.Simulator.Port
	if flags.port != 0 {
		port = flags.port
	}

	// Resolve the advertised address before starting anything, so a detection
	// failure is reported immediately rather than after the server is up.
	advertised, detection, err := resolveHost(flags.host, cfg.Simulator.Host)
	if err != nil {
		return err
	}

	color := supportsColor(out)
	live := newLiveLog(out, color)

	// History is recorded regardless of which session store is in use. Live
	// session state and history have different lifetimes: the first is
	// worthless once the process exits, the second is the whole point of
	// `ussd logs` (ADR-003).
	sinks := session.MultiSink{live}

	var history *sqlite.EventStore
	if !flags.noHistory {
		history, err = openHistory(ctx, flags.history)
		if err != nil {
			return err
		}
		defer history.Close()

		// Redaction wraps only the durable sink: the live terminal view keeps
		// full input, while nothing sensitive reaches the file on disk.
		if flags.redactInput {
			sinks = append(sinks, session.NewRedactingSink(history))
		} else {
			sinks = append(sinks, history)
		}
	}

	engine, cleanup, err := buildEngine(ctx, cfg, engineOptions{
		store:   flags.store,
		dbPath:  flags.dbPath,
		latency: flags.latency,
		events:  sinks,
	})
	if err != nil {
		return err
	}
	defer cleanup()

	srv, err := simulator.New(simulator.Options{
		Engine:      engine,
		ProjectID:   cfg.Project,
		ServiceCode: cfg.USSD.ServiceCode,
		BindAddr:    net.JoinHostPort("0.0.0.0", strconv.Itoa(port)),
		TokenTTL:    flags.tokenTTL,
		Logger:      discardLogger(),
	})
	if err != nil {
		return fmt.Errorf("%w\n\nIs another process already using port %d?", err, port)
	}

	token, err := srv.IssueToken()
	if err != nil {
		return err
	}

	url := fmt.Sprintf("http://%s/s/%s", net.JoinHostPort(advertised, strconv.Itoa(srv.Port())), token)

	printDashboard(out, dashboardInfo{
		project:     cfg.Project,
		url:         url,
		callback:    cfg.Application.Callback,
		serviceCode: cfg.USSD.ServiceCode,
		store:       flags.store,
		latency:     flags.latency,
		redacted:    flags.redactInput,
		detection:   detection,
		color:       color,
		showQR:      !flags.noQR,
	})

	// Ctrl-C shuts the server down gracefully so an in-flight USSD request is
	// not cut off mid-session.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	err = srv.Serve(ctx)

	s := style{color}
	fmt.Fprintf(out, "\n%s\n", s.apply(ansiDim, "Simulator stopped."))

	if history != nil {
		// Never let dropped history pass silently. Emit cannot fail a session,
		// so the only honest place to report a write failure is here.
		if n := history.Dropped(); n > 0 {
			fmt.Fprintf(out, "%s\n", s.apply(ansiAmber,
				fmt.Sprintf("Warning: %d session events could not be recorded.", n)))
		} else {
			fmt.Fprintf(out, "%s\n", s.apply(ansiDim,
				"Run 'ussd logs' to review this run's sessions."))
		}
	}
	return err
}

// openHistory prepares the history database, creating its directory on demand.
func openHistory(ctx context.Context, path string) (*sqlite.EventStore, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create history directory %s: %w", dir, err)
		}
	}
	return sqlite.OpenHistory(ctx, path, nil)
}

// resolveHost decides which address to advertise, preferring an explicit
// choice and falling back to scored detection.
func resolveHost(flagHost, configHost string) (string, *netdetect.Result, error) {
	if flagHost != "" {
		return flagHost, nil, nil
	}
	if configHost != "" {
		return configHost, nil, nil
	}

	res, err := netdetect.DetectSystem()
	if err != nil {
		return "", nil, err
	}
	if res.Best == nil {
		return "", nil, fmt.Errorf(
			"could not find a LAN address to advertise\n\n" +
				"Connect to Wi-Fi, or pass --host <your-ip> explicitly")
	}
	return res.Best.Addr.String(), &res, nil
}

type dashboardInfo struct {
	project     string
	url         string
	callback    string
	serviceCode string
	store       string
	latency     time.Duration
	redacted    bool
	detection   *netdetect.Result
	color       bool
	showQR      bool
}

func printDashboard(out io.Writer, info dashboardInfo) {
	s := style{info.color}

	fmt.Fprintf(out, "\n%s\n\n", s.apply(ansiBold, "USSD Lab"))
	fmt.Fprintf(out, "Project:       %s\n", info.project)
	fmt.Fprintf(out, "Service code:  %s\n", info.serviceCode)
	fmt.Fprintf(out, "Callback:      %s\n", info.callback)
	fmt.Fprintf(out, "Store:         %s\n", info.store)
	if info.latency > 0 {
		fmt.Fprintf(out, "Latency:       %s (simulated)\n", info.latency)
	}
	if info.redacted {
		fmt.Fprintf(out, "History:       %s\n",
			s.apply(ansiDim, "user input redacted"))
	}
	fmt.Fprintf(out, "\nSimulator:     %s\n", s.apply(ansiBlue, info.url))

	// When detection was ambiguous, show the alternatives. A silently wrong
	// address produces a QR code that scans and then hangs, which is the
	// hardest possible failure to diagnose (ADR-004).
	if d := info.detection; d != nil && d.Ambiguous {
		fmt.Fprintf(out, "\n%s\n",
			s.apply(ansiAmber, "Several interfaces look equally plausible."))
		for _, c := range d.Candidates {
			fmt.Fprintf(out, "  %s\n", s.apply(ansiDim, c.String()))
		}
		fmt.Fprintf(out, "%s\n",
			s.apply(ansiDim, "If the phone cannot connect, pass --host explicitly."))
	}

	if info.showQR {
		fmt.Fprintln(out)
		if err := qr.Render(out, info.url, qr.Options{Color: info.color}); err != nil {
			fmt.Fprintf(out, "%s\n", s.apply(ansiAmber,
				"Could not render a QR code; open the URL above instead."))
		}
	}

	fmt.Fprintf(out, "\n%s\n", "Scan the QR code with your phone on the same Wi-Fi network.")
	fmt.Fprintf(out, "%s\n", s.apply(ansiDim,
		"The phone may stay in airplane mode as long as Wi-Fi is on."))
	fmt.Fprintf(out, "\n%s\n", s.apply(ansiDim, "Waiting for a session…  (Ctrl-C to stop)"))
}
