package tui

import (
	"strings"
	"testing"
)

func TestHelpTextDocumentsEscBacktrackAndExistingEscActions(t *testing.T) {
	help := helpText()
	for _, phrase := range []string{
		"idle: first arms backtrack; second opens history prompt selector",
		"selector Esc cancels",
		"busy Esc interrupts turn/compaction",
		"approval Esc denies",
		"slash menu Esc dismisses",
	} {
		if !strings.Contains(help, phrase) {
			t.Errorf("help text missing %q:\n%s", phrase, help)
		}
	}

	foundHelp := false
	for _, command := range slashCatalog() {
		if command.Name == "/help" && !strings.Contains(command.Description, "Esc") {
			t.Fatalf("/help description should mention Esc behavior: %q", command.Description)
		} else if command.Name == "/help" {
			foundHelp = true
		}
	}
	if !foundHelp {
		t.Fatal("slash catalog is missing /help")
	}
}

func TestBacktrackIsEscOnlyAndNeverAQueuedSlashCommand(t *testing.T) {
	if action, _ := parseSlash("/backtrack"); action != slashUnknown {
		t.Fatalf("/backtrack unexpectedly parses as %v", action)
	}
	if got := filterSlashCommands("/back"); got != nil {
		t.Fatalf("/backtrack unexpectedly appears in slash menu: %#v", got)
	}
	if got := classifyBusyAction(slashBacktrack, ""); got != busyInputReject {
		t.Fatalf("backtrack action busy disposition = %v, want reject", got)
	}
	if isQueueableInput(backtrackKeyInput) {
		t.Fatal("Esc/backtrack must not enter the busy FIFO")
	}
}

func TestBusyInputClassificationKeepsExistingBoundaries(t *testing.T) {
	cases := []struct {
		input string
		want  busyInputDisposition
	}{
		{input: "follow up", want: busyInputEnqueue},
		{input: "/unknown", want: busyInputEnqueue},
		{input: "/help", want: busyInputExecuteImmediately},
		{input: "/context", want: busyInputExecuteImmediately},
		{input: "/status", want: busyInputExecuteImmediately},
		{input: "/rules", want: busyInputExecuteImmediately},
		{input: "/btw question", want: busyInputExecuteImmediately},
		{input: "/usage off", want: busyInputExecuteImmediately},
		{input: "/sessions", want: busyInputExecuteImmediately},
		{input: "/queue clear", want: busyInputExecuteImmediately},
		{input: "/permissions", want: busyInputExecuteImmediately},
		{input: "/compact", want: busyInputReject},
		{input: "/clear", want: busyInputReject},
		{input: "/new topic", want: busyInputReject},
		{input: "/resume sess-1", want: busyInputReject},
		{input: "/fork", want: busyInputReject},
		{input: "/title title", want: busyInputReject},
		{input: "/delete sess-1", want: busyInputReject},
		{input: "/exit", want: busyInputReject},
	}
	for _, tc := range cases {
		if got := classifyBusyInput(tc.input); got != tc.want {
			t.Errorf("classifyBusyInput(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}
