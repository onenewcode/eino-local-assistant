package tui

import (
	"context"
	"errors"
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
				MaxOutputTokens:     8192,
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
	if !hasLineContaining(m.lines, lineSystem, "model switched to Coding 5.2 (openai/gpt-5.2-coding)") {
		t.Fatalf("picker confirmation missing: %#v", m.lines)
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
	if !hasLineContaining(m.lines, lineSystem, "model switched to openai/custom-endpoint-deployment") {
		t.Fatalf("free-form confirmation missing: %#v", m.lines)
	}
}

func boolPointer(value bool) *bool {
	return &value
}
