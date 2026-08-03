package main

import (
	"context"
	"fmt"
	"time"

	"eino-local-assistant/internal/agent"
	"eino-local-assistant/internal/chat"
	"eino-local-assistant/internal/config"
	"eino-local-assistant/internal/contextbuild"
	"eino-local-assistant/internal/memory"
	"eino-local-assistant/internal/provider"
	"eino-local-assistant/internal/sandbox"
	"eino-local-assistant/internal/store"
	"eino-local-assistant/internal/tools"
	"eino-local-assistant/internal/usage"

	"github.com/cloudwego/eino/components/model"
)

// commandRuntime contains the production dependencies shared by the
// interactive TUI and non-interactive commands. It stays private to cmd so it
// cannot become a second application layer outside the existing packages.
type commandRuntime struct {
	cfg            config.Config
	session        *chat.Session
	sessionStore   *store.ThreadStore
	chatModel      model.ToolCallingChatModel
	registry       *tools.Registry
	reactModel     *agent.ReActModel
	memStore       *memory.Store
	sessionOpts    chat.SessionOptions
	composePrompt  func() (string, error)
	approvalMode   tools.ApprovalMode
	permissions    *tools.PermissionSet
	sessionAllows  *tools.SessionAllowlist
	sessionDenies  *tools.SessionDenylist
	workspaceRoot  string
	readOnlyRoots  []string
	protectedPaths []string
	sandboxRunner  *tools.SandboxRunner
	runtimeCfg     config.RuntimeConfig
}

// Close releases the sandbox worker copy after no more tools can run.
func (r *commandRuntime) Close() error {
	if r == nil || r.sandboxRunner == nil {
		return nil
	}
	return r.sandboxRunner.Close()
}

