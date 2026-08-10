package tui

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// maxQueue caps follow-up messages queued while a turn is running.
const maxQueue = 32

// queuePreviewRunes is the max preview length used by queue displays.
const queuePreviewRunes = 48

const (
	queueCommandUsage         = "usage: /queue | /queue clear | /queue drop <1-based-index> | /queue edit <1-based-index> <new text> | /queue resume"
	queueEmptyMessage         = "queue empty"
	queueDropIndexError       = "queue drop index must be a positive integer"
	queueDropRangeError       = "queue drop index out of range"
	queueEditTextError        = "queue edit text must not be empty"
	queueEditRangeError       = "queue edit index out of range"
	queueEditIndexError       = "queue edit index must be a positive integer"
	queueEditAdmissionError   = "queue edit rejected: new text cannot be a mutative or immediately executable slash command"
	queueResumeBusyMessage    = "queue resume unavailable: current operation is still running; queue and pause unchanged"
	permissionModeBusyMessage = "permission mode changes are unavailable while busy; retry when idle"
	permissionModeSideMessage = "permission mode changes are unavailable while a side question is pending; retry when it finishes"
)

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
	busyInputSteer
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

// classifyBusyAction keeps context/session mutations behind an idle boundary,
// while allowing operational inspection and queue control to run immediately.
// Internal key actions must be classified explicitly so they cannot fall
// through to the natural-language FIFO path.
func classifyBusyAction(action slashAction, arg string) busyInputDisposition {
	switch action {
	case slashBacktrack:
		return busyInputReject
	case slashSteer:
		// Steering targets the existing regular turn directly. It is never a
		// FIFO follow-up, including when the core rejects the admission.
		return busyInputSteer
	case slashHelp, slashContext, slashStatus, slashStatusLine, slashGoal, slashTasks, slashDiff, slashRules, slashSide, slashUsage, slashSessions, slashQueue:
		return busyInputExecuteImmediately
	case slashReview:
		return busyInputReject
	case slashPermissions:
		if strings.TrimSpace(arg) == "" {
			return busyInputExecuteImmediately
		}
		return busyInputReject
	case slashPlan:
		return busyInputReject
	case slashModel:
		return busyInputReject
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

func parseQueueDropIndex(fields []string) (int, string) {
	if len(fields) != 2 {
		return 0, queueCommandUsage
	}
	index, err := strconv.Atoi(fields[1])
	if err != nil || index < 1 {
		return 0, queueDropIndexError
	}
	return index, ""
}

func parseQueueEdit(arg string) (int, string, string) {
	arg = strings.TrimSpace(arg)
	fields := strings.Fields(arg)
	if len(fields) < 2 {
		return 0, "", queueCommandUsage
	}
	index, err := strconv.Atoi(fields[1])
	if err != nil || index < 1 {
		return 0, "", queueEditIndexError
	}
	if len(fields) < 3 {
		return 0, "", queueEditTextError
	}

	// Match enqueue admission: trim only the replacement's outer whitespace;
	// preserve internal spacing in the stored follow-up.
	remaining := strings.TrimSpace(arg[len(fields[0]):])
	newText := strings.TrimSpace(remaining[len(fields[1]):])
	if newText == "" {
		return 0, "", queueEditTextError
	}
	return index, newText, ""
}

func dropQueuedFollowUp(queue []string, index int) ([]string, string, bool) {
	if index < 1 || index > len(queue) {
		return queue, "", false
	}
	dropped := queue[index-1]
	copy(queue[index-1:], queue[index:])
	queue[len(queue)-1] = ""
	return queue[:len(queue)-1], dropped, true
}

// promoteQueuedFollowUp moves a 1-based queue entry to the FIFO head.
func promoteQueuedFollowUp(queue []string, index int) ([]string, bool) {
	if index < 1 || index > len(queue) {
		return queue, false
	}
	if index == 1 {
		return queue, true
	}
	selected := queue[index-1]
	copy(queue[1:index], queue[:index-1])
	queue[0] = selected
	return queue, true
}

func editQueuedFollowUp(queue []string, index int, newText string) ([]string, bool) {
	if index < 1 || index > len(queue) {
		return queue, false
	}
	queue[index-1] = newText
	return queue, true
}

func formatQueueList(queue []string, paused bool) string {
	if len(queue) == 0 {
		if paused {
			return queueEmptyMessage + " (paused); use /queue resume to continue"
		}
		return queueEmptyMessage
	}
	var b strings.Builder
	state := ""
	if paused {
		state = " [paused]"
	}
	fmt.Fprintf(&b, "Queue (%d)%s:\n", len(queue), state)
	for i, item := range queue {
		fmt.Fprintf(&b, "  %d. %s\n", i+1, queuePreview(item))
	}
	b.WriteString("Use /queue clear to drop all, /queue drop <1-based-index> to drop one, /queue edit <1-based-index> <new text> to edit one, or /queue resume to continue.")
	return strings.TrimRight(b.String(), "\n")
}

func queuePausedLine(n int) string {
	return fmt.Sprintf("queue paused after turn error; %d queued follow-ups retained; use /queue resume to continue", n)
}
