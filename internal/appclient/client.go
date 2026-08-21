package appclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/yeboahd24/ussd-lab/internal/protocol"
)

// DefaultTimeout bounds a single application call.
//
// Real USSD gateways abandon a request in the tens of seconds; a laptop
// application that has not answered in ten is almost certainly stuck, and a
// fast failure is more useful to a developer than a long hang.
const DefaultTimeout = 10 * time.Second

// Options configures an HTTPClient.
type Options struct {
	// CallbackURL is the ONLY endpoint this client will contact. It comes from
	// validated project configuration and is never supplied by the browser
	// (MVP design §23).
	CallbackURL string

	// Timeout bounds each call. Zero selects DefaultTimeout.
	Timeout time.Duration

	// Latency artificially delays each call, for error simulation
	// (MVP design §21, `ussd dev --latency`).
	Latency time.Duration

	// Transport allows tests to inject a stub. Zero uses a tuned default.
	Transport http.RoundTripper
}

// HTTPClient is the production ApplicationClient.
type HTTPClient struct {
	callbackURL string
	latency     time.Duration
	http        *http.Client
}

// Compile-time proof that HTTPClient satisfies the interface the session
// engine will depend on.
var _ protocol.ApplicationClient = (*HTTPClient)(nil)

// errRedirectBlocked marks a refused redirect so Send can classify it.
var errRedirectBlocked = errors.New("redirect blocked")

// New builds an HTTPClient.
func New(opts Options) (*HTTPClient, error) {
	if opts.CallbackURL == "" {
		return nil, fmt.Errorf("appclient: CallbackURL is required")
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	transport := opts.Transport
	if transport == nil {
		transport = &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   2 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:          10,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   5 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		}
	}

	return &HTTPClient{
		callbackURL: opts.CallbackURL,
		latency:     opts.Latency,
		http: &http.Client{
			Timeout:   timeout,
			Transport: transport,
			// Refuse redirects outright. Following one would let the
			// developer's application redirect USSD traffic -- including user
			// input -- to a host that was never configured.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errRedirectBlocked
			},
		},
	}, nil
}

// Send delivers req to the developer's application and parses the reply.
func (c *HTTPClient) Send(ctx context.Context, req *protocol.USSDRequest) (*protocol.USSDResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, &Error{
			Code:    CodeInvalidRequest,
			Message: "refusing to send an invalid request",
			Cause:   err,
		}
	}

	if c.latency > 0 {
		select {
		case <-time.After(c.latency):
		case <-ctx.Done():
			return nil, c.classify(ctx, ctx.Err())
		}
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, &Error{
			Code:    CodeInvalidRequest,
			Message: "could not encode the request",
			Cause:   err,
		}
	}

	httpReq, err := http.NewRequestWithContext(
		ctx, http.MethodPost, c.callbackURL, bytes.NewReader(body))
	if err != nil {
		return nil, &Error{
			Code:    CodeInvalidRequest,
			Message: "could not build the HTTP request",
			Cause:   err,
		}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/plain")
	httpReq.Header.Set("User-Agent", "ussd-lab")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, c.classify(ctx, err)
	}
	defer func() {
		// Drain a bounded amount so the connection can be reused, then close.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, protocol.MaxResponseBytes))
		_ = resp.Body.Close()
	}()

	// Read one byte beyond the limit so an oversized body is detected rather
	// than silently truncated into a response that looks valid.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxResponseBytes+1))
	if err != nil {
		return nil, c.classify(ctx, err)
	}

	if len(raw) > protocol.MaxResponseBytes {
		return nil, &Error{
			Code: CodeTooLarge,
			Message: fmt.Sprintf(
				"application response exceeded %d bytes", protocol.MaxResponseBytes),
			Hint: "USSD screens are short; return a paged menu instead",
		}
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &Error{
			Code: CodeHTTPStatus,
			Message: fmt.Sprintf("application returned HTTP %d %s",
				resp.StatusCode, http.StatusText(resp.StatusCode)),
			Hint: fmt.Sprintf("check the handler at %s and its server logs", c.callbackURL),
		}
	}

	parsed, err := protocol.ParseResponse(raw)
	if err != nil {
		return nil, &Error{
			Code:    CodeMalformed,
			Message: "application response could not be parsed",
			Hint:    `respond with "CON <text>" to continue or "END <text>" to finish`,
			Cause:   err,
		}
	}

	return &parsed, nil
}

// classify maps a transport failure onto a developer-actionable cause.
//
// The distinction that matters most is unavailable (nothing is listening)
// versus timeout (something is listening but is not answering) -- they lead to
// completely different debugging steps.
func (c *HTTPClient) classify(ctx context.Context, err error) *Error {
	if errors.Is(err, errRedirectBlocked) {
		return &Error{
			Code:    CodeRedirectBlocked,
			Message: "application attempted an HTTP redirect, which is not followed",
			Hint:    "return the USSD response directly instead of redirecting",
			Cause:   err,
		}
	}

	// A cancelled parent context is a timeout from the caller's perspective.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return c.timeoutError(err)
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return c.timeoutError(err)
	}

	// http.Client.Timeout surfaces as a *url.Error whose Timeout() is true;
	// the net.Error branch above catches it. Anything else that failed to
	// establish a connection is treated as unavailable.
	return &Error{
		Code:    CodeUnavailable,
		Message: fmt.Sprintf("could not reach the application at %s", c.callbackURL),
		Hint:    "is your application running, and is the callback URL in ussd.yaml correct?",
		Cause:   err,
	}
}

func (c *HTTPClient) timeoutError(cause error) *Error {
	return &Error{
		Code:    CodeTimeout,
		Message: fmt.Sprintf("application at %s did not respond in time", c.callbackURL),
		Hint:    "the handler may be blocked; USSD sessions expire quickly",
		Cause:   cause,
	}
}
