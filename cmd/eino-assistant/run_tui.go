package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"eino-local-assistant/internal/agent"
	"eino-local-assistant/internal/chat"
	"eino-local-assistant/internal/config"
	"eino-local-assistant/internal/contextbuild"
	"eino-local-assistant/internal/provider"
	"eino-local-assistant/internal/store"
	"eino-local-assistant/internal/tools"
	"eino-local-assistant/internal/tui"
	"eino-local-assistant/internal/usage"

	"golang.org/x/term"
)

// sessionStart selects how the TUI conversation is opened.
type sessionStart struct {
	title              string
	resumeID           string
	recoverInterrupted bool
}

func runTUI(configPath string, start sessionStart, stderr io.Writer) error {
	// Process lifetime: SIGTERM only. TUI handles Ctrl+C for turn interrupt vs quit.
	processCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()

	if !isInteractive() {
		return errors.New("interactive terminal required (stdin and stdout must be a TTY)")
	}

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

	chatModel, err := provider.NewOpenAIModel(processCtx, cfg.Model)
	if err != nil {
		return err
	}

	registry, err := tools.Default(time.Now, tools.RunCommandOptions{
		Disabled:       cfg.Tools.RunCommand.Disabled,
		TimeoutSeconds: cfg.Tools.RunCommand.TimeoutSeconds,
		MaxOutputBytes: cfg.Tools.RunCommand.MaxOutputBytes,
		WorkingDir:     cfg.Tools.RunCommand.WorkingDir,
	})
	if err != nil {
		return err
	}

	model, err := agent.NewReActModel(processCtx, chatModel, registry.All())
	if err != nil {
		return err
	}
	contextCfg := contextbuild.Config{
		KeepRecentTurns:           cfg.Context.KeepRecentTurns,
		ModelContextTokens:        cfg.Context.ModelContextTokens,
		OutputReserveTokens:       cfg.Context.OutputReserveTokens,
		AutoCompactTriggerPercent: cfg.Context.AutoCompactTriggerPercent,
		PostCompactTargetPercent:  cfg.Context.PostCompactTargetPercent,
		SummaryMaxTokens:          cfg.Context.SummaryMaxTokens,
		MaxLowGainAttempts:        cfg.Context.MaxLowGainAttempts,
		LowGainThresholdPercent:   cfg.Context.LowGainThresholdPercent,
	}
	// The raw provider has no tools bound. Keep compaction on this separate
	// interface so a checkpoint request cannot enter the ReAct tool loop.
	compactor, err := contextbuild.NewModelCompactor(chatModel, contextCfg)
	if err != nil {
		return fmt.Errorf("create context compactor: %w", err)
	}

	sessionOpts := chat.SessionOptions{
		Store:     sessionStore,
		ModelName: cfg.Model.Name,
		Pricing: usage.Pricing{
			InputPerMillion:  cfg.Pricing.InputPerMillion,
			OutputPerMillion: cfg.Pricing.OutputPerMillion,
		},
		Context:            contextCfg,
		Compactor:          compactor,
		RecoverInterrupted: start.recoverInterrupted,
	}

	var session *chat.Session
	if start.resumeID != "" {
		session, err = chat.OpenSession(model, sessionStore, start.resumeID, sessionOpts)
		if err != nil {
			return fmt.Errorf("resume session: %w", err)
		}
		fmt.Fprintf(stderr, "resumed session %s\n", session.ID())
	} else {
		sessionOpts.Title = start.title
		session, err = chat.NewSession(model, cfg.Assistant.SystemPrompt, sessionOpts)
		if err != nil {
			return err
		}
	}

	sessionID, err := tui.Run(processCtx, tui.Deps{
		Session:      session,
		Store:        sessionStore,
		SystemPrompt: cfg.Assistant.SystemPrompt,
		SessionOpts:  sessionOpts,
		Status:       statusFrom(cfg.Model.Name, registry),
	})
	// After alt-screen teardown, print a Codex-style resume command into
	// the main terminal scrollback so the session is one paste away.
	if hint := tui.FormatResumeHint(appName, sessionID); hint != "" {
		fmt.Fprintln(stderr, hint)
	}
	return err
}

func isInteractive() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

func statusFrom(modelName string, registry *tools.Registry) tui.StatusInfo {
	names := make([]string, 0)
	if registry != nil {
		infos, err := registry.Infos(context.Background())
		if err == nil {
			for _, info := range infos {
				if info != nil && info.Name != "" {
					names = append(names, info.Name)
				}
			}
		}
	}
	return tui.StatusInfo{Model: modelName, Tools: names, MaxStep: agent.MaxStep}
}
