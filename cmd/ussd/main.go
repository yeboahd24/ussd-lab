// Command ussd is the USSD Lab command line interface.
package main

import (
	"os"

	"github.com/yeboahd24/ussd-lab/internal/cli"
)

// Injected at link time by the Makefile.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	os.Exit(cli.Execute(cli.Env{
		Build: cli.BuildInfo{
			Version: version,
			Commit:  commit,
			Date:    date,
		},
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}))
}
