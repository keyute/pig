package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"github.com/lmittmann/tint"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/keyute/kopi/internal/app"
	"github.com/keyute/kopi/internal/config"
	"github.com/keyute/kopi/internal/line"
	"github.com/keyute/kopi/internal/pigruntime"
	"github.com/keyute/kopi/internal/tool"
)

const joinTimeout = 5 * time.Second

func Execute() {
	if err := newRoot().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "kopi: %v\n", err)
		os.Exit(1)
	}
}

// newRoot builds the whole command tree; tests execute it in-process.
func newRoot() *cobra.Command {
	root := rootCmd()
	root.AddCommand(versionCmd())
	root.AddCommand(configCmd())
	root.AddCommand(loginCmd())
	return root
}

func rootCmd() *cobra.Command {
	var resumeID string
	var cont bool
	cmd := &cobra.Command{
		Use:           "kopi",
		Short:         "Kopi coding agent",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return setupLogging(cmd)
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInteractive(cmd, app.OpenRequest{ResumeID: resumeID, Continue: cont})
		},
	}
	cmd.PersistentFlags().String("model", "", "model as provider/id (overrides the config file)")
	cmd.PersistentFlags().String("log-level", "warn", "log level: debug, info, warn or error")
	cmd.Flags().BoolVarP(&cont, "continue", "c", false, "continue the most recent session in this directory")
	cmd.Flags().StringVarP(&resumeID, "resume", "r", "", "resume the session with this id")
	cmd.MarkFlagsMutuallyExclusive("continue", "resume")
	return cmd
}

func setupLogging(cmd *cobra.Command) error {
	name, err := cmd.Flags().GetString("log-level")
	if err != nil {
		return err
	}
	levels := map[string]slog.Level{"debug": slog.LevelDebug, "info": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError}
	level, ok := levels[name]
	if !ok {
		return fmt.Errorf("invalid --log-level %q: want debug, info, warn or error", name)
	}
	stderr := cmd.ErrOrStderr()
	slog.SetDefault(slog.New(tint.NewTextHandler(stderr, &tint.Options{Level: level, NoColor: !isTerminal(stderr)})))
	return nil
}

func isTerminal(v any) bool {
	f, ok := v.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// resolveConfig layers defaults, the global file and the flags the user set.
func resolveConfig(cmd *cobra.Command, paths config.Paths) (*config.Resolved, error) {
	global, err := config.GlobalFile(paths.ConfigFile)
	if err != nil {
		return nil, err
	}
	flags := map[string]any{}
	if cmd.Flags().Changed("model") {
		model, err := cmd.Flags().GetString("model")
		if err != nil {
			return nil, err
		}
		flags["model"] = model
	}
	return config.Resolve(config.Defaults(), global, config.Map("flags", flags))
}

// openRuntime pins PiG's environment, creates the agent dir and builds the
// runtime for cwd.
func openRuntime(ctx context.Context, paths config.Paths, cwd string) (*pigruntime.Runtime, error) {
	// PiG's session lookup still consults its default dirs even with an
	// explicit agent dir, so they are pointed into Kopi home process-wide.
	set, unset := pigruntime.IsolationEnv(paths.Home, paths.AgentDir)
	for k, v := range set {
		if err := os.Setenv(k, v); err != nil {
			return nil, fmt.Errorf("failed to set %s: %w", k, err)
		}
	}
	for _, k := range unset {
		if err := os.Unsetenv(k); err != nil {
			return nil, fmt.Errorf("failed to unset %s: %w", k, err)
		}
	}
	if err := os.MkdirAll(paths.AgentDir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create agent dir: %w", err)
	}
	return pigruntime.New(ctx, pigruntime.Options{AgentDir: paths.AgentDir, CWD: cwd})
}

func runInteractive(cmd *cobra.Command, req app.OpenRequest) (err error) {
	paths, err := config.ResolvePaths()
	if err != nil {
		return err
	}
	resolved, err := resolveConfig(cmd, paths)
	if err != nil {
		return err
	}
	model := resolved.Config.Model
	if model == "" {
		return config.ErrNoModel
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to resolve home directory: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to resolve working directory: %w", err)
	}

	ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGTERM)
	defer stop()

	// workRunning is set when Close leaves work running; see below.
	var workRunning bool
	rt, err := openRuntime(ctx, paths, cwd)
	if err != nil {
		return err
	}
	defer func() {
		if !workRunning {
			rt.Close()
		}
	}()
	ws, err := tool.NewWorkspace(cwd, tool.DefaultProtected(userHome, paths.Home))
	if err != nil {
		return err
	}
	defer func() {
		if !workRunning {
			ws.Close()
		}
	}()

	c := app.NewController(rt, app.Config{Model: model, Workspace: cwd, Tools: []tool.Tool{tool.NewRead(ws)}, JoinTimeout: joinTimeout})
	// Created before Open so the session line is printed.
	frontend := line.New(c, cmd.InOrStdin(), cmd.OutOrStdout(), isTerminal(cmd.InOrStdin()))
	// Only Open may be cancelled by SIGINT; ctx outlives it as the runtime's
	// signal.
	openCtx, stopOpen := signal.NotifyContext(ctx, os.Interrupt)
	if err := c.Open(openCtx, req); err != nil {
		stopOpen()
		return diagnose(err, model, resolved.Origins["model"], paths, req, rt.OAuthProviders())
	}
	// SIGINT is a channel rather than part of the context because it must
	// keep firing: each one interrupts the running turn, and only one at
	// idle exits. It is registered before stopOpen so SIGINT never reverts to
	// the default exit in between.
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	stopOpen()
	runErr := frontend.Run(ctx, interrupts)

	// Close must not inherit SIGTERM's cancellation; it bounds itself by
	// JoinTimeout.
	closeErr := c.Close(context.WithoutCancel(ctx))
	if errors.Is(closeErr, pigruntime.ErrJoinTimeout) {
		// Close left the session open under work that is still running, so
		// the runtime and workspace stay open too rather than being pulled
		// from under it; the process exits right after.
		workRunning = true
		closeErr = fmt.Errorf("running work did not stop before exit: %w", closeErr)
	}
	return errors.Join(runErr, closeErr)
}

// diagnose turns the open failures a user can fix into Kopi's own advice.
func diagnose(err error, model, origin string, paths config.Paths, req app.OpenRequest, loginProviders []string) error {
	switch {
	case errors.Is(err, pigruntime.ErrNoCredential):
		provider, _, _ := config.ParseModelRef(model)
		if slices.Contains(loginProviders, provider) {
			return fmt.Errorf("no credential for provider %q: run \"kopi login %s\"", provider, provider)
		}
		// PiG has no public accessor naming a provider's API-key variable.
		return fmt.Errorf("no credential for provider %q: Kopi has no sign-in flow for it; supply its credential the way PiG reads it, such as an API key in the environment", provider)
	case errors.Is(err, pigruntime.ErrUnknownModel):
		where := "--model"
		if origin != "flags" {
			where = fmt.Sprintf("\"model\" in %s", paths.ConfigFile)
		}
		return fmt.Errorf("unknown model %q: check %s", model, where)
	case errors.Is(err, pigruntime.ErrSessionNotFound) && req.Continue:
		return errors.New("no session to continue in this directory: start without --continue")
	case errors.Is(err, pigruntime.ErrSessionNotFound):
		return fmt.Errorf("no session %q in this directory: start kopi and run /resume to list sessions", req.ResumeID)
	}
	return err
}
