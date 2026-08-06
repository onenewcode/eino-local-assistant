package tools

import (
	"strings"
	"unicode"
)

// Decision is the three-state authorization outcome for a tool call.
type Decision string

const (
	DecisionAllow Decision = "allow"
	DecisionAsk   Decision = "ask"
	DecisionDeny  Decision = "deny"
)

// Evaluation is the effective shell authorization result.
type Evaluation struct {
	Decision     Decision
	RuleID       string
	Reason       string
	PolicyPrompt bool // true only for a matching Codex decision = "prompt" rule.
}

// HasShellMetacharacters reports whether command likely embeds shell control
// syntax beyond a simple argv list. Conservative: false positives only force ask.
func HasShellMetacharacters(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	for _, op := range []string{"&&", "||", ">>", "<<", "2>", "&>", "$(", "`"} {
		if strings.Contains(command, op) {
			return true
		}
	}
	if strings.ContainsAny(command, "\n\r;|<>&") {
		return true
	}
	if strings.Contains(command, "$") {
		return true
	}
	if strings.ContainsAny(command, "()") {
		return true
	}
	if strings.ContainsAny(command, "*?[]") {
		return true
	}
	for _, r := range command {
		if r < 32 && r != '\t' {
			return true
		}
		if !unicode.IsPrint(r) && r != '\t' {
			return true
		}
	}
	return false
}
