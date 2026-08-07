package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"eino-local-assistant/internal/chat"

	tea "github.com/charmbracelet/bubbletea"
)

func TestModelPickerAltPOpensWithCapabilitiesAndAppliesCanonicalName(t *testing.T) {
	m := newTestModel(t)
	m.deps.Status.Model = "openai/old-model"
	m.deps.SessionOpts.ModelName = "old-model"
	m.deps.ModelCatalog = []ModelCatalogEntry{
		{
			CanonicalName: "old-model",
			DisplayName:   "Old model",
		},
		{
			CanonicalName: "gpt-5.2-coding",
			DisplayName:   "Coding 5.2",
			Aliases:       []string{"coding"},
			Description:   "general coding model",
			Provenance:    "config",
			Capabilities: ModelCatalogCapabilities{
				ContextWindowTokens: 128000,
				ReasoningEfforts:    []string{"low", "high"},
				InputModalities:     []string{"text", "image"},
				SupportsTools:       boolPointer(true),
				SupportsStreaming:   boolPointer(true),
			},
		},
	}
	m.textarea.SetValue("draft that should survive the picker")

	var callbackName string
	m.deps.SwitchModel = func(_ context.Context, _ *chat.Session, name string) (ModelSwitchResult, error) {
		callbackName = name
		return ModelSwitchResult{
			Status:      StatusInfo{Model: "openai/" + name, ModelDisplayName: "Coding 5.2"},
			SessionOpts: chat.SessionOptions{ModelName: name},
		}, nil
	}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}, Alt: true})
	m = next.(*model)
	if !m.modelPickerOpen() {
		t.Fatal("Alt+P did not open model picker")
	}
	if m.textarea.Value() != "draft that should survive the picker" {
		t.Fatalf("picker changed composer draft: %q", m.textarea.Value())
	}
	view := m.View()
	for _, want := range []string{"Coding 5.2", "gpt-5.2-coding", "alias=coding", "ctx=128k", "effort=low/high", "input=text/image", "tools=yes", "source=config"} {
		if !strings.Contains(view, want) {
			t.Fatalf("picker view missing %q:\n%s", want, view)
		}
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(*model)
	if m.modelPickerSel != 1 {
		t.Fatalf("picker selection = %d, want second entry", m.modelPickerSel)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(*model)
	if callbackName != "gpt-5.2-coding" {
		t.Fatalf("callback name = %q, want canonical name", callbackName)
	}
	if m.modelPickerOpen() {
		t.Fatal("successful picker selection left overlay open")
	}
	if m.textarea.Value() != "draft that should survive the picker" {
		t.Fatalf("successful picker selection changed draft: %q", m.textarea.Value())
	}
	if !hasLineContaining(m.lines, lineSystem, "model switched to Coding 5.2") {
		t.Fatalf("picker confirmation missing: %#v", m.lines)
	}
}

func TestModelPickerEffortOptions(t *testing.T) {
	tests := []struct {
		name  string
		entry ModelCatalogEntry
		want  []string
	}{
		{
			name: "single declaration keeps different catalog default",
			entry: ModelCatalogEntry{Capabilities: ModelCatalogCapabilities{
				ReasoningEfforts:       []string{"low"},
				DefaultReasoningEffort: "xhigh",
			}},
			want: []string{"low", "medium", "xhigh"},
		},
		{
			name: "single declaration deduplicates matching catalog default",
			entry: ModelCatalogEntry{Capabilities: ModelCatalogCapabilities{
				ReasoningEfforts:       []string{"low"},
				DefaultReasoningEffort: "LOW",
			}},
			want: []string{"low", "medium"},
		},
		{
			name: "empty declaration keeps catalog default behavior",
			entry: ModelCatalogEntry{Capabilities: ModelCatalogCapabilities{
				DefaultReasoningEffort: "balanced",
			}},
			want: []string{"medium", "balanced"},
		},
		{
			name:  "empty declaration defaults to medium",
			entry: ModelCatalogEntry{},
			want:  []string{"medium"},
		},
		{
			name: "multiple declarations retain order and append different default",
			entry: ModelCatalogEntry{Capabilities: ModelCatalogCapabilities{
				ReasoningEfforts:       []string{"low", "high"},
				DefaultReasoningEffort: "xhigh",
			}},
			want: []string{"low", "high", "medium", "xhigh"},
		},
		{
			name: "multiple declarations deduplicate default case insensitively",
			entry: ModelCatalogEntry{Capabilities: ModelCatalogCapabilities{
				ReasoningEfforts:       []string{"low", "high"},
				DefaultReasoningEffort: "HIGH",
			}},
			want: []string{"low", "high", "medium"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := modelPickerEffortOptions(tt.entry)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("modelPickerEffortOptions() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestModelPickerAppliesSelectedEffortInsteadOfAPlaceholder(t *testing.T) {
	m := newTestModel(t)
	m.deps.Status.Model = "openai/current-model"
	m.deps.ModelCatalog = []ModelCatalogEntry{{
		CanonicalName: "candidate-model",
		DisplayName:   "Candidate",
		Capabilities: ModelCatalogCapabilities{
			ReasoningEfforts: []string{"low", "high"},
		},
	}}
	var received ModelSelection
	m.deps.SwitchModelWithOptions = func(_ context.Context, _ *chat.Session, selection ModelSelection) (ModelSwitchResult, error) {
		received = selection
		return ModelSwitchResult{
			Status: StatusInfo{
				Model:           "openai/" + selection.ModelName,
				ReasoningEffort: selection.ReasoningEffort,
			},
			SessionOpts: chat.SessionOptions{
				ModelName:       selection.ModelName,
				ReasoningEffort: selection.ReasoningEffort,
			},
		}, nil
	}

	next, _ := m.openModelPicker()
	m = next.(*model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(*model)
	if !m.modelPickerEffortOpen() {
		t.Fatal("model selection did not open the effort picker")
	}
	if got := m.modelPickerEfforts[m.modelPickerEffortSel]; got != defaultModelReasoningEffort {
		t.Fatalf("initial picker effort = %q, want %q", got, defaultModelReasoningEffort)
	}
	if view := m.View(); strings.Contains(view, "default") {
		t.Fatalf("effort picker must not show a default placeholder:\n%s", view)
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(*model)
	if received.ModelName != "candidate-model" || received.ReasoningEffort != defaultModelReasoningEffort {
		t.Fatalf("selected effort was not forwarded: %#v", received)
	}
	if got := m.deps.Status.ReasoningEffort; got != defaultModelReasoningEffort {
		t.Fatalf("displayed effort = %q, want the applied request %q", got, defaultModelReasoningEffort)
	}
}

func TestModelPickerFailureKeepsCurrentBindingAndOverlay(t *testing.T) {
	m := newTestModel(t)
	oldStatus := m.deps.Status
	m.deps.ModelCatalog = []ModelCatalogEntry{{CanonicalName: "candidate", DisplayName: "Candidate"}}
	wantErr := errors.New("candidate unavailable")
	m.deps.SwitchModel = func(context.Context, *chat.Session, string) (ModelSwitchResult, error) {
		return ModelSwitchResult{}, wantErr
	}

	next, _ := m.openModelPicker()
	m = next.(*model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(*model)
	if !m.modelPickerOpen() {
		t.Fatal("failed picker selection should remain open for retry")
	}
	if m.deps.Status.Model != oldStatus.Model {
		t.Fatalf("failed picker selection changed status model: %q", m.deps.Status.Model)
	}
	if !hasLineContaining(m.lines, lineError, "candidate unavailable") {
		t.Fatalf("picker failure missing: %#v", m.lines)
	}
}

func TestModelCommandWithoutNameOpensConfiguredPicker(t *testing.T) {
	m := newTestModel(t)
	m.deps.ModelCatalog = []ModelCatalogEntry{{CanonicalName: "configured", DisplayName: "Configured"}}
	m.deps.SwitchModel = func(context.Context, *chat.Session, string) (ModelSwitchResult, error) {
		return ModelSwitchResult{}, errors.New("not reached")
	}

	next, _ := m.submit("/model")
	m = next.(*model)
	if !m.modelPickerOpen() {
		t.Fatal("/model without a name did not open configured picker")
	}
}

func TestModelNoCatalogKeepsFreeFormFallback(t *testing.T) {
	m := newTestModel(t)
	var callbackName string
	m.deps.SwitchModel = func(_ context.Context, _ *chat.Session, name string) (ModelSwitchResult, error) {
		callbackName = name
		return ModelSwitchResult{Status: StatusInfo{Model: "openai/" + name}, SessionOpts: chat.SessionOptions{ModelName: name}}, nil
	}

	next, _ := m.submit("/model custom-endpoint-deployment")
	m = next.(*model)
	if callbackName != "custom-endpoint-deployment" {
		t.Fatalf("free-form callback name = %q", callbackName)
	}
	if m.modelPickerOpen() {
		t.Fatal("free-form model switch unexpectedly opened picker")
	}
	if !hasLineContaining(m.lines, lineSystem, "model switched to custom-endpoint-deployment") {
		t.Fatalf("free-form confirmation missing: %#v", m.lines)
	}
}

func boolPointer(value bool) *bool {
	return &value
}
