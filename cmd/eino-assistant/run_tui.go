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

	"eino-local-assistant/internal/chat"
	"eino-local-assistant/internal/config"
	"eino-local-assistant/internal/memory"
	"eino-local-assistant/internal/runtimeguard"
	"eino-local-assistant/internal/sandbox"
	"eino-local-assistant/internal/tools"
	"eino-local-assistant/internal/tui"

	"golang.org/x/term"
)

// sessionStart selects how the TUI conversation is opened.
type sessionStart struct {
	title              string
	resumeID           string
	forkID             string
	forkLast           bool
	initialPrompt      string
	recoverInterrupted bool
	ephemeral          bool
	modelName          string
	reasoningEffort    string
	reasoningEffortSet bool
	yolo               bool
}

func (start sessionStart) sourceSessionID() string {
	if id := strings.TrimSpace(start.resumeID); id != "" {
		return id
	}
	return strings.TrimSpace(start.forkID)
}

func (start sessionStart) validate() error {
	if strings.TrimSpace(start.resumeID) != "" && strings.TrimSpace(start.forkID) != "" {
		return errors.New("resume and fork session selectors cannot be combined")
	}
	if start.forkLast && strings.TrimSpace(start.forkID) != "" {
		return errors.New("fork accepts either a session id or --last, not both")
	}
	if start.forkLast && strings.TrimSpace(start.resumeID) != "" {
		return errors.New("resume and fork session selectors cannot be combined")
	}
	if start.ephemeral && (strings.TrimSpace(start.forkID) != "" || start.forkLast) {
		return errors.New("fork cannot use an ephemeral session ledger")
	}
	return nil
}

