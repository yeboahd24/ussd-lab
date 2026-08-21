package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"

	"github.com/yeboahd24/ussd-lab/internal/qr"
	"github.com/yeboahd24/ussd-lab/internal/session"
)

// ANSI styling for the terminal dashboard. Kept minimal and always paired with
// a reset, so a redirected log file stays readable.
const (
	ansiReset = "\x1b[0m"
	ansiDim   = "\x1b[2m"
	ansiBold  = "\x1b[1m"
	ansiBlue  = "\x1b[34m"
	ansiGreen = "\x1b[32m"
	ansiRed   = "\x1b[31m"
	ansiAmber = "\x1b[33m"
)

// style applies an ANSI code when colour is enabled.
type style struct{ enabled bool }

func (s style) apply(code, text string) string {
	if !s.enabled {
		return text
	}
	return code + text + ansiReset
}

// liveLog renders session events to the terminal as they happen.
//
// It is an EventSink, so the engine emits to it without knowing a terminal
// exists. Wiring it alongside an in-memory sink via session.MultiSink is what
// lets `ussd dev` show a live view and keep history at the same time, with no
// change to the engine (MVP design §15).
type liveLog struct {
	mu    sync.Mutex
	out   io.Writer
	style style

	// lastSession suppresses repeating the session header for consecutive
	// events in the same conversation, which is what makes a multi-session
	// stream readable.
	lastSession string

	// showHeader is false when replaying a single session, where the header is
	// printed once by the caller.
	showHeader bool
}

func newLiveLog(out io.Writer, color bool) *liveLog {
	return &liveLog{out: out, style: style{enabled: color}, showHeader: true}
}

// SetSessionHeader controls whether a header is printed when the session
// changes.
func (l *liveLog) SetSessionHeader(show bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.showHeader = show
}

func (l *liveLog) Emit(_ context.Context, e session.Event) {
	l.mu.Lock()
	defer l.mu.Unlock()

	line, ok := l.format(e)
	if !ok {
		return
	}

	if l.showHeader && e.SessionID != l.lastSession {
		fmt.Fprintf(l.out, "\n%s\n", l.style.apply(ansiDim, "── "+e.SessionID+" "+
			strings.Repeat("─", max(0, 48-len(e.SessionID)))))
		l.lastSession = e.SessionID
	}

	fmt.Fprintf(l.out, "%s  %s\n",
		l.style.apply(ansiDim, e.Timestamp.Format("15:04:05")), line)
}

// format renders one event, or reports false for events that add noise rather
// than information. APPLICATION_REQUEST is suppressed because the input that
// triggered it was already shown.
func (l *liveLog) format(e session.Event) (string, bool) {
	s := l.style

	switch e.Type {
	case session.EventSessionStarted:
		return s.apply(ansiBold, "START ") + str(e.Payload["service_code"]), true

	case session.EventInputReceived:
		return s.apply(ansiBlue, "USER → ") + str(e.Payload["text"]), true

	case session.EventApplicationResponse:
		kind := str(e.Payload["type"])
		color := ansiGreen
		if kind == "END" {
			color = ansiBold
		}
		return s.apply(color, "APP  → "+kind) + " " + oneLine(str(e.Payload["text"])), true

	case session.EventSessionCompleted:
		return s.apply(ansiDim, "SESSION COMPLETED"), true

	case session.EventSessionCancelled:
		return s.apply(ansiAmber, "SESSION CANCELLED"), true

	case session.EventSessionTimeout:
		return s.apply(ansiAmber, "SESSION TIMEOUT"), true

	case session.EventApplicationError:
		return s.apply(ansiRed, "APPLICATION ERROR ") + str(e.Payload["error"]), true

	default:
		return "", false
	}
}

// oneLine flattens a multi-line USSD screen so the log stays scannable, and
// truncates it so a long menu does not dominate the terminal.
func oneLine(s string) string {
	const limit = 60

	s = strings.ReplaceAll(s, "\n", " / ")
	if len(s) > limit {
		return s[:limit] + "…"
	}
	return s
}

func str(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// supportsColor reports whether w is a colour-capable terminal.
func supportsColor(w io.Writer) bool { return qr.SupportsColor(w) }

// discardLogger is used when the dashboard owns the terminal: interleaving
// structured log lines with the live session view makes both unreadable.
func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}
