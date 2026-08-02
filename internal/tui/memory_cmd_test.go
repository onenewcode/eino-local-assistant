package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"eino-local-assistant/internal/chat"
	"eino-local-assistant/internal/contextbuild"
	"eino-local-assistant/internal/memory"
	"eino-local-assistant/internal/store"
)

func TestMemoryAddDoesNotRewriteSystemPrompt(t *testing.T) {
	ctx := context.Background()
	ws := t.TempDir()
	st, err := store.NewThreadStore(filepath.Join(ws, "sessions"))
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	mem, err := memory.Open(memory.Options{
		WorkspaceRoot:   ws,
		UseEnabled:      true,
		GenerateEnabled: false,
	})
	if err != nil {
		t.Fatalf("memory.Open: %v", err)
	}
	const createSystem = "session system create-time"
	session, err := chat.NewSession(&staticModel{}, createSystem, chat.SessionOptions{Store: st})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	composeCalls := 0
	m := newModel(Deps{
		Ctx:     ctx,
		Session: session,
		Store:   st,
		Memory:  mem,
		ComposeSystemPrompt: func() (string, error) {
			composeCalls++
			return "should not be applied mid-session", nil
		},
	})
	next, _ := m.cmdMemory("add keep-frozen preference")
	mm := next.(*model)
	if mm.deps.Session.SystemPrompt() != createSystem {
		t.Fatalf("system rewritten by /memory add: %q", mm.deps.Session.SystemPrompt())
	}
	if composeCalls != 0 {
		t.Fatalf("compose called %d times on /memory add; want 0", composeCalls)
	}
	if !hasLineContaining(mm.lines, lineSystem, projectContextRefreshHint) {
		t.Fatalf("missing refresh hint: %#v", mm.lines)
	}
	entries, err := mem.ListActive()
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
}

func TestResumeDoesNotRefreshSystemPrompt(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	const original = "original system"
	first, err := chat.NewSession(&staticModel{}, original, chat.SessionOptions{Store: st})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	id := first.ID()
	active, err := chat.NewSession(&staticModel{}, "other session system", chat.SessionOptions{Store: st})
	if err != nil {
		t.Fatalf("NewSession active: %v", err)
	}
	composeCalls := 0
	m := newModel(Deps{
		Ctx:     ctx,
		Session: active,
		Store:   st,
		ComposeSystemPrompt: func() (string, error) {
			composeCalls++
			return "fresh disk agents", nil
		},
		SessionOpts: chat.SessionOptions{Store: st},
		Status:      StatusInfo{Model: "test"},
	})
	next, _ := m.cmdResume(id)
	mm := next.(*model)
	if composeCalls != 0 {
		t.Fatalf("compose called %d times on /resume; want 0", composeCalls)
	}
	if mm.deps.Session.ID() != id {
		t.Fatalf("resumed id = %q, want %q", mm.deps.Session.ID(), id)
	}
	if got := mm.deps.Session.SystemPrompt(); got != original {
		t.Fatalf("resumed system = %q, want %q", got, original)
	}
}

func TestCompactPreservesCreateTimeSystemPrompt(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	const createSystem = "create-time system for compact freeze"
	compactor := tuiCheckpointCompactor(func(_ context.Context, request contextbuild.CompactionRequest, _ contextbuild.CompactionUsageObserver) (contextbuild.Checkpoint, error) {
		return contextbuild.DeterministicCheckpoint(request)
	})
	// Same gain floor pattern as chat compaction fixtures: structured
	// DeterministicCheckpoint is not useful for tiny one-line source turns.
	session, err := chat.NewSession(&staticModel{}, createSystem, chat.SessionOptions{
		Store:     st,
		Compactor: compactor,
		Context: contextbuild.Config{
			WindowTokens:              8_000,
			MaxOutputTokens:           1_000,
			KeepRecentTurns:           1,
			LowGainThresholdPercent:   1,
			AutoCompactTriggerPercent: 75,
			PostCompactTargetPercent:  45,
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	bulk := strings.Repeat("retained evidence ", 300)
	if err := session.Ask(ctx, "first "+bulk, nil); err != nil {
		t.Fatalf("first Ask: %v", err)
	}
	if err := session.Ask(ctx, "second "+bulk, nil); err != nil {
		t.Fatalf("second Ask: %v", err)
	}
	if _, err := session.Compact(ctx, "preserve facts"); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if got := session.SystemPrompt(); got != createSystem {
		t.Fatalf("after compact system = %q, want create-time %q", got, createSystem)
	}
	// applyThreadState after a later turn must still match durable create-time system.
	if err := session.Ask(ctx, "post-compact turn", nil); err != nil {
		t.Fatalf("post-compact Ask: %v", err)
	}
	if got := session.SystemPrompt(); got != createSystem {
		t.Fatalf("after post-compact turn system = %q, want %q", got, createSystem)
	}
	// Resume must load the same durable snapshot (not a live-only rewrite).
	resumed, err := chat.OpenSession(&staticModel{}, st, session.ID(), chat.SessionOptions{Store: st})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if got := resumed.SystemPrompt(); got != createSystem {
		t.Fatalf("resume after compact system = %q, want %q", got, createSystem)
	}
}

func TestFinishCompactionDoesNotClaimProjectContextReload(t *testing.T) {
	m := newTestModel(t)
	const createSystem = "frozen"
	// Ensure finishCompaction success path has no reload wording even if compose is set.
	composeCalls := 0
	m.deps.ComposeSystemPrompt = func() (string, error) {
		composeCalls++
		return "reloaded should not apply", nil
	}
	if m.deps.Session != nil {
		// newTestModel may already set a session; freeze check uses SystemPrompt if present.
		_ = createSystem
	}
	m.mode = modeCompacting
	_ = m.finishCompaction(compactDoneMsg{
		result: chat.CompactionResult{
			CheckpointID: "cmp_test",
			BeforeTokens: 100,
			AfterTokens:  40,
		},
	})
	if composeCalls != 0 {
		t.Fatalf("finishCompaction composed system %d times; want 0", composeCalls)
	}
	for _, line := range m.lines {
		if strings.Contains(line.text, "project context reloaded") ||
			strings.Contains(line.text, "project context reload") {
			t.Fatalf("misleading reload UX: %#v", m.lines)
		}
	}
	if !hasLineContaining(m.lines, lineSystem, "context compacted") {
		t.Fatalf("missing compact success line: %#v", m.lines)
	}
}