func runTUI(configPath string, start sessionStart, stderr io.Writer) (runErr error) {
	// Process lifetime: SIGTERM only. TUI handles Ctrl+C for turn interrupt vs quit.
	processCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()

	if !isInteractive() {
		return errors.New("interactive terminal required (stdin and stdout must be a TTY)")
	}

	approvalBridge := tui.NewApprovalBridge()
	runtime, err := newCommandRuntime(processCtx, configPath, start, approvalBridge)
	if err != nil {
		return err
	}
	if start.yolo {
		fmt.Fprintln(stderr, tools.YoloModeWarning)
	}
	defer func() {
		if closeErr := runtime.Close(); closeErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("close command runtime: %w", closeErr))
		}
	}()
	if runtime.forkParentID != "" {
		fmt.Fprintf(stderr, "forked session %s from %s\n", runtime.session.ID(), runtime.forkParentID)
	} else if start.resumeID != "" {
		// Keep the durable create-time system prompt. Mid-session rewrites would
		// bust provider prefix cache and diverge from freeze-until-/new-or-/clear.
		fmt.Fprintf(stderr, "resumed session %s\n", runtime.session.ID())
	}
	modelCfg, initialChatModel, initialReactModel, initialSessionOpts := runtime.modelSnapshot()

	idleAfter, err := modelCfg.Memory.MemoryIdleAfterDuration()
	if err != nil {
		return err
	}
	scanMaxAge, err := modelCfg.Memory.MemoryScanMaxAgeDuration()
	if err != nil {
		return err
	}
	var activeSessionID atomic.Value
	activeSessionID.Store(runtime.session.ID())
	consolidator := &memory.Consolidator{
		Store:      runtime.memStore,
		Threads:    runtime.sessionStore,
		Model:      initialChatModel,
		IdleAfter:  idleAfter,
		ScanMaxAge: scanMaxAge,
		MaxPerScan: modelCfg.Memory.MemoryMaxRollouts(),
		ActiveThreadID: func() string {
			id, _ := activeSessionID.Load().(string)
			return id
		},
	}
	consolidator.StartLoop(processCtx, 5*time.Minute)

	cmdMode := "ask"
	if runtime.approvalState != nil {
		cmdMode = runtime.approvalState.InteractiveMode()
	} else if runtime.approvalMode == tools.ApprovalNever {
		cmdMode = "auto"
	}
	var sandboxInfo tui.SandboxInfo
	if runtime.sandboxRunner != nil {
		availability := sandbox.CurrentAvailability()
		backend := string(availability.Backend)
		if !availability.Available {
			backend = "unavailable"
		}
		sandboxInfo = tui.SandboxInfo{
			Mode:                modelCfg.Sandbox.ModeNormalized(),
			Backend:             backend,
			ReadOnlyRoots:       runtime.readOnlyRoots,
			ProtectedPaths:      runtime.protectedPaths,
			HostEscalation:      !modelCfg.Tools.Shell.Disabled,
			ToolchainVisibility: string(runtime.sandboxEnvironment.Mode),
			EnvironmentMode:     sandboxEnvironmentMode(runtime.sandboxEnvironment),
			PathEntries:         runtime.sandboxEnvironment.PathEntries,
			CacheRoots:          runtime.sandboxEnvironment.CacheRoots,
		}
	}
	runtimeInfo := tui.RuntimeInfo{
		MaxTurnSeconds:                    runtime.runtimeCfg.MaxTurnSeconds,
		MaxModelSteps:                     runtime.runtimeCfg.MaxModelSteps,
		MaxToolCalls:                      runtime.runtimeCfg.MaxToolCalls,
		MaxConsecutiveEquivalentToolCalls: runtime.runtimeCfg.MaxConsecutiveEquivalentToolCalls,
	}
	policyInfo := tui.CommandPolicyInfo{
		Mode: cmdMode,
		// Keep the static config policy separate from the process-local yolo
		// override so /permissions does not mislabel yolo as a persisted policy.
		Approval:      modelCfg.ApprovalPolicyNormalized(),
		ApprovalState: runtime.approvalState,
		WorkspaceOnly: true,
		WorkspaceRoot: runtime.workspaceRoot,
		ToolPolicy:    runtime.toolPolicy,
		SessionAllows: runtime.sessionAllows,
		SessionDenies: runtime.sessionDenies,
		Sandbox:       sandboxInfo,
		Runtime:       runtimeInfo,
	}

	sessionID, err := tui.Run(processCtx, tui.Deps{
		Session: runtime.session,
		Store:   runtime.sessionStore,
		// Use the session's frozen prompt for both fresh and resumed sessions.
		// In particular, startup resume must not re-read current instruction files.
		SystemPrompt: runtime.session.SystemPrompt(),
		ComposeSystemPrompt: func() (string, error) {
			return runtime.composePrompt()
		},
		SideQuestion:    runtime.sideQuestion,
		WorkspaceReview: runtime.workspaceReview,
		SwitchModel: func(ctx context.Context, session *chat.Session, name string) (tui.ModelSwitchResult, error) {
			bundle, switchErr := runtime.switchModel(ctx, session, name)
			if switchErr != nil {
				return tui.ModelSwitchResult{}, switchErr
			}
			return tui.ModelSwitchResult{
				Status:      statusFromConfig(bundle.cfg, runtime.registry, cmdMode, bundle.reactModel.MaxModelSteps(), sandboxInfo, runtimeInfo),
				SessionOpts: bundle.sessionOpts,
			}, nil
		},
		SwitchModelWithOptions: func(ctx context.Context, session *chat.Session, selection tui.ModelSelection) (tui.ModelSwitchResult, error) {
			bundle, switchErr := runtime.switchModelWithOptions(ctx, session, selection.ModelName, selection.ReasoningEffort)
			if switchErr != nil {
				return tui.ModelSwitchResult{}, switchErr
			}
			return tui.ModelSwitchResult{
				Status:      statusFromConfig(bundle.cfg, runtime.registry, cmdMode, bundle.reactModel.MaxModelSteps(), sandboxInfo, runtimeInfo),
				SessionOpts: bundle.sessionOpts,
			}, nil
		},
		OpenSession: func(ctx context.Context, id string, recoverInterrupted bool) (tui.SessionOpenResult, error) {
			opened, openErr := runtime.openSession(ctx, id, recoverInterrupted)
			if openErr != nil {
				return tui.SessionOpenResult{}, openErr
			}
			return tui.SessionOpenResult{
				Session:     opened.session,
				Status:      statusFromConfig(opened.bundle.cfg, runtime.registry, cmdMode, opened.bundle.reactModel.MaxModelSteps(), sandboxInfo, runtimeInfo),
				SessionOpts: opened.bundle.sessionOpts,
			}, nil
		},
		RulesReport:             runtime.rulesReport,
		WorkspaceDiff:           runtime.workspaceDiff,
		InvalidateRulesSnapshot: runtime.invalidateRulesSnapshot,
		SessionOpts:             initialSessionOpts,
		Status:                  statusFromConfig(modelCfg, runtime.registry, cmdMode, initialReactModel.MaxModelSteps(), sandboxInfo, runtimeInfo),
		ModelCatalog:            modelCatalogFromConfig(modelCfg.Model.CatalogEntries()),
		TurnOptions: runtimeguard.TurnOptions{
			MaxModelSteps:                     runtime.runtimeCfg.MaxModelSteps,
			MaxToolCalls:                      runtime.runtimeCfg.MaxToolCalls,
			MaxConsecutiveEquivalentToolCalls: runtime.runtimeCfg.MaxConsecutiveEquivalentToolCalls,
			Timeout:                           time.Duration(runtime.runtimeCfg.MaxTurnSeconds) * time.Second,
		},
		HideTurnUsage: !modelCfg.UI.TurnUsageEnabled(),
		StatusLine: tui.StatusLineConfig{
			Fields: modelCfg.UI.StatusLineFields(),
		},
		SaveStatusLineConfig: func(statusLine tui.StatusLineConfig) error {
			return config.SaveStatusLineConfig(configPath, statusLine.Fields)
		},
		Approval:   approvalBridge,
		PolicyInfo: policyInfo,
		Memory:     runtime.memStore,
		NotifyActiveSession: func(id string) {
			activeSessionID.Store(id)
		},
		InitialPrompt: start.initialPrompt,
	})
	// After alt-screen teardown, print a Codex-style resume command into
	// the main terminal scrollback so the session is one paste away.
	if hint := tui.FormatResumeHint(appName, sessionID); hint != "" {
		fmt.Fprintln(stderr, hint)
	}
	return err
}

