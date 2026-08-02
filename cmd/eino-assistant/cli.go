package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"eino-local-assistant/internal/config"
	"eino-local-assistant/internal/store"

	"github.com/spf13/cobra"
)

// version is overridden at link time with -ldflags "-X main.version=...".
var version = "dev"

const appName = "eino-assistant"

// rootOptions holds flags shared by all subcommands.
type rootOptions struct {
	configPath string
}

func newRootCommand() *cobra.Command {
	opts := &rootOptions{}

	root := &cobra.Command{
		Use:   appName + " [command]",
		Short: "Eino local coding assistant",
		Long:  "Eino local coding assistant — interactive TUI chat with ReAct tools and session persistence.",
		Example: fmt.Sprintf(
			"  %[1]s\n  %[1]s chat --config config.yml\n  %[1]s chat --title \"debug flaky test\"\n  %[1]s resume 20260715-120000-abc123\n  %[1]s sessions\n  %[1]s version",
			appName,
		),
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		// Bare invocation starts a new chat session.
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTUI(opts.configPath, sessionStart{}, cmd.ErrOrStderr())
		},
	}

	root.PersistentFlags().StringVar(&opts.configPath, "config", "config.yml", "path to the YAML configuration file")

	root.AddCommand(
		newChatCommand(opts),
		newResumeCommand(opts),
		newSessionsCommand(opts),
		newVersionCommand(),
	)

	root.SetOut(os.Stdout)
	root.SetErr(os.Stderr)

	return root
}

func newChatCommand(opts *rootOptions) *cobra.Command {
	var title string
	cmd := &cobra.Command{
		Use:     "chat",
		Aliases: []string{"new"},
		Short:   "Start a new interactive chat session (default)",
		Long:    "Start a new interactive chat session in the TUI.\nRequires an interactive terminal (stdin and stdout must be a TTY).",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTUI(opts.configPath, sessionStart{title: strings.TrimSpace(title)}, cmd.ErrOrStderr())
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "optional title for the new session")
	return cmd
}

func newResumeCommand(opts *rootOptions) *cobra.Command {
	var recoverInterrupted bool
	cmd := &cobra.Command{
		Use:   "resume <session-id>",
		Short: "Resume a saved session in the TUI",
		Long:  "Resume a previously saved session and open it in the TUI.\nRequires an interactive terminal (stdin and stdout must be a TTY).\n\nList ids with:\n  " + appName + " sessions",
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.ExactArgs(1)(cmd, args); err != nil {
				return err
			}
			if strings.TrimSpace(args[0]) == "" {
				return errors.New("session id is required")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTUI(opts.configPath, sessionStart{resumeID: strings.TrimSpace(args[0]), recoverInterrupted: recoverInterrupted}, cmd.ErrOrStderr())
		},
	}
	cmd.Flags().BoolVar(&recoverInterrupted, "recover", false, "explicitly terminate an interrupted active turn before resuming")
	return cmd
}

func newSessionsCommand(opts *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:     "sessions",
		Aliases: []string{"ls"},
		Short:   "List saved sessions",
		Long:    "List saved sessions (most recent first). Does not require a TTY.",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return listSessions(opts.configPath, cmd.OutOrStdout())
		},
	}
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", appName, version)
			return nil
		},
	}
}

func listSessions(configPath string, stdout io.Writer) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	dataDir, err := cfg.Storage.ResolveDataDir()
	if err != nil {
		return err
	}
	sessionStore, err := store.NewThreadStore(dataDir)
	if err != nil {
		return fmt.Errorf("open session store: %w", err)
	}

	list, err := sessionStore.ListThreads(context.Background())
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}
	if len(list) == 0 {
		fmt.Fprintln(stdout, "no saved sessions")
		return nil
	}

	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tTITLE\tMSGS\tTOKENS\tCOST\tUPDATED")
	for _, meta := range list {
		title := meta.Title
		if title == "" {
			title = "(untitled)"
		}
		cost := fmt.Sprintf("$%.4f", meta.CostUSD)
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%s\t%s\n",
			meta.ID,
			title,
			meta.MessageCount,
			meta.TotalTokens,
			cost,
			meta.UpdatedAt.Local().Format(time.RFC3339),
		)
	}
	return tw.Flush()
}

// execute runs the cobra command tree with the given args (excluding program name).
func execute(args []string) error {
	root := newRootCommand()
	root.SetArgs(args)
	return root.Execute()
}
