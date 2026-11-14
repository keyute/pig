package cmd

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"

	"github.com/keyute/kopi/internal/pigruntime"
)

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the Kopi and PiG versions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			kopi := "dev"
			if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
				kopi = info.Main.Version
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "kopi %s\npig %s\n", kopi, pigruntime.Version())
			return err
		},
	}
}
