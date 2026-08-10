package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"eino-local-assistant/internal/config"
	"eino-local-assistant/internal/store"
	"eino-local-assistant/internal/usage"

	"github.com/spf13/cobra"
)

// version is overridden at link time with -ldflags "-X main.version=...".
var version = "dev"

const appName = "eino"

// rootOptions holds flags shared by all subcommands.
type rootOptions struct {
	configPath    string
	configPathErr error
	modelName     string
	yolo          bool
}

// commandDeps is immutable after construction. Tests create a fresh command
// tree with a local session factory instead of mutating global runtime hooks.
type commandDeps struct {
	exec        execCommandDeps
	interactive interactiveCommandRunner
}

type interactiveCommandRunner func(string, sessionStart, io.Writer) error

func newRootCommand() *cobra.Command {
	return newRootCommandWithDeps(commandDeps{exec: defaultExecCommandDeps()})
}

func newRootCommandWithDeps(deps commandDeps) *cobra.Command {
	configPath, configPathErr := config.UserConfigPath()
	opts := &rootOptions{configPath: configPath, configPathErr: configPathErr}
	if deps.interactive == nil {
		deps.interactive = runTUI
	}

	root := &cobra.Command{
		Use:   appName + " [command]",
		Short: "Eino local coding assistant",
		Long:  "Eino local coding assistant — interactive TUI chat and durable non-interactive execution with ReAct tools. Use -m/--model for a startup-only model override on interactive chat and resume. Use --yolo only when you explicitly accept host-side tool execution without approval prompts.",
		Example: fmt.Sprintf(
			"  %[1]s\n  %[1]s exec \"summarize this repository\"\n  %[1]s exec - < build.log\n  %[1]s resume 20260715-120000-abc123\n  %[1]s sessions\n  %[1]s mcp list\n  %[1]s version",
			appName,
		),
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		// Bare invocation starts a new chat session.
		RunE: func(cmd *cobra.Command, args []string) error {
			return deps.interactive(opts.configPath, sessionStart{modelName: opts.modelName, yolo: opts.yolo}, cmd.ErrOrStderr())
		},
	}
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if opts.yolo && cmd != cmd.Root() && (cmd.Parent() != cmd.Root() || (cmd.Name() != "chat" && cmd.Name() != "resume")) {
			return errors.New("--yolo is only supported for interactive chat/new/resume; headless and informational commands cannot use it")
		}
		if cmd.Name() == "version" {
			return nil
		}
		if opts.configPathErr != nil {
			return fmt.Errorf("resolve global configuration path: %w", opts.configPathErr)
		}
		return nil
	}

	root.PersistentFlags().BoolVar(&opts.yolo, "yolo", false, "DANGEROUS: interactive tools bypass approval prompts and the OS sandbox")
	root.Flags().StringVarP(&opts.modelName, "model", "m", "", "model name for this interactive session (startup override)")

	root.AddCommand(
		newChatCommand(opts, deps.interactive),
		newExecCommand(opts, deps.exec),
		newResumeCommand(opts, deps.interactive),
		newSessionsCommand(opts),
		newMCPCommand(opts),
		newVersionCommand(),
	)

	root.SetOut(os.Stdout)
	root.SetErr(os.Stderr)

	return root
}

func newChatCommand(opts *rootOptions, interactive interactiveCommandRunner) *cobra.Command {
	var title string
	var modelName string
	cmd := &cobra.Command{
		Use:     "chat",
		Aliases: []string{"new"},
		Short:   "Start a new interactive chat session (default)",
		Long:    "Start a new interactive chat session in the TUI.\nRequires an interactive terminal (stdin and stdout must be a TTY).\nUse -m/--model for a startup-only model override.",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return interactive(opts.configPath, sessionStart{title: strings.TrimSpace(title), modelName: modelName, yolo: opts.yolo}, cmd.ErrOrStderr())
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "optional title for the new session")
	cmd.Flags().StringVarP(&modelName, "model", "m", "", "model name for this interactive session (startup override)")
	return cmd
}

func newResumeCommand(opts *rootOptions, interactive interactiveCommandRunner) *cobra.Command {
	var recoverInterrupted bool
	var modelName string
	cmd := &cobra.Command{
		Use:   "resume <session-id>",
		Short: "Resume a saved session in the TUI",
		Long:  "Resume a previously saved session and open it in the TUI.\nRequires an interactive terminal (stdin and stdout must be a TTY).\nUse -m/--model for a startup-only model override; it does not rewrite the saved session.\n\nList ids with:\n  " + appName + " sessions",
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
			return interactive(opts.configPath, sessionStart{resumeID: strings.TrimSpace(args[0]), recoverInterrupted: recoverInterrupted, modelName: modelName, yolo: opts.yolo}, cmd.ErrOrStderr())
		},
	}
	cmd.Flags().BoolVar(&recoverInterrupted, "recover", false, "explicitly terminate an interrupted active turn or pending compaction before resuming")
	cmd.Flags().StringVarP(&modelName, "model", "m", "", "model name for this interactive session (startup override)")
	return cmd
}

func newSessionsCommand(opts *rootOptions) *cobra.Command {
	var outputFormat string
	cmd := &cobra.Command{
		Use:     "sessions",
		Aliases: []string{"ls"},
		Short:   "List saved sessions",
		Long:    "List saved sessions (most recent first). Does not require a TTY. Use --output-format json for machine-readable output.",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := normalizeSessionsOutputFormat(outputFormat)
			if err != nil {
				return err
			}
			return listSessionsWithFormat(opts.configPath, format, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&outputFormat, "output-format", sessionsOutputFormatText, "session list format: text or json")
	return cmd
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

const (
	sessionsOutputFormatText = "text"
	sessionsOutputFormatJSON = "json"
)

func normalizeSessionsOutputFormat(raw string) (string, error) {
	format := strings.ToLower(strings.TrimSpace(raw))
	if format == "" {
		return sessionsOutputFormatText, nil
	}
	if format != sessionsOutputFormatText && format != sessionsOutputFormatJSON {
		return "", fmt.Errorf("unsupported sessions output format %q (choose text or json)", raw)
	}
	return format, nil
}

func listSessions(configPath string, stdout io.Writer) error {
	return listSessionsWithFormat(configPath, sessionsOutputFormatText, stdout)
}

func listSessionsWithFormat(configPath, format string, stdout io.Writer) error {
	cfg, _, err := loadCommandConfig(configPath)
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
	if format == sessionsOutputFormatJSON {
		if err := json.NewEncoder(stdout).Encode(list); err != nil {
			return fmt.Errorf("write sessions JSON: %w", err)
		}
		return nil
	}
	if len(list) == 0 {
		fmt.Fprintln(stdout, "no saved sessions")
		return nil
	}

	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tTITLE\tMSGS\tAPI USAGE\tCONTEXT\tCOST~\tUPDATED")
	for _, meta := range list {
		title := meta.Title
		if title == "" {
			title = "(untitled)"
		}
		apiUsage := usage.FormatAPIUsage(usage.APIUsageFromMeta(meta))
		contextSnapshot := usage.FormatContextSnapshot(meta.LastContext)
		cost := usage.FormatCostEstimate(meta.CostUSD, meta.UsageStatus)
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\t%s\n",
			meta.ID,
			title,
			meta.MessageCount,
			apiUsage,
			contextSnapshot,
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
