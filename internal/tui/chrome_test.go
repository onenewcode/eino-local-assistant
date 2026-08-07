package tui

import (
	"strings"
	"testing"
)

func TestViewHasComposerBorderAndSinglePrompt(t *testing.T) {
	m := newTestModel(t)
	m.width = 60
	m.height = 20
	m.layout()
	view := m.View()
	if !strings.Contains(view, "╭") {
		t.Fatalf("expected rounded border in view:\n%s", view)
	}
	// The model footer remains visible beneath the composer.
	if !strings.Contains(view, "test-model") {
		t.Fatalf("status missing:\n%s", view)
	}
	// Single-line empty composer: at most one › prompt glyph in chrome.
	if c := strings.Count(view, "›"); c > 2 {
		t.Fatalf("too many › prompts (%d):\n%s", c, view)
	}
	if strings.LastIndex(view, "test-model") < strings.LastIndex(view, "╰") {
		t.Fatalf("status footer must follow composer:\n%s", view)
	}
	t.Log("\n" + view)
}

func TestRenderComposerUsesQuietBorder(t *testing.T) {
	out := renderComposer(40, "› hello")
	if !strings.Contains(out, "╭") {
		t.Fatalf("expected rounded border: %q", out)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("content missing: %q", out)
	}
}
