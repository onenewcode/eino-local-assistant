package tui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestSkillsCommandListsBoundedMetadataWithoutSessionMutation(t *testing.T) {
	session := mustSession(t, &staticModel{}, "system")
	called := false
	m := newModel(Deps{
		Ctx:     context.Background(),
		Session: session,
		ListProjectSkills: func(ctx context.Context) (ProjectSkillsCatalog, error) {
			called = true
			if ctx == nil {
				t.Fatal("list callback received nil context")
			}
			return ProjectSkillsCatalog{Skills: []ProjectSkillSummary{{
				Name:        "release",
				Path:        ".agents/skills/release/SKILL.md",
				Description: "Run the release checklist.",
			}}, Truncated: true}, nil
		},
	})
	m.queue = []string{"follow-up"}
	beforeTranscript := session.Transcript()
	beforeQueue := append([]string(nil), m.queue...)

	next, cmd := m.submit("/skills")
	mm := next.(*model)
	if cmd != nil || !called {
		t.Fatalf("/skills must synchronously call the local read-only callback: cmd=%v called=%v", cmd, called)
	}
	if !reflect.DeepEqual(session.Transcript(), beforeTranscript) {
		t.Fatal("/skills changed the durable session transcript")
	}
	if !reflect.DeepEqual(mm.queue, beforeQueue) {
		t.Fatalf("/skills changed queue: got %#v want %#v", mm.queue, beforeQueue)
	}
	for _, want := range []string{
		"Project skills (1):",
		"release - .agents/skills/release/SKILL.md - Run the release checklist.",
		"discovery reached its bounded result limit",
		"Use /skills <name> to preview one discovered SKILL.md",
	} {
		if !hasLineContaining(mm.lines, lineSystem, want) {
			t.Fatalf("/skills output missing %q: %#v", want, mm.lines)
		}
	}
}

func TestSkillsCommandReadsSelectedSkillAndSanitizesPreview(t *testing.T) {
	calledWith := ""
	m := newModel(Deps{
		Ctx:     context.Background(),
		Session: mustSession(t, &staticModel{}, "system"),
		ReadProjectSkill: func(_ context.Context, name string) (ProjectSkillDetails, error) {
			calledWith = name
			return ProjectSkillDetails{
				Name:      "release checklist",
				Path:      ".agents/skills/release checklist/SKILL.md",
				Content:   "# Release\n\nRun checks.\x1b[2J\xff",
				Bytes:     29,
				Truncated: true,
			}, nil
		},
	})

	next, cmd := m.submit("/skills release checklist")
	mm := next.(*model)
	if cmd != nil || calledWith != "release checklist" {
		t.Fatalf("/skills reader call = %q cmd=%v", calledWith, cmd)
	}
	if !hasLineContaining(mm.lines, lineSystem, "Project skill: release checklist") ||
		!hasLineContaining(mm.lines, lineSystem, "Read: 29 bytes (truncated)") ||
		!hasLineContaining(mm.lines, lineSystem, "This is project data only") {
		t.Fatalf("skill detail output missing: %#v", mm.lines)
	}
	for _, line := range mm.lines {
		if strings.ContainsAny(line.text, "\x1b\xff") {
			t.Fatalf("skill preview leaked terminal control data: %q", line.text)
		}
	}
}

func TestSkillsCommandHandlesUnavailableReaderAndBoundedTUIPreview(t *testing.T) {
	m := newTestModel(t)
	next, cmd := m.submit("/skills release")
	if cmd != nil || !hasLineContaining(next.(*model).lines, lineError, "skills unavailable: project skill reader is not configured") {
		t.Fatalf("missing reader should have a local error: %#v", next.(*model).lines)
	}

	m = newModel(Deps{
		Ctx:     context.Background(),
		Session: mustSession(t, &staticModel{}, "system"),
		ListProjectSkills: func(context.Context) (ProjectSkillsCatalog, error) {
			return ProjectSkillsCatalog{}, errors.New("denied\x1b[31m")
		},
		ReadProjectSkill: func(context.Context, string) (ProjectSkillDetails, error) {
			return ProjectSkillDetails{Content: strings.Repeat("x", skillsCommandMaxBytes+10)}, nil
		},
	})
	next, _ = m.submit("/skills")
	if hasLineContaining(next.(*model).lines, lineError, "\x1b") {
		t.Fatal("skills error leaked terminal control byte")
	}
	next, cmd = m.submit("/skills huge")
	if cmd != nil || !hasLineContaining(next.(*model).lines, lineSystem, "TUI preview truncated after 65536 bytes") {
		t.Fatalf("TUI preview cap missing: %#v", next.(*model).lines)
	}
}

func TestSkillsCommandRunsImmediatelyWhileBusy(t *testing.T) {
	m := newModel(Deps{
		Ctx:     context.Background(),
		Session: mustSession(t, &staticModel{}, "system"),
		ReadProjectSkill: func(context.Context, string) (ProjectSkillDetails, error) {
			return ProjectSkillDetails{Name: "release", Path: "skills/release/SKILL.md", Content: "check"}, nil
		},
	})
	m.mode = modeBusy
	m.turnID = 7
	m.queue = []string{"retained"}
	cancelled := false
	m.turnCancel = func() { cancelled = true }

	next, cmd := m.queueWhileBusy("/skills release")
	mm := next.(*model)
	if cmd != nil || mm.mode != modeBusy || mm.turnID != 7 || cancelled {
		t.Fatalf("busy /skills changed foreground state: mode=%s turn=%d cmd=%v cancelled=%v", modeName(mm.mode), mm.turnID, cmd, cancelled)
	}
	if !reflect.DeepEqual(mm.queue, []string{"retained"}) {
		t.Fatalf("busy /skills changed queue: %#v", mm.queue)
	}
	if !hasLineContaining(mm.lines, lineSystem, "Project skill: release") {
		t.Fatalf("busy /skills did not render result: %#v", mm.lines)
	}
}
