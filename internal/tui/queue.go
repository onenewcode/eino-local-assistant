package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// maxQueue caps follow-up messages queued while a turn is running.
const maxQueue = 32

// queuePreviewRunes is the max preview length for the "queued (n): …" system line.
const queuePreviewRunes = 48

// backtrackKeyInput is the internal representation used if a future input
// parser routes Esc through the text classifier. Esc is a key action, not a
// user-facing slash command.
const backtrackKeyInput = "\x1b"

// busyInputDisposition describes how a submitted input behaves while an
// operation is in flight. The TUI dispatcher owns executing immediate inputs;
// this package only classifies them so queue policy remains testable.
type busyInputDisposition int

const (
	busyInputEnqueue busyInputDisposition = iota
	busyInputExecuteImmediately
	busyInputReject
)

// enqueueFollowUp appends a trimmed follow-up. Returns false if the queue is full.
func enqueueFollowUp(queue []string, input string) ([]string, bool) {
	input = strings.TrimSpace(input)
	if input == "" {
		return queue, true
	}
	if len(queue) >= maxQueue {
		return queue, false
	}
	return append(queue, input), true
}

func queuePreview(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}
	// Collapse internal whitespace for a one-line preview.
	fields := strings.Fields(input)
	compact := strings.Join(fields, " ")
	if utf8.RuneCountInString(compact) <= queuePreviewRunes {
		return compact
	}
	runes := []rune(compact)
	return string(runes[:queuePreviewRunes-1]) + "…"
}

func queuedSystemLine(n int, input string) string {
	return fmt.Sprintf("queued (%d): %s", n, queuePreview(input))
}

// classifyBusyAction keeps context/session mutations behind an idle boundary,
// while allowing operational inspection and queue control to run immediately.
// Internal key actions must be classified explicitly so they cannot fall
// through to the natural-language FIFO path.
func classifyBusyAction(action slashAction, arg string) busyInputDisposition {
	switch action {
	case slashBacktrack:
		return busyInputReject
	case slashHelp, slashContext, slashStatus, slashRules, slashSide, slashUsage, slashSessions, slashQueue, slashPermissions:
		return busyInputExecuteImmediately
	case slashMemory:
		// list/status are read-only; mutations must wait for idle.
		if memoryCommandAllowsBusy(arg) {
			return busyInputExecuteImmediately
		}
		return busyInputReject
	case slashCompact, slashClear, slashNew, slashResume, slashFork, slashTitle, slashDelete, slashExit:
		return busyInputReject
	default:
		return busyInputEnqueue
	}
}

// classifyBusyInput applies the busy policy to submitted text and to the
// internal Esc representation used by a future backtrack dispatcher.
func classifyBusyInput(input string) busyInputDisposition {
	if input == backtrackKeyInput {
		return classifyBusyAction(slashBacktrack, "")
	}
	action, arg := parseSlash(input)
	return classifyBusyAction(action, arg)
}

// isQueueableInput reports whether input belongs in the FIFO follow-up queue.
func isQueueableInput(input string) bool {
	return classifyBusyInput(input) == busyInputEnqueue
}

// isImmediatelyExecutableWhileBusy identifies local/read-only commands that
// the TUI may dispatch without waiting for the current model turn to finish.
func isImmediatelyExecutableWhileBusy(input string) bool {
	return classifyBusyInput(input) == busyInputExecuteImmediately
}

func formatQueueList(queue []string) string {
	if len(queue) == 0 {
		return "queue empty"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Queue (%d):\n", len(queue))
	for i, item := range queue {
		fmt.Fprintf(&b, "  %d. %s\n", i+1, queuePreview(item))
	}
	b.WriteString("Use /queue clear to drop all.")
	return strings.TrimRight(b.String(), "\n")
}
