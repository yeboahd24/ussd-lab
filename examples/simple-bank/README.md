# simple-bank

An example USSD application, used to demonstrate the USSD Lab protocol.

It implements:

```
*124#

1. Send Money      → recipient → amount → confirm → END
2. Check Balance   → END  Your balance is GHS 1,000
3. Exit            → END  Goodbye.
```

It is **not** a financial system. There is no persistence, no authentication
and no money. A real application must enforce idempotency, authorisation,
balance checks and an audit trail — USSD Lab only transports the interaction.

## Running it

```bash
go run ./examples/simple-bank            # listens on 127.0.0.1:8000
go run ./examples/simple-bank -addr :9000
```

Then, in a project whose `ussd.yaml` points at it:

```bash
ussd dev
```

## What to notice

**It imports nothing from USSD Lab.** The protocol needs no SDK — the whole
application is `encoding/json` plus `net/http`. The same logic is as short in
Python, PHP, Node or Java.

**It has no session storage.** `text` carries the full input history
(`"1*0241234567*100"`), so the application derives its position in the menu
from that string alone. This is why the core is a pure function:

```go
func route(in []string) string
```

which is also why `main_test.go` can test every menu path without starting a
server.

**Responses are plain text.** `CON <text>` shows a screen and waits;
`END <text>` shows a final screen and ends the session.
