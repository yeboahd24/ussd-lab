package cli

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

func newVersionCmd(env Env) *cobra.Command {
	var short bool

	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print the USSD Lab version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()

			if short {
				fmt.Fprintln(out, env.Build.Version)
				return nil
			}

			fmt.Fprintf(out, "ussd %s\n", env.Build.Version)
			fmt.Fprintf(out, "  commit:  %s\n", env.Build.Commit)
			fmt.Fprintf(out, "  built:   %s\n", env.Build.Date)
			fmt.Fprintf(out, "  go:      %s\n", runtime.Version())
			fmt.Fprintf(out, "  os/arch: %s/%s\n", runtime.GOOS, runtime.GOARCH)
			return nil
		},
	}

	cmd.Flags().BoolVar(&short, "short", false, "print only the version number")
	return cmd
}
