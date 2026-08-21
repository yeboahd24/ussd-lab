# USSD Lab

Build and test USSD applications on your laptop. No shortcode, no aggregator,
no provider sandbox.

```
      Your laptop                          Your phone
 ┌────────────────────┐                ┌──────────────┐
 │  ussd dev          │                │              │
 │    simulator :7345 │◀──── Wi-Fi ───▶│  USSD screen │
 │    session engine  │                │              │
 └─────────┬──────────┘                └──────────────┘
           │
           ▼
   your app on :8000
```

Scan a QR code, dial `*124#` on your phone, and your local application answers.
The phone can be in **airplane mode with Wi-Fi on** — no cellular network is
involved.

---

## Quickstart

```bash
# 1. Build
make build

# 2. Start an application (or use the example)
go run ./examples/simple-bank &

# 3. Create a project
mkdir mybank && cd mybank
ussd init

# 4. Start the simulator
ussd dev
```

```
USSD Lab

Project:       mybank
Service code:  *124#
Callback:      http://localhost:8000/ussd
Store:         memory

Simulator:     http://192.168.1.20:7345/s/aIbI5tBCABrpP9I7PBxy…

        ▄▄▄▄▄▄▄  ▄  ▄▄ ▄▄▄▄▄▄▄
        █ ▄▄▄ █ ▀█▄▀▄█ █ ▄▄▄ █
        █ ███ █ ▄ ▀▄▄▄ █ ███ █
        ▀▀▀▀▀▀▀ ▀ ▀ ▀▄ ▀▀▀▀▀▀▀
                  …

Scan the QR code with your phone on the same Wi-Fi network.
The phone may stay in airplane mode as long as Wi-Fi is on.

Waiting for a session…  (Ctrl-C to stop)
```

Scan it, dial `*124#`, and the conversation appears live in your terminal:

```
── sess_ivr2smsxoeb3sqiw ───────────────────────────
11:38:13  START *124#
11:38:13  APP  → CON Welcome to MyBank / 1. Send Money / 2. Check Balance
11:38:13  USER → 1
11:38:13  APP  → CON Enter recipient number:
11:38:14  USER → 0241234567
11:38:14  APP  → CON Enter amount:
11:38:14  USER → 250
11:38:14  APP  → END Transaction successful.
11:38:14  SESSION COMPLETED
```

