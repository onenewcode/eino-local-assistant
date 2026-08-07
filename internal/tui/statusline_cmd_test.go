package tui

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestStatusLineCommandSavesSelectedFieldsAndHidesProvider(t *testing.T) {
	m := newTestModel(t)
	m.deps.Status.Model = "openai/gpt-5.6-terra"
	var saved []string
	m.deps.SaveStatusLineFields = func(fields []string) error {
		saved = append([]string(nil), fields...)
		return nil
	}

	next, cmd := m.submit("/statusline set model, effort context policy")
	mm := next.(*model)
	if cmd != nil {
		t.Fatalf("/statusline should not start a turn: %v", cmd)
	}
	want := []string{statusFieldModel, statusFieldEffort, statusFieldContext, statusFieldPolicy}
	if !reflect.DeepEqual(mm.deps.StatusLineFields, want) || !reflect.DeepEqual(saved, want) {
		t.Fatalf("status-line fields = %#v, saved=%#v, want=%#v", mm.deps.StatusLineFields, saved, want)
	}
	line := mm.statusLabel()
	if !strings.Contains(line, "gpt-5.6-terra") || strings.Contains(line, "openai/") {
		t.Fatalf("model footer did not hide provider: %q", line)
	}
	next, _ = mm.submit("/statusline hide policy")
	mm = next.(*model)
	if strings.Contains(mm.statusLabel(), "cmd=") {
		t.Fatalf("hidden policy still appears: %q", mm.statusLabel())
	}
	if want := []string{statusFieldModel, statusFieldEffort, statusFieldContext}; !reflect.DeepEqual(saved, want) {
		t.Fatalf("hide save = %#v, want=%#v", saved, want)
	}
}

func TestStatusLineCommandDoesNotApplyUnsavedChange(t *testing.T) {
	m := newTestModel(t)
	m.deps.StatusLineFields = []string{statusFieldModel, statusFieldContext}
	m.deps.SaveStatusLineFields = func([]string) error { return errors.New("read-only config") }

	next, _ := m.submit("/statusline show policy")
	mm := next.(*model)
	if want := []string{statusFieldModel, statusFieldContext}; !reflect.DeepEqual(mm.deps.StatusLineFields, want) {
		t.Fatalf("failed save changed fields = %#v, want=%#v", mm.deps.StatusLineFields, want)
	}
	if !hasLineContaining(mm.lines, lineError, "save status-line settings: read-only config") {
		t.Fatalf("save failure missing from transcript: %#v", mm.lines)
	}
}

func TestStatusLineCommandReportsAndValidatesFields(t *testing.T) {
	m := newTestModel(t)
	next, _ := m.submit("/statusline")
	mm := next.(*model)
	if !hasLineContaining(mm.lines, lineSystem, "Status line fields (persistent)") || !hasLineContaining(mm.lines, lineSystem, "Available: model") {
		t.Fatalf("status-line report missing: %#v", mm.lines)
	}

	next, _ = mm.submit("/statusline set model model")
	mm = next.(*model)
	if !hasLineContaining(mm.lines, lineError, "duplicate status-line field: model") {
		t.Fatalf("duplicate validation missing: %#v", mm.lines)
	}
}
