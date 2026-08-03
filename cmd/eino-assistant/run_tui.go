package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"eino-local-assistant/internal/agent"
	"eino-local-assistant/internal/chat"
	"eino-local-assistant/internal/config"
	"eino-local-assistant/internal/contextbuild"
	"eino-local-assistant/internal/memory"
	"eino-local-assistant/internal/provider"
	"eino-local-assistant/internal/runtimeguard"
	"eino-local-assistant/internal/sandbox"
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

	chatModel, err := provider.NewChatModel(processCtx, cfg.Model)
	if err != nil {
		return err
	}

	perms, err := cfg.BuildPermissions()
	if err != nil {
		return err
	}
	workspaceRoot, err := tools.ResolveWorkspaceRoot(cfg.Workspace.Root)
	if err != nil {
		return err
	}
	protectedPaths, err := effectiveSandboxProtectedPaths(
		workspaceRoot,
		cfg.Sandbox.EffectiveProtectedPaths(),
		configPath,
		dataDir,
	)
	if err != nil {
		return err
	}
	readOnlyRoots, err := cfg.Sandbox.ResolveReadOnlyRoots()
	if err != nil {
		return err
	}
	sandboxRunner, err := tools.NewSandboxRunner(tools.SandboxRunnerOptions{
		Mode:           sandbox.Mode(cfg.Sandbox.ModeNormalized()),
		WorkspaceRoot:  workspaceRoot,
		ReadOnlyRoots:  readOnlyRoots,
		ProtectedPaths: protectedPaths,
		AllowedHosts:   cfg.Sandbox.Network.AllowedDomains,
	})
	if err != nil {
		return fmt.Errorf("create sandbox runner: %w", err)
	}
	defer sandboxRunner.Close()
	runtimeCfg := cfg.Runtime.Normalize()
	sessionAllows := tools.NewSessionAllowlist()
	sessionDenies := tools.NewSessionDenylist()
	approvalBridge := tui.NewApprovalBridge()
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
		return fmt.Errorf("open memory store: %w", err)
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
			Approver:       approvalBridge,
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
			Approver:      approvalBridge,
			SessionAllows: sessionAllows,
			SessionDenies: sessionDenies,
			Sandbox:       sandboxRunner,
		},
	})
	if err != nil {
		return err
	}
	// The task runtime stays in internal/agent, while its three orchestration
	// tools join the normal policy-enforced workspace tool registry here.
	taskController := agent.NewTaskController()
	taskTools, err := agent.NewTaskTools(taskController)
	if err != nil {
		return fmt.Errorf("create task tools: %w", err)
	}
	allTools := append(registry.All(), taskTools...)
	registry = tools.New(allTools...)

	model, err := agent.NewReActModelWithOptions(processCtx, chatModel, registry.All(), agent.ReActOptions{
		MaxStep:        runtimeCfg.MaxReactSteps,
		TaskController: taskController,
	})
	if err != nil {
		return err
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
			sum, err := memStore.Summary()
			if err != nil {
				return "", err
			}
			memBlock = agent.FormatMemoryBlock(sum.Text)
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
		session, err = chat.OpenSession(model, sessionStore, start.resumeID, sessionOpts)
		if err != nil {
			return fmt.Errorf("resume session: %w", err)
		}
		// Keep the durable create-time system prompt. Mid-session rewrites would
		// bust provider prefix cache and diverge from freeze-until-/new-or-/clear.
		fmt.Fprintf(stderr, "resumed session %s\n", session.ID())
	} else {
		sessionOpts.Title = start.title
		fullPrompt, err := composePrompt()
		if err != nil {
			return fmt.Errorf("compose system prompt: %w", err)
		}
		session, err = chat.NewSession(model, fullPrompt, sessionOpts)
		if err != nil {
			return err
		}
	}

	idleAfter, err := cfg.Memory.MemoryIdleAfterDuration()
	if err != nil {
		return err
	}
	scanMaxAge, err := cfg.Memory.MemoryScanMaxAgeDuration()
	if err != nil {
		return err
	}
	var activeSessionID atomic.Value
	activeSessionID.Store(session.ID())
	consolidator := &memory.Consolidator{
		Store:      memStore,
		Threads:    sessionStore,
		Model:      chatModel,
		IdleAfter:  idleAfter,
		ScanMaxAge: scanMaxAge,
		MaxPerScan: cfg.Memory.MemoryMaxRollouts(),
		ActiveThreadID: func() string {
			id, _ := activeSessionID.Load().(string)
			return id
		},
	}
	consolidator.StartLoop(processCtx, 5*time.Minute)

	cmdMode := "ask"
	if approvalMode == tools.ApprovalNever {
		cmdMode = "auto"
	}
	availability := sandbox.CurrentAvailability()
	backend := string(availability.Backend)
	if !availability.Available {
		backend = "unavailable"
	}
	sandboxInfo := tui.SandboxInfo{
		Mode:           cfg.Sandbox.ModeNormalized(),
		Backend:        backend,
		ReadOnlyRoots:  readOnlyRoots,
		ProtectedPaths: protectedPaths,
		AllowedDomains: cfg.Sandbox.Network.AllowedDomains,
		HostEscalation: !cfg.Tools.Shell.Disabled,
	}
	runtimeInfo := tui.RuntimeInfo{
		MaxTurnSeconds: runtimeCfg.MaxTurnSeconds,
		MaxReactSteps:  runtimeCfg.MaxReactSteps,
		MaxToolCalls:   runtimeCfg.MaxToolCalls,
	}
	policyInfo := tui.CommandPolicyInfo{
		Mode:          cmdMode,
		Approval:      string(approvalMode),
		Profile:       cfg.Permissions.PermissionsProfile(),
		WorkspaceOnly: true,
		WorkspaceRoot: workspaceRoot,
		Permissions:   perms,
		SessionAllows: sessionAllows,
		SessionDenies: sessionDenies,
		Sandbox:       sandboxInfo,
		Runtime:       runtimeInfo,
	}

	// Deps.SystemPrompt is the full composed prompt used when TUI creates a new
	// session (/new, /clear); keep it aligned with chat.NewSession above.
	composedPrompt, err := composePrompt()
	if err != nil {
		return fmt.Errorf("compose system prompt: %w", err)
	}
	sessionID, err := tui.Run(processCtx, tui.Deps{
		Session:      session,
		Store:        sessionStore,
		SystemPrompt: composedPrompt,
		ComposeSystemPrompt: func() (string, error) {
			return composePrompt()
		},
		SessionOpts: sessionOpts,
		Status:      statusFrom(cfg.Model.Provider+"/"+cfg.Model.Name, registry, cmdMode, model.MaxSteps(), sandboxInfo, runtimeInfo),
		TurnOptions: runtimeguard.TurnOptions{
			MaxToolCalls: runtimeCfg.MaxToolCalls,
			Timeout:      time.Duration(runtimeCfg.MaxTurnSeconds) * time.Second,
		},
		HideTurnUsage: !cfg.UI.TurnUsageEnabled(),
		Approval:      approvalBridge,
		PolicyInfo:    policyInfo,
		Memory:        memStore,
		NotifyActiveSession: func(id string) {
			activeSessionID.Store(id)
		},
	})
	// After alt-screen teardown, print a Codex-style resume command into
	// the main terminal scrollback so the session is one paste away.
	if hint := tui.FormatResumeHint(appName, sessionID); hint != "" {
		fmt.Fprintln(stderr, hint)
	}
	return err
}

