package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/yeboahd24/ussd-lab/internal/session"
	"github.com/yeboahd24/ussd-lab/internal/storage/sqlite"
)

// DefaultHistoryPath is where session history is kept.
//
// It lives in a dot-directory so a project's working tree stays clean, and so a
// single .gitignore entry covers everything USSD Lab writes.
const DefaultHistoryPath = ".ussd/history.db"

type logsFlags struct {
	path  string
	limit int
}

func newLogsCmd(env Env, global *globalFlags) *cobra.Command {
	flags := &logsFlags{}

	cmd := &cobra.Command{
		Use:   "logs [session-id]",
		Short: "Show recorded USSD sessions",
		Long: "Without arguments, lists recent sessions.\n\n" +
			"Given a session id, prints that conversation's full transcript --\n" +
			"the same view `ussd dev` shows live, available after the fact.",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			var id string
			if len(args) == 1 {
				id = args[0]
			}
			return runLogs(cmd.Context(), env, flags, id)
		},
	}

	cmd.Flags().StringVar(&flags.path, "history", DefaultHistoryPath, "path to the history database")
	cmd.Flags().IntVarP(&flags.limit, "limit", "n", sqlite.DefaultHistoryLimit, "maximum sessions to list")

	return cmd
}

func runLogs(ctx context.Context, env Env, flags *logsFlags, sessionID string) error {
	out := env.Stdout
	s := style{supportsColor(out)}

	// Distinguish "no history yet" from a real failure: the first is normal
	// before the first `ussd dev`, and telling someone their database is
	// corrupt when they simply have not run anything is unhelpful.
	if _, err := os.Stat(flags.path); os.IsNotExist(err) {
		return fmt.Errorf(
			"no session history at %s\n\nRun 'ussd dev' and complete a session first",
			flags.path)
	}

	store, err := sqlite.OpenHistory(ctx, flags.path, nil)
	if err != nil {
		return err
	}
	defer store.Close()

	if sessionID != "" {
		return printTranscriptFor(ctx, out, s, store, sessionID)
	}
	return printSessionList(ctx, out, s, store, flags.limit)
}

func printSessionList(
	ctx context.Context,
	out io.Writer,
	s style,
	store session.HistoryStore,
	limit int,
) error {
	sessions, err := store.ListSessions(ctx, limit)
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		fmt.Fprintf(out, "\n%s\n\n", s.apply(ansiDim, "No sessions recorded yet."))
		return nil
	}

	// Padding is applied BEFORE colour. ANSI escapes have no display width, so
	// padding a already-coloured string misaligns every row once colour is on.
	header := fmt.Sprintf("%-22s %-8s %-10s %-14s %6s  %s",
		"SESSION", "CODE", "STATUS", "PHONE", "INPUTS", "STARTED")
	fmt.Fprintf(out, "\n%s\n", s.apply(ansiBold, header))

	now := time.Now()
	for _, sum := range sessions {
		fmt.Fprintf(out, "%-22s %-8s %s %-14s %6d  %s\n",
			sum.SessionID,
			sum.ServiceCode,
			s.apply(statusColor(sum.Status), fmt.Sprintf("%-10s", sum.Status)),
			sum.PhoneNumber,
			sum.InputCount,
			s.apply(ansiDim, relativeTime(sum.StartedAt, now)))
	}

	fmt.Fprintf(out, "\n%s\n\n", s.apply(ansiDim,
		fmt.Sprintf("%d sessions.  Run 'ussd logs <session-id>' for a transcript.",
			len(sessions))))
	return nil
}

func printTranscriptFor(
	ctx context.Context,
	out io.Writer,
	s style,
	store session.HistoryStore,
	sessionID string,
) error {
	events, err := store.ListEvents(ctx, sessionID)
	if err != nil {
		return err
	}
	if len(events) == 0 {
		return fmt.Errorf("no session %s in the history\n\nRun 'ussd logs' to list sessions", sessionID)
	}

	// The header details live in the first event's payload, since summaries
	// are reconstructed from events rather than stored separately.
	var serviceCode, phone string
	for _, e := range events {
		if e.Type == session.EventSessionStarted {
			serviceCode = eventString(e, "service_code")
			phone = eventString(e, "phone_number")
			break
		}
	}

	fmt.Fprintf(out, "\n%s %s\n", s.apply(ansiBold, "Session:"), sessionID)
	if phone != "" {
		fmt.Fprintf(out, "%s   %s\n", s.apply(ansiBold, "Phone:"), phone)
	}
	if serviceCode != "" {
		fmt.Fprintf(out, "%s %s\n", s.apply(ansiBold, "Service:"), serviceCode)
	}
	fmt.Fprintln(out)

	// Rendering reuses the same formatter as the live view, so a transcript
	// read after the fact is identical to what scrolled past during `ussd dev`.
	renderer := newLiveLog(out, s.enabled)
	renderer.SetSessionHeader(false)
	for _, e := range events {
		renderer.Emit(ctx, e)
	}

	fmt.Fprintln(out)
	return nil
}

func eventString(e session.Event, key string) string {
	if e.Payload == nil {
		return ""
	}
	if v, ok := e.Payload[key].(string); ok {
		return v
	}
	return ""
}

func statusColor(st session.Status) string {
	switch st {
	case session.StatusCompleted:
		return ansiGreen
	case session.StatusError, session.StatusTimeout:
		return ansiRed
	case session.StatusCancelled:
		return ansiAmber
	default:
		return ansiDim
	}
}

// relativeTime renders a timestamp as "3m ago", which is easier to scan than
// an absolute time when looking for the session you just ran.
func relativeTime(t time.Time, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
