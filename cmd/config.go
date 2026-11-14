package cmd

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/keyute/kopi/internal/config"
)

// configCmd never builds the PiG runtime or creates anything: it only reads
// the config file.
func configCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "config",
		Short: "Print the Kopi home, config file and effective configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := config.ResolvePaths()
			if err != nil {
				return err
			}
			resolved, err := resolveConfig(cmd, paths)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if _, err := fmt.Fprintf(out, "home: %s\nconfig file: %s\n\n", paths.Home, paths.ConfigFile); err != nil {
				return err
			}
			tw := tabwriter.NewWriter(out, 0, 0, 1, ' ', 0)
			for _, e := range resolved.Entries() {
				fmt.Fprintf(tw, "%s\t= %s\t(%s)\n", e.Key, e.Value, e.Origin)
			}
			return tw.Flush()
		},
	}
}