// effectiveSandboxProtectedPaths adds host-owned control files that happen to
// live below a workspace. The model can invoke shell from that workspace, so
// a config with API credentials or the session ledger must not become readable
// merely because a user chose a colocated path.
func effectiveSandboxProtectedPaths(workspaceRoot string, configured []string, configPath, dataDir string) ([]string, error) {
	canonicalWorkspace, err := tools.ResolveWorkspaceRoot(workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve sandbox workspace: %w", err)
	}
	workspaceRoot = canonicalWorkspace
	if err := rejectWorkspaceControlSymlink(workspaceRoot, configPath, "configuration file"); err != nil {
		return nil, err
	}
	if err := rejectWorkspaceControlSymlink(workspaceRoot, dataDir, "session storage"); err != nil {
		return nil, err
	}
	paths := append([]string(nil), configured...)
	paths, _, err = addWorkspaceProtectedPath(paths, workspaceRoot, configPath, "configuration file")
	if err != nil {
		return nil, err
	}
	resolvedDataDir, err := resolveExistingPath(dataDir)
	if err != nil {
		return nil, fmt.Errorf("resolve session storage: %w", err)
	}
	if tools.PathWithinWorkspace(resolvedDataDir, workspaceRoot) {
		return nil, fmt.Errorf("session storage %q must not contain workspace %q", resolvedDataDir, workspaceRoot)
	}
	paths, _, err = addWorkspaceProtectedPath(paths, workspaceRoot, resolvedDataDir, "session storage")
	if err != nil {
		return nil, err
	}
	return paths, nil
}

