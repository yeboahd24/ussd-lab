package qr

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestRender_ProducesOutput(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	if err := Render(&buf, "http://192.168.1.20:7345/s/abc123", Options{Color: true}); err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	out := buf.String()
	if out == "" {
		t.Fatal("Render() wrote nothing")
	}
	if !strings.Contains(out, upperHalf) {
		t.Error("output contains no block characters")
	}
	if !strings.Contains(out, reset) {
		t.Error("colour output does not reset; the terminal would stay coloured")
	}
}

func TestRender_Monochrome(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	if err := Render(&buf, "http://192.168.1.20:7345", Options{Color: false}); err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	if strings.Contains(buf.String(), "\x1b[") {
		t.Error("monochrome output contains ANSI escapes")
	}
}

// Both forms must be roughly square, or the code will not scan.
func TestRender_Proportions(t *testing.T) {
	t.Parallel()

	for _, color := range []bool{true, false} {
		var buf bytes.Buffer
		if err := Render(&buf, "http://192.168.1.20:7345/s/abc", Options{Color: color}); err != nil {
			t.Fatalf("Render() error = %v", err)
		}

		lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
		if len(lines) < 10 {
			t.Errorf("color=%v: %d rows, too small to be a QR code", color, len(lines))
		}

		// Half-block packing means the module grid is twice the row count.
		width := len([]rune(stripANSI(lines[0])))
		modules := len(lines) * 2
		if width < modules-2 || width > modules+2 {
			t.Errorf("color=%v: %d columns vs ~%d module rows; not square", color, width, modules)
		}
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	inEscape := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			inEscape = true
		case inEscape && r == 'm':
			inEscape = false
		case !inEscape:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// A long URL must still encode; go-qrcode picks a bigger version automatically.
func TestRender_LongURL(t *testing.T) {
	t.Parallel()

	long := "http://192.168.100.200:7345/s/" + strings.Repeat("a", 200)

	var buf bytes.Buffer
	if err := Render(&buf, long, Options{Color: true}); err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if buf.Len() == 0 {
		t.Error("no output for a long URL")
	}
}

func TestRender_EmptyContent(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	// Encoding an empty string is valid; the point is that it must not panic.
	if err := Render(&buf, "", Options{Color: true}); err != nil {
		t.Logf("Render(\"\") error = %v (acceptable)", err)
	}
}

func TestSupportsColor_HonoursNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	if SupportsColor(os.Stdout) {
		t.Error("SupportsColor() = true with NO_COLOR set")
	}
}

func TestSupportsColor_HonoursDumbTerm(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "dumb")

	if SupportsColor(os.Stdout) {
		t.Error("SupportsColor() = true with TERM=dumb")
	}
}

// A redirected stream must not receive escape sequences.
func TestSupportsColor_NonTerminal(t *testing.T) {
	t.Parallel()

	if SupportsColor(&bytes.Buffer{}) {
		t.Error("SupportsColor() = true for a non-file writer")
	}
}
