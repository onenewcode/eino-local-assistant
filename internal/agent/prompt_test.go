package agent

import (
	"strings"
	"testing"
)

func TestComposeSystemPromptAppendsToolPolicy(t *testing.T) {
	got := ComposeSystemPrompt("你是一个严谨、实用的编程助手。")
	if !strings.HasPrefix(got, "你是一个严谨、实用的编程助手。") {
		t.Fatalf("persona missing: %q", got)
	}
	for _, want := range []string{
		"Tool Guidelines",
		"shell",
		"apply_patch",
		"get_current_time",
		"user_denied",
		"never invent tool results",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("policy missing %q", want)
		}
	}
}

func TestComposeSystemPromptKeepsBuiltInPolicyCompact(t *testing.T) {
	got := ComposeSystemPrompt("")
	if len([]rune(got)) > 2600 {
		t.Fatalf("built-in prompt is too large (%d runes)", len([]rune(got)))
	}
}

func TestComposeSystemPromptDefaultPersona(t *testing.T) {
	got := ComposeSystemPrompt("  ")
	if !strings.HasPrefix(got, DefaultPersona) {
		t.Fatalf("default persona missing: %q", got)
	}
}