// rejectWorkspaceControlSymlink prevents a worker from replacing a symlinked
// config or state entry after its resolved target has been masked. The next
// host launch must not consume a file the worker was able to swap in.
func rejectWorkspaceControlSymlink(workspaceRoot, candidate, label string) error {
	abs, err := filepath.Abs(strings.TrimSpace(candidate))
	if err != nil {
		return fmt.Errorf("resolve %s: %w", label, err)
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", label, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return rejectLexicalWorkspaceControlSymlink(workspaceRoot, abs, label)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return fmt.Errorf("resolve %s parent: %w", label, err)
	}
	entry := filepath.Join(parent, filepath.Base(abs))
	if tools.PathWithinWorkspace(workspaceRoot, entry) || pathLexicallyWithinWorkspaceVariant(workspaceRoot, abs) {
		return fmt.Errorf("%s %q must not be a symlink inside workspace %q", label, abs, workspaceRoot)
	}
	return rejectLexicalWorkspaceControlSymlink(workspaceRoot, abs, label)
}

func rejectLexicalWorkspaceControlSymlink(workspaceRoot, candidate, label string) error {
	for _, root := range workspacePathVariants(workspaceRoot) {
		if !pathLexicallyWithin(root, candidate) {
			continue
		}
		relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
		if err != nil {
			return err
		}
		current := filepath.Clean(root)
		for _, component := range strings.Split(relative, string(filepath.Separator)) {
			if component == "" || component == "." {
				continue
			}
			current = filepath.Join(current, component)
			info, err := os.Lstat(current)
			if err != nil {
				return fmt.Errorf("inspect %s: %w", label, err)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("%s %q must not traverse workspace symlink %q", label, candidate, current)
			}
		}
		return nil
	}
	return nil
}

func pathLexicallyWithinWorkspaceVariant(workspaceRoot, candidate string) bool {
	for _, root := range workspacePathVariants(workspaceRoot) {
		if pathLexicallyWithin(root, candidate) {
			return true
		}
	}
	return false
}

func workspacePathVariants(workspaceRoot string) []string {
	workspaceRoot = filepath.Clean(workspaceRoot)
	variants := []string{workspaceRoot}
	for _, alias := range []struct{ canonical, alternate string }{
		{canonical: "/private/etc", alternate: "/etc"},
		{canonical: "/private/tmp", alternate: "/tmp"},
		{canonical: "/private/var", alternate: "/var"},
	} {
		if workspaceRoot == alias.canonical {
			variants = append(variants, alias.alternate)
			continue
		}
		if strings.HasPrefix(workspaceRoot, alias.canonical+string(filepath.Separator)) {
			variants = append(variants, alias.alternate+strings.TrimPrefix(workspaceRoot, alias.canonical))
		}
	}
	return variants
}

func pathLexicallyWithin(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func addWorkspaceProtectedPath(paths []string, workspaceRoot, candidate, label string) ([]string, bool, error) {
	resolved, err := resolveExistingPath(candidate)
	if err != nil {
		return nil, false, fmt.Errorf("resolve %s: %w", label, err)
	}
	if !tools.PathWithinWorkspace(workspaceRoot, resolved) {
		return paths, false, nil
	}
	relative, err := filepath.Rel(workspaceRoot, resolved)
	if err != nil || relative == "." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, false, fmt.Errorf("%s %q cannot be protected below workspace %q", label, resolved, workspaceRoot)
	}
	pattern := filepath.ToSlash(relative)
	for _, existing := range paths {
		if existing == pattern {
			return paths, true, nil
		}
	}
	return append(paths, pattern), true, nil
}

func resolveExistingPath(raw string) (string, error) {
	abs, err := filepath.Abs(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

func isInteractive() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

func statusFrom(modelName string, registry *tools.Registry, cmdMode string, maxStep int, sandboxInfo tui.SandboxInfo, runtimeInfo tui.RuntimeInfo) tui.StatusInfo {
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
	cmdPolicy := ""
	if cmdMode != "" {
		cmdPolicy = "cmd=" + cmdMode
	}
	return tui.StatusInfo{
		Model:     modelName,
		Tools:     names,
		MaxStep:   maxStep,
		CmdPolicy: cmdPolicy,
		Sandbox:   sandboxInfo,
		Runtime:   runtimeInfo,
	}
}