No phone to hand? Everything above also works over `curl`, and
[`ussd test`](#ussd-test) needs no phone at all.

---

## What your application has to do

One HTTP endpoint. JSON in, plain text out. **No SDK.**

```python
parts = text.split("*") if text else []

if not parts:           return "CON Welcome\n1. Send Money\n2. Balance"
if parts[0] == "2":     return "END Your balance is GHS 1,000"
...
```

`text` accumulates everything typed so far (`"1*0241234567*250"`), so your
application stays **stateless** — no session storage of your own.

A worked Go application, with tests, is in `examples/simple-bank/` — it
imports nothing from USSD Lab, which is checked by a test.

---

## Commands

### `ussd init`

Creates `ussd.yaml`, a `README.md` and a starter test. The project name comes
from the directory.

```bash
ussd init --callback http://localhost:9000/ussd --service-code '*789#'
```

### `ussd dev`

Starts the simulator on your LAN and prints a QR code.

| Flag | Purpose |
|---|---|
| `--host` | Override the advertised address if detection picks the wrong interface |
| `--port` | Override the port from `ussd.yaml` |
| `--latency 500ms` | Delay every application call, to simulate a slow network |
| `--store sqlite` | Persist live session state as well as history |
| `--redact-input` | Replace recorded input with `[REDACTED]` — use when testing PIN entry |
| `--no-history` | Do not record anything to disk |
| `--no-qr` | Print the URL without a QR code |

### `ussd test`

Runs declarative tests from `tests/*.yaml` against your application. **No phone,
no browser, no port** — and the same session engine the simulator uses, so a
green suite means what a phone would.

```yaml
name: Send Money
steps:
  - input: "1"
    expect:
      - contains: "Enter recipient number:"
  - input: "0241234567"
  - input: "250"
  - input: "1"
assertions:
  - type: END
    contains: "Transaction successful."
    status: COMPLETED
```

```
✓ Cancel Transfer                  4ms
✓ Check Balance                    520µs
✓ Send Money                       400µs

3 passed  6ms
```

Failures print the whole transcript and exit non-zero, so it drops into CI.

Assertions: `contains`, `not_contains`, `equals`, `matches` (regex), `type`
(`CON`/`END`), `status`.

### `ussd logs`

```
SESSION                CODE     STATUS     PHONE          INPUTS  STARTED
sess_y5ewxpqjgjuqtwyg  *124#    CANCELLED  233240000001        0  just now
sess_d4r5jbjyhjxbbt4t  *124#    COMPLETED  233240000001        1  2m ago
```

`ussd logs <session-id>` replays the full transcript, identical to the live
view.

---

## Configuration

```yaml
project: mybank

application:
  callback: http://localhost:8000/ussd   # the ONLY URL the simulator contacts

ussd:
  service_code: "*124#"
  session_timeout: 120                   # seconds of think-time per screen

simulator:
  port: 7345
  host: ""                               # blank = auto-detect the LAN address
```

Configuration is validated on load: a `file://` callback, a relative URL,
embedded credentials or a malformed short code are rejected before anything
runs, and every problem is reported at once.

---

## The phone cannot really dial `*124#`

It cannot, and USSD Lab does not pretend otherwise. **USSD dialling is a
cellular network function** — there is no way to route a real `*124#` from a
handset to your laptop, and any tool claiming to do so is doing something else.

What USSD Lab reproduces is the **protocol and the developer experience**: the
session model, the accumulating input, `CON`/`END`, timeouts, and the exact
request your application will receive in production. The phone runs a browser-
based USSD screen instead of the native dialler.

Putting the phone in **airplane mode + Wi-Fi** is a useful habit: it guarantees
the test is nowhere near your operator's real USSD system.

---

## How it is put together

```
CLI  ─────▶  Simulator  ─────▶  Session engine  ─────▶  Your application
(transport)  (transport)        (owns sessions)         (owns business logic)
```

The rule that shapes everything: **the simulator is a transport, not the USSD
business logic.** The session engine has no HTTP and no SQL; the protocol
package imports nothing at all. Those are compiler-enforced, not conventions:

```bash
go list -f '{{join .Deps "\n"}}' ./internal/protocol | grep ussd-lab   # empty
go list -f '{{join .Deps "\n"}}' ./internal/session  | grep net/http   # empty
```

That is what will let real provider adapters slot in later without rewriting
the core — the simulator is simply the first transport.

The decisions behind that shape:

| # | Decision |
|---|---|
| 1 | Modular monolith, boundaries enforced by the import graph |
| 2 | Normalized protocol; input accumulates as `1*0241234567*250` |
| 3 | Two session stores behind one interface, validated by a shared conformance suite |
| 4 | Bind `0.0.0.0`, score interfaces rather than first-match, QR carries an attach token |
| 5 | Build the provider-adapter seam, defer the interface until a real provider exists |
| 6 | Session history derived from the event log, not stored separately |

---

## Development

```bash
make build    # build ./ussd
make test     # go test ./... -race
make lint     # go vet + gofmt
```

Four direct dependencies: `cobra`, `yaml.v3`, `modernc.org/sqlite` (pure Go, so
cross-compilation needs no C toolchain), `skip2/go-qrcode`. The phone UI is
hand-written HTML/CSS/JS embedded with `go:embed` — no Node, no bundler, no
build step.

---

## Security

The simulator binds `0.0.0.0` so your phone can reach it, which means anything
on the same Wi-Fi can too. Access is controlled by an unguessable attach token
carried in the QR code.

**If you test PIN entry, use `ussd dev --redact-input`.** By default the event
log records what the user typed, because that is what makes the debugger
useful — and USSD Lab cannot tell a PIN from a menu choice. History files are
created `0600` and `.ussd/` is in `.gitignore`, but the default is unredacted.

Two findings are known and open: the attach token's lifetime does not slide
with an active session, so a run past an hour starts rejecting the attached
phone; and there is no rate limiting.

---

## Status

MVP. All 18 acceptance criteria from the design document pass.

Three of them are verified structurally rather than physically, and should not
be read as stronger than they are: the QR encodes the correct LAN address but
was never scanned with a real handset; the browser UI's server-side contract is
tested but it was never driven in a real browser; and offline operation is
verified by asserting no asset references an external host, not by running on an
air-gapped machine.

Not built yet, by design: provider adapters, cloud tunnels, dashboards,
accounts, rate limiting.
