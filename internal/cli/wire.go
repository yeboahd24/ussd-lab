package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/yeboahd24/ussd-lab/internal/appclient"
	"github.com/yeboahd24/ussd-lab/internal/config"
	"github.com/yeboahd24/ussd-lab/internal/session"
	"github.com/yeboahd24/ussd-lab/internal/storage/memory"
	"github.com/yeboahd24/ussd-lab/internal/storage/sqlite"
)

// engineOptions are the knobs both `ussd dev` and `ussd test` expose.
type engineOptions struct {
	store   string
	dbPath  string
	latency time.Duration
	events  session.EventSink
}

// buildEngine wires a session engine from project configuration.
//
// `ussd dev` and `ussd test` share this deliberately. MVP design §19 requires
// the test runner and the interactive simulator to use the same engine; sharing
// the construction path is how that stays true as options are added, rather
// than depending on two call sites being kept in step by hand.
func buildEngine(
	ctx context.Context,
	cfg *config.Config,
	opts engineOptions,
) (*session.Engine, func(), error) {
	store, cleanup, err := openStore(ctx, opts.store, opts.dbPath)
	if err != nil {
		return nil, nil, err
	}

	app, err := appclient.New(appclient.Options{
		CallbackURL: cfg.Application.Callback,
		Latency:     opts.latency,
	})
	if err != nil {
		cleanup()
		return nil, nil, err
	}

	events := opts.events
	if events == nil {
		events = session.NopSink{}
	}

	engine, err := session.New(session.Options{
		Store:   store,
		App:     app,
		Events:  events,
		Timeout: time.Duration(cfg.USSD.SessionTimeout) * time.Second,
		Logger:  discardLogger(),
	})
	if err != nil {
		cleanup()
		return nil, nil, err
	}

	return engine, cleanup, nil
}

// openStore builds the named session store.
//
// Both implementations pass the same conformance suite, so this is a genuine
// swap rather than a special case (ADR-003).
func openStore(ctx context.Context, name, dbPath string) (session.SessionStore, func(), error) {
	switch name {
	case "", "memory":
		return memory.New(), func() {}, nil

	case "sqlite":
		st, err := sqlite.Open(ctx, dbPath)
		if err != nil {
			return nil, nil, err
		}
		return st, func() { _ = st.Close() }, nil

	default:
		return nil, nil, fmt.Errorf("unknown --store %q: use memory or sqlite", name)
	}
}
