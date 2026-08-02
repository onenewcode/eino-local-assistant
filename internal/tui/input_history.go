package tui

import (
	"strings"

	"github.com/cloudwego/eino/schema"
)

// maxInputHistory caps remembered composer submissions (oldest dropped first).
const maxInputHistory = 100

// inputHistory is a shell-style composer history (oldest → newest).
// pos == -1 means "live draft"; otherwise entries[pos] is shown.
type inputHistory struct {
	entries []string
	pos     int // -1 = draft mode
	draft   string
}

func newInputHistory() inputHistory {
	return inputHistory{pos: -1}
}

// push records a submitted line. Consecutive duplicates are skipped.
func (h *inputHistory) push(input string) {
	input = strings.TrimSpace(input)
	if input == "" {
		return
	}
	if n := len(h.entries); n > 0 && h.entries[n-1] == input {
		h.pos = -1
		h.draft = ""
		return
	}
	h.entries = append(h.entries, input)
	if len(h.entries) > maxInputHistory {
		h.entries = append([]string(nil), h.entries[len(h.entries)-maxInputHistory:]...)
	}
	h.pos = -1
	h.draft = ""
}

// browsing reports whether the composer is currently showing a history entry.
func (h *inputHistory) browsing() bool {
	return h.pos >= 0
}

// current returns the entry under pos, or "" when in draft mode.
func (h *inputHistory) current() string {
	if h.pos < 0 || h.pos >= len(h.entries) {
		return ""
	}
	return h.entries[h.pos]
}

// up moves to an older entry. currentValue is the live composer text when leaving draft mode.
// ok is false when there is no older entry (composer unchanged).
func (h *inputHistory) up(currentValue string) (next string, ok bool) {
	if len(h.entries) == 0 {
		return "", false
	}
	if h.pos < 0 {
		h.draft = currentValue
		h.pos = len(h.entries) - 1
		return h.entries[h.pos], true
	}
	if h.pos == 0 {
		return h.entries[0], false
	}
	h.pos--
	return h.entries[h.pos], true
}

// down moves toward the live draft. ok is false when already on draft / nothing to do.
func (h *inputHistory) down() (next string, ok bool) {
	if h.pos < 0 {
		return "", false
	}
	if h.pos+1 >= len(h.entries) {
		h.pos = -1
		next = h.draft
		h.draft = ""
		return next, true
	}
	h.pos++
	return h.entries[h.pos], true
}

// exitBrowse drops history navigation after the user edits the composer.
func (h *inputHistory) exitBrowse() {
	h.pos = -1
	h.draft = ""
}

// clear wipes history (e.g. after /new).
func (h *inputHistory) clear() {
	h.entries = nil
	h.pos = -1
	h.draft = ""
}

// seedFromMessages loads user message contents (oldest first). Replaces entries.
func (h *inputHistory) seedFromMessages(hist []*schema.Message) {
	h.clear()
	for _, msg := range hist {
		if msg == nil || msg.Role != schema.User {
			continue
		}
		h.push(msg.Content)
	}
}
