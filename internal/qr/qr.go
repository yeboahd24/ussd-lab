// Package qr renders QR codes for the terminal.
//
// The content is a URL and nothing else. It carries no secrets beyond a
// short-lived attach token, no phone numbers and no project internals
// (MVP design §7).
package qr

import (
	"fmt"
	"io"
	"os"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// ANSI colour codes. Explicit foreground AND background are set for every cell
// so the code scans on light and dark terminal themes alike -- relying on the
// terminal's default background is the usual reason terminal QR codes fail to
// scan for half of users.
const (
	reset   = "\x1b[0m"
	fgBlack = "\x1b[30m"
	fgWhite = "\x1b[97m"
	bgBlack = "\x1b[40m"
	bgWhite = "\x1b[107m"
)

// Two vertically adjacent modules are drawn in one character cell using the
// upper-half block, halving the height so a QR code fits an ordinary terminal.
const upperHalf = "▀"

// Options configures rendering.
type Options struct {
	// Color renders with ANSI colours. When false, a monochrome block form is
	// used, which needs a dark terminal background to scan.
	Color bool
}

// Render writes a scannable QR code for content.
func Render(w io.Writer, content string, opts Options) error {
	code, err := qrcode.New(content, qrcode.Medium)
	if err != nil {
		return fmt.Errorf("qr: encode %q: %w", content, err)
	}

	// Bitmap includes the mandatory quiet zone; true means a dark module.
	bitmap := code.Bitmap()

	var b strings.Builder
	for y := 0; y < len(bitmap); y += 2 {
		if opts.Color {
			renderColorRow(&b, bitmap, y)
		} else {
			renderMonoRow(&b, bitmap, y)
		}
		b.WriteByte('\n')
	}

	_, err = io.WriteString(w, b.String())
	return err
}

func renderColorRow(b *strings.Builder, bitmap [][]bool, y int) {
	for x := 0; x < len(bitmap[y]); x++ {
		top := bitmap[y][x]
		bottom := y+1 < len(bitmap) && bitmap[y+1][x]

		// Foreground paints the upper module, background the lower one.
		if top {
			b.WriteString(fgBlack)
		} else {
			b.WriteString(fgWhite)
		}
		if bottom {
			b.WriteString(bgBlack)
		} else {
			b.WriteString(bgWhite)
		}
		b.WriteString(upperHalf)
	}
	b.WriteString(reset)
}

// renderMonoRow draws light modules as filled blocks, which scans on a dark
// terminal. It is the fallback when colour is unavailable.
func renderMonoRow(b *strings.Builder, bitmap [][]bool, y int) {
	for x := 0; x < len(bitmap[y]); x++ {
		top := bitmap[y][x]
		bottom := y+1 < len(bitmap) && bitmap[y+1][x]

		switch {
		case top && bottom:
			b.WriteString(" ")
		case top && !bottom:
			b.WriteString("▄") // lower half
		case !top && bottom:
			b.WriteString("▀") // upper half
		default:
			b.WriteString("█") // full block
		}
	}
}

// SupportsColor reports whether w looks like a colour-capable terminal.
//
// NO_COLOR is honoured (https://no-color.org), and a redirected stream gets the
// monochrome form so a piped log file is not full of escape sequences.
func SupportsColor(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}

	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