func sandboxEnvironmentMode(snapshot sandbox.EnvironmentSnapshot) string {
	if snapshot.Mode == sandbox.ToolchainVisibilityAuto {
		return "filtered-host"
	}
	return "isolated"
}

// effectiveSandboxProtectedPaths adds host-owned control files that happen to
// live below a workspace. The model can invoke shell from that workspace, so
// a config with API credentials or the session ledger must not become readable
// merely because a user chose a colocated path.
func effectiveSandboxProtectedPaths(workspaceRoot string, configured []string, configPath, dataDir string, additionalDataDirs ...string) ([]string, error) {
	return effectiveSandboxProtectedPathsWithSourceThreadPaths(workspaceRoot, configured, configPath, dataDir, additionalDataDirs, nil)
}

func effectiveSandboxProtectedPathsWithSourceThreadPaths(workspaceRoot string, configured []string, configPath, dataDir string, sourceDataDirs, sourceThreadPaths []string) ([]string, error) {
	return effectiveSandboxProtectedPathsWithUserToolPolicyRoot(workspaceRoot, configured, configPath, dataDir, "", sourceDataDirs, sourceThreadPaths)
}

// effectiveSandboxProtectedPathsWithUserToolPolicyRoot also protects the
// user-owned tool-policy directory when it is below the workspace. This keeps
// project trust and user rules outside the normal workspace-write surface even
// when session storage was configured elsewhere.
func effectiveSandboxProtectedPathsWithUserToolPolicyRoot(workspaceRoot string, configured []string, configPath, dataDir, userToolPolicyRoot string, sourceDataDirs, sourceThreadPaths []string) ([]string, error) {
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
	if strings.TrimSpace(userToolPolicyRoot) != "" {
		if err := rejectWorkspaceControlSymlink(workspaceRoot, userToolPolicyRoot, "user tool-policy directory"); err != nil {
			return nil, err
		}
	}
	paths := append([]string(nil), configured...)
	paths, _, err = addWorkspaceProtectedPath(paths, workspaceRoot, configPath, "configuration file")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(userToolPolicyRoot) != "" {
		paths, _, err = addWorkspaceProtectedPath(paths, workspaceRoot, userToolPolicyRoot, "user tool-policy directory")
		if err != nil {
			return nil, err
		}
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
	for _, sourceDataDir := range sourceDataDirs {
		if strings.TrimSpace(sourceDataDir) == "" {
			continue
		}
		if err := rejectWorkspaceControlSymlink(workspaceRoot, sourceDataDir, "source session storage"); err != nil {
			return nil, err
		}
		resolvedSourceDataDir, err := resolveExistingPath(sourceDataDir)
		if err != nil {
			return nil, fmt.Errorf("resolve source session storage: %w", err)
		}
		if tools.PathWithinWorkspace(resolvedSourceDataDir, workspaceRoot) {
			return nil, fmt.Errorf("source session storage %q must not contain workspace %q", resolvedSourceDataDir, workspaceRoot)
		}
		paths, _, err = addWorkspaceProtectedPath(paths, workspaceRoot, resolvedSourceDataDir, "source session storage")
		if err != nil {
			return nil, err
		}
	}
	for _, sourceThreadPath := range sourceThreadPaths {
		if strings.TrimSpace(sourceThreadPath) == "" {
			continue
		}
		if err := rejectWorkspaceControlSymlink(workspaceRoot, sourceThreadPath, "source session thread"); err != nil {
			return nil, err
		}
		resolvedSourceThreadPath, err := resolveExistingPath(sourceThreadPath)
		if err != nil {
			return nil, fmt.Errorf("resolve source session thread: %w", err)
		}
		paths, _, err = addWorkspaceProtectedPath(paths, workspaceRoot, resolvedSourceThreadPath, "source session thread")
		if err != nil {
			return nil, err
		}
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

func statusFrom(modelName, reasoningEffort string, registry *tools.Registry, cmdMode string, maxModelSteps int, sandboxInfo tui.SandboxInfo, runtimeInfo tui.RuntimeInfo) tui.StatusInfo {
	reasoningEffort = strings.TrimSpace(reasoningEffort)
	if reasoningEffort == "" {
		reasoningEffort = config.DefaultReasoningEffort
	}
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
		Model:           modelName,
		ReasoningEffort: reasoningEffort,
		Tools:           names,
		MaxModelSteps:   maxModelSteps,
		CmdPolicy:       cmdPolicy,
		Sandbox:         sandboxInfo,
		Runtime:         runtimeInfo,
	}
}

func statusFromConfig(cfg config.Config, registry *tools.Registry, cmdMode string, maxModelSteps int, sandboxInfo tui.SandboxInfo, runtimeInfo tui.RuntimeInfo) tui.StatusInfo {
	status := statusFrom(
		cfg.Model.Provider+"/"+cfg.Model.Name,
		cfg.Model.ReasoningEffort,
		registry,
		cmdMode,
		maxModelSteps,
		sandboxInfo,
		runtimeInfo,
	)
	status.ModelDisplayName = cfg.Model.CatalogDisplayName(cfg.Model.Name)
	status.DeclaredCatalogLifecycle = declaredCatalogLifecycle(cfg.Model)
	status.DeclaredReasoningEfforts, status.DeclaredReasoningEffortDefault = declaredReasoningCapabilities(cfg.Model)
	return status
}

// declaredCatalogLifecycle returns only the local catalog declaration for the
// current canonical model name. It does not describe provider health,
// entitlement, discovery, or provider-effective state.
func declaredCatalogLifecycle(modelCfg config.ModelConfig) string {
	canonicalName := strings.TrimSpace(modelCfg.Name)
	if canonicalName == "" {
		return ""
	}
	for _, entry := range modelCfg.CatalogEntries() {
		if !strings.EqualFold(strings.TrimSpace(entry.Name), canonicalName) {
			continue
		}
		lifecycle := strings.ToLower(strings.TrimSpace(entry.Lifecycle))
		if lifecycle == "" {
			return "active"
		}
		return lifecycle
	}
	return ""
}

// declaredReasoningCapabilities returns only metadata for the current
// canonical catalog name. Free-form names and aliases intentionally receive
// no catalog capability claims.
func declaredReasoningCapabilities(modelCfg config.ModelConfig) ([]string, string) {
	canonicalName := strings.TrimSpace(modelCfg.Name)
	if canonicalName == "" {
		return nil, ""
	}
	for _, entry := range modelCfg.CatalogEntries() {
		if !strings.EqualFold(strings.TrimSpace(entry.Name), canonicalName) {
			continue
		}
		efforts := make([]string, 0, len(entry.Capabilities.ReasoningEfforts))
		for _, effort := range entry.Capabilities.ReasoningEfforts {
			effort = strings.TrimSpace(effort)
			if effort != "" {
				efforts = append(efforts, effort)
			}
		}
		return efforts, strings.TrimSpace(entry.Capabilities.DefaultReasoningEffort)
	}
	return nil, ""
}

func modelCatalogFromConfig(entries []config.ModelCatalogEntry) []tui.ModelCatalogEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]tui.ModelCatalogEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, tui.ModelCatalogEntry{
			CanonicalName: strings.TrimSpace(entry.Name),
			DisplayName:   strings.TrimSpace(entry.DisplayName),
			Aliases:       append([]string(nil), entry.Aliases...),
			Description:   strings.TrimSpace(entry.Description),
			Lifecycle:     strings.TrimSpace(entry.Lifecycle),
			Provenance:    "config",
			Capabilities: tui.ModelCatalogCapabilities{
				ContextWindowTokens:    entry.Capabilities.ContextWindowTokens,
				SupportsReasoning:      copyBoolPointer(entry.Capabilities.SupportsReasoning),
				ReasoningEfforts:       append([]string(nil), entry.Capabilities.ReasoningEfforts...),
				DefaultReasoningEffort: strings.TrimSpace(entry.Capabilities.DefaultReasoningEffort),
				InputModalities:        append([]string(nil), entry.Capabilities.InputModalities...),
				SupportsTools:          copyBoolPointer(entry.Capabilities.SupportsTools),
				SupportsStreaming:      copyBoolPointer(entry.Capabilities.SupportsStreaming),
			},
		})
	}
	return out
}

func copyBoolPointer(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
