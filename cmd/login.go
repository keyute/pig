package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/keyute/kopi/internal/config"
)

func loginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login [provider]",
		Short: "Log in to a model provider, or list the providers that offer login",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := config.ResolvePaths()
			if err != nil {
				return err
			}
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("failed to resolve working directory: %w", err)
			}
			rt, err := openRuntime(cmd.Context(), paths, cwd)
			if err != nil {
				return err
			}
			defer rt.Close()

			out := cmd.OutOrStdout()
			if len(args) == 0 {
				_, err := fmt.Fprintf(out, "Providers that offer login:\n  %s\n\nRun \"kopi login <provider>\".\n", strings.Join(rt.OAuthProviders(), "\n  "))
				return err
			}
			provider := args[0]
			if err := rt.Login(cmd.Context(), provider, cmd.InOrStdin(), out); err != nil {
				return err
			}
			_, err = fmt.Fprintf(out, "Logged in to %s. The credential is stored in %s.\n", provider, filepath.Join(paths.AgentDir, "auth.json"))
			return err
		},
	}
}
