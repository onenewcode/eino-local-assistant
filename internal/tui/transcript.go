package tui

import (
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
)

// hasReplayableTranscript reports whether a transcript contains user or non-empty assistant
// content worth showing in the on-screen transcript.
func hasReplayableTranscript(transcript []*schema.Message) bool {
	for _, msg := range transcript {
		if msg == nil {
			continue
		}
		switch msg.Role {
		case schema.User:
			return true
		case schema.Assistant:
			if strings.TrimSpace(msg.Content) != "" {
				return true
			}
		}
	}
	return false
}

// seedLinesFromTranscript builds transcript lines from a loaded replay window.
// Skips system messages; replays user + non-empty assistant; ends with a separator.
// banner is the primary system line (e.g. "resumed id (n messages)"); title is optional.
func seedLinesFromTranscript(transcript []*schema.Message, banner, title string) []transcriptLine {
	var lines []transcriptLine
	if banner != "" {
		lines = append(lines, transcriptLine{kind: lineSystem, text: banner})
	}
	if title != "" {
		lines = append(lines, transcriptLine{kind: lineSystem, text: "title: " + title})
	}
	for _, msg := range transcript {
		if msg == nil {
			continue
		}
		switch msg.Role {
		case schema.User:
			lines = append(lines, transcriptLine{kind: lineUser, text: msg.Content})
		case schema.Assistant:
			if strings.TrimSpace(msg.Content) != "" {
				lines = append(lines, transcriptLine{kind: lineAssistant, text: msg.Content})
			}
		}
	}
	lines = append(lines, transcriptLine{kind: lineSep, text: ""})
	return lines
}

func resumeBanner(id string, msgCount int) string {
	return fmt.Sprintf("resumed %s (%d messages)", id, msgCount)
}

func forkBanner(childID, sourceID, lastTurnID string, msgCount int) string {
	boundary := ""
	if lastTurnID != "" {
		boundary = " at " + lastTurnID
	}
	return fmt.Sprintf("forked %s from %s%s (%d messages)", childID, sourceID, boundary, msgCount)
}
