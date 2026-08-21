// Command simple-bank is an example USSD application.
//
// It exists to demonstrate the protocol, not to be a financial system: there is
// no persistence, no authentication and no real money. See the note on
// financial safety in docs/ussd-lab-provider-adapter-design.md §50.
//
// Note what this file does NOT import: nothing from USSD Lab. A USSD
// application needs no SDK. Everything here is the Go standard library, and the
// same 60 lines of logic would be as short in Python, PHP, Node or Java.
//
//	go run ./examples/simple-bank
//	ussd dev
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// request is the payload USSD Lab posts. Only the fields this application
// actually uses are declared; unknown fields are ignored, so the protocol can
// gain fields without breaking existing applications.
type request struct {
	SessionID   string `json:"session_id"`
	ServiceCode string `json:"service_code"`
	PhoneNumber string `json:"phone_number"`

	// Text accumulates every input so far, joined by "*". It is empty on the
	// first request. Because it carries the whole history, this application
	// needs no session storage of its own -- it works out where the user is
	// from Text alone.
	Text string `json:"text"`
}

const demoBalance = "GHS 1,000"

func main() {
	addr := flag.String("addr", "127.0.0.1:8000", "address to listen on")
	flag.Parse()

	mux := http.NewServeMux()
	mux.Handle("POST /ussd", newHandler())

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("simple-bank: listen on %s: %v", *addr, err)
	}

	// Printing the resolved address lets a caller pass ":0" and still find the
	// port -- which is what the end-to-end test does.
	fmt.Printf("simple-bank listening on %s\n", ln.Addr().String())
	os.Stdout.Sync()

	log.Fatal(http.Serve(ln, mux))
}

func newHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}

		// Plain text is the whole response format. No SDK, no JSON encoder.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, route(inputs(req.Text)))
	})
}

// inputs splits the accumulated text into the individual entries.
func inputs(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(text, "*")
}

// route maps the input history to a screen.
//
// The whole application is a function from "what the user has typed" to "what
// to show next". That is what makes a USSD application testable: no server, no
// session, just a pure function.
func route(in []string) string {
	if len(in) == 0 {
		return con("Welcome to MyBank",
			"1. Send Money",
			"2. Check Balance",
			"3. Exit")
	}

	switch in[0] {
	case "1":
		return sendMoney(in[1:])
	case "2":
		return end("Your balance is " + demoBalance)
	case "3":
		return end("Goodbye.")
	default:
		return end("Invalid choice. Please dial again.")
	}
}

// sendMoney walks the transfer flow. steps holds the entries after the "1".
func sendMoney(steps []string) string {
	switch len(steps) {
	case 0:
		return con("Enter recipient number:")

	case 1:
		if !validPhone(steps[0]) {
			// Ending here rather than re-prompting keeps the example short.
			// A real application would return CON and ask again.
			return end("That does not look like a phone number.")
		}
		return con("Enter amount:")

	case 2:
		amount, ok := parseAmount(steps[1])
		if !ok {
			return end("That is not a valid amount.")
		}
		return con(
			fmt.Sprintf("Send GHS %s to %s?", amount, steps[0]),
			"1. Confirm",
			"2. Cancel")

	default:
		if steps[2] == "1" {
			// A real application would perform the transfer here, and would
			// need idempotency: USSD requests can be delivered more than once.
			return end("Transaction successful.")
		}
		return end("Transaction cancelled.")
	}
}

func validPhone(s string) bool {
	if len(s) < 9 || len(s) > 15 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// parseAmount accepts a positive whole number of cedis.
func parseAmount(s string) (string, bool) {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return "", false
	}
	return strconv.Itoa(n), true
}

// con builds a screen that continues the session.
func con(lines ...string) string {
	return "CON " + strings.Join(lines, "\n")
}

// end builds a screen that terminates the session.
func end(text string) string {
	return "END " + text
}
