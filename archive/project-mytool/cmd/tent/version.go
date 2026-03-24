package main

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

// Build-time variables set via ldflags
var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func versionCmd() *cobra.Command {
	var short bool

	cmd := &cobra.Command{
		Use:   "version",
		Short: "Show tent version and build information",
		Long: `Display version, build commit, build date, Go version, and platform.

Examples:
  tent version
  tent version --short`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if short {
				fmt.Printf("tent %s\n", version)
				return nil
			}

			fmt.Printf("tent version %s\n", version)
			fmt.Printf("  commit:    %s\n", commit)
			fmt.Printf("  built:     %s\n", buildDate)
			fmt.Printf("  go:        %s\n", runtime.Version())
			fmt.Printf("  platform:  %s/%s\n", runtime.GOOS, runtime.GOARCH)
			return nil
		},
	}

	cmd.Flags().BoolVar(&short, "short", false, "Print only the version number")
	return cmd
}