// newCommandRuntime performs the shared production wiring. An absent
// approver is intentional for headless execution: the tool layer then rejects
// on-request decisions instead of trying to consume command stdin.
func newCommandRuntime(ctx context.Context, configPath string, start sessionStart, approver tools.Approver) (_ *commandRuntime, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}

	dataDir, err := cfg.Storage.ResolveDataDir()
	if err != nil {
		return nil, err
	}
	sessionStore, err := store.NewThreadStore(dataDir)
	if err != nil {
		return nil, fmt.Errorf("open session store: %w", err)
	}

	chatModel, err := provider.NewChatModel(ctx, cfg.Model)
	if err != nil {
		return nil, err
	}

	perms, err := cfg.BuildPermissions()
	if err != nil {
		return nil, err
	}
	workspaceRoot, err := tools.ResolveWorkspaceRoot(cfg.Workspace.Root)
	if err != nil {
		return nil, err
	}
	protectedPaths, err := effectiveSandboxProtectedPaths(
		workspaceRoot,
		cfg.Sandbox.EffectiveProtectedPaths(),
		configPath,
		dataDir,
	)
	if err != nil {
		return nil, err
	}
	readOnlyRoots, err := cfg.Sandbox.ResolveReadOnlyRoots()
	if err != nil {
		return nil, err
	}
	sandboxRunner, err := tools.NewSandboxRunner(tools.SandboxRunnerOptions{
		Mode:           sandbox.Mode(cfg.Sandbox.ModeNormalized()),
		WorkspaceRoot:  workspaceRoot,
		ReadOnlyRoots:  readOnlyRoots,
		ProtectedPaths: protectedPaths,
		AllowedHosts:   cfg.Sandbox.Network.AllowedDomains,
	})
	if err != nil {
		return nil, fmt.Errorf("create sandbox runner: %w", err)
	}
	defer func() {
		if err != nil {
			_ = sandboxRunner.Close()
		}
	}()

	runtimeCfg := cfg.Runtime.Normalize()
	sessionAllows := tools.NewSessionAllowlist()
	sessionDenies := tools.NewSessionDenylist()
	approvalMode := tools.NormalizeApprovalMode(cfg.ApprovalPolicyNormalized())
	shell := cfg.Tools.Shell
	patch := cfg.Tools.ApplyPatch

	memStore, err := memory.Open(memory.Options{
		WorkspaceRoot:    workspaceRoot,
		MaxSummaryTokens: cfg.Memory.MemorySummaryTokens(),
		UseEnabled:       cfg.Memory.MemoryEnabled(),
		GenerateEnabled:  cfg.Memory.MemoryGenerate(),
	})
	if err != nil {
		return nil, fmt.Errorf("open memory store: %w", err)
	}

	registry, err := tools.DefaultWithOptions(tools.DefaultOptions{
		Clock:       time.Now,
		MemoryStore: memStore,
		Shell: tools.ShellOptions{
			Disabled:       shell.Disabled,
			TimeoutSeconds: shell.TimeoutSeconds,
			MaxOutputBytes: shell.MaxOutputBytes,
			WorkingDir:     shell.WorkingDir,
			Approval:       approvalMode,
			WorkspaceOnly:  true,
			WorkspaceRoot:  workspaceRoot,
			Permissions:    perms,
			Approver:       approver,
			SessionAllows:  sessionAllows,
			SessionDenies:  sessionDenies,
			Sandbox:        sandboxRunner,
		},
		ApplyPatch: tools.ApplyPatchOptions{
			Disabled:      patch.Disabled,
			WorkspaceRoot: workspaceRoot,
			MaxBytes:      patch.MaxBytes,
			Approval:      approvalMode,
			Permissions:   perms,
			Approver:      approver,
			SessionAllows: sessionAllows,
			SessionDenies: sessionDenies,
			Sandbox:       sandboxRunner,
		},
	})
	if err != nil {
		return nil, err
	}
	taskController := agent.NewTaskController()
	taskTools, err := agent.NewTaskTools(taskController)
	if err != nil {
		return nil, fmt.Errorf("create task tools: %w", err)
	}
	registry = tools.New(append(registry.All(), taskTools...)...)

	reactModel, err := agent.NewReActModelWithOptions(ctx, chatModel, registry.All(), agent.ReActOptions{
		MaxStep:        runtimeCfg.MaxReactSteps,
		TaskController: taskController,
	})
	if err != nil {
		return nil, err
	}
	modelContext := cfg.Model.Context
	contextCfg := contextbuild.Config{
		WindowTokens:              modelContext.WindowTokens,
		MaxOutputTokens:           modelContext.MaxOutputTokens,
		KeepRecentTurns:           modelContext.KeepRecentTurns,
		AutoCompactTriggerPercent: modelContext.AutoCompactTriggerPercent,
		PostCompactTargetPercent:  modelContext.PostCompactTargetPercent,
		SummaryMaxTokens:          modelContext.SummaryMaxTokens,
		LowGainThresholdPercent:   modelContext.LowGainThresholdPercent,
	}
	compactor, err := contextbuild.NewModelCompactor(chatModel, contextCfg)
	if err != nil {
		return nil, fmt.Errorf("create context compactor: %w", err)
	}

	sessionOpts := chat.SessionOptions{
		Store:     sessionStore,
		ModelName: cfg.Model.Name,
		Pricing: usage.Pricing{
			InputPerMillion:  cfg.Model.Pricing.InputPerMillion,
			OutputPerMillion: cfg.Model.Pricing.OutputPerMillion,
		},
		Context:            contextCfg,
		MaxLowGainAttempts: modelContext.MaxLowGainAttempts,
		Compactor:          compactor,
		RecoverInterrupted: start.recoverInterrupted,
	}
	composePrompt := func() (string, error) {
		memBlock := ""
		if memStore.UseEnabled() {
			summary, summaryErr := memStore.Summary()
			if summaryErr != nil {
				return "", summaryErr
			}
			memBlock = agent.FormatMemoryBlock(summary.Text)
		}
		return agent.ComposeWithLayers(cfg.Assistant.SystemPrompt, agent.LayerOptions{
			WorkspaceRoot:              workspaceRoot,
			ProjectInstructionsEnabled: cfg.Rules.RulesEnabled(),
			ProjectInstructionsTokens:  cfg.Rules.RulesMaxTokens(),
			MemoryBlock:                memBlock,
		})
	}

	var session *chat.Session
	if start.resumeID != "" {
		session, err = chat.OpenSession(reactModel, sessionStore, start.resumeID, sessionOpts)
		if err != nil {
			return nil, fmt.Errorf("resume session: %w", err)
		}
	} else {
		sessionOpts.Title = start.title
		fullPrompt, promptErr := composePrompt()
		if promptErr != nil {
			return nil, fmt.Errorf("compose system prompt: %w", promptErr)
		}
		session, err = chat.NewSession(reactModel, fullPrompt, sessionOpts)
		if err != nil {
			return nil, err
		}
	}

	return &commandRuntime{
		cfg:            cfg,
		session:        session,
		sessionStore:   sessionStore,
		chatModel:      chatModel,
		registry:       registry,
		reactModel:     reactModel,
		memStore:       memStore,
		sessionOpts:    sessionOpts,
		composePrompt:  composePrompt,
		approvalMode:   approvalMode,
		permissions:    perms,
		sessionAllows:  sessionAllows,
		sessionDenies:  sessionDenies,
		workspaceRoot:  workspaceRoot,
		readOnlyRoots:  readOnlyRoots,
		protectedPaths: protectedPaths,
		sandboxRunner:  sandboxRunner,
		runtimeCfg:     runtimeCfg,
	}, nil
}
