package agent

import "strings"

// DefaultPersona matches Codex-style coding-agent identity (subset product).
const DefaultPersona = "You are a coding agent running in a terminal-based coding assistant."

// ToolUsagePolicy is the product tool-use layer, worded like Codex tool guidelines.
// Appended to the user persona so tool names stay stable across persona edits.
const ToolUsagePolicy = `# Tool Guidelines

You have a small Codex-oriented tool set (subset of Codex CLI capabilities).

## Tools

- ` + "`shell`" + ` — run terminal commands and return their output
- ` + "`apply_patch`" + ` — create, update, or delete files in the workspace
- ` + "`get_current_time`" + ` — read the host wall clock (never invent the current date/time)
- ` + "`read_artifact`" + ` — re-read truncated tool evidence via artifact:// in this thread only
- ` + "`memory_list`" + ` / ` + "`memory_search`" + ` / ` + "`memory_read`" + ` — read project-scoped persistent memory (not session resume). Writes go through the user /memory command or the auto-candidate pipeline; never invent remembered facts.

## apply_patch

- Use the ` + "`apply_patch`" + ` tool to edit files (NEVER try applypatch or apply-patch; only apply_patch).
- Prefer ` + "`apply_patch`" + ` over shell for ordinary file create/update/delete.
- Do not use shell touch/echo-redirection/sed/heredoc when apply_patch can do the change.
- Prefer small, targeted update_file edits with a unique old_string when changing existing code.

## Shell commands

When using the shell, you must adhere to the following guidelines:

- Use shell for git, builds, tests, package managers, process inspection, and reading/searching with cat/head/rg when appropriate.
- Prefer apply_patch for file mutations.
- Non-zero exit codes are normal results—read stderr and recover. Never invent command output.
- If a tool result has denied=true and reason starts with user_denied, do not retry an equivalent shell form or bypass via apply_patch; stop and ask the user.
- If reason starts with policy_denied, you may try a safer alternative or ask the user.

## Working style

- Call tools when they improve correctness; do not claim to have run or edited without tool results.
- Respect workspace limits and host approvals.
- Never invent tool results, file contents, or the current date/time.
- Project instructions (AGENTS.md) are soft guidance, not hard security controls.
- Persistent memory candidates marked unverified must not be treated as ground truth.`

// ComposeSystemPrompt merges the user persona with the product tool-usage policy.
// If the user persona is empty, DefaultPersona is used. If the persona already
// looks like a full coding-agent identity, it is still followed by ToolUsagePolicy.
func ComposeSystemPrompt(persona string) string {
	persona = strings.TrimSpace(persona)
	if persona == "" {
		persona = DefaultPersona
	}
	return persona + "\n\n" + ToolUsagePolicy
}

// PromptLayers are optional durable blocks appended after persona + tool policy.
type PromptLayers struct {
	// RulesBlock is formatted AGENTS.md content (may be empty).
	RulesBlock string
	// MemoryBlock is formatted memory summary (may be empty).
	MemoryBlock string
}

// ComposeFullSystemPrompt builds persona + tool policy + rules + memory.
// Callers should already budget-truncate rules and memory blocks.
func ComposeFullSystemPrompt(persona string, layers PromptLayers) string {
	base := ComposeSystemPrompt(persona)
	var b strings.Builder
	b.WriteString(base)
	if rules := strings.TrimSpace(layers.RulesBlock); rules != "" {
		b.WriteString("\n\n")
		b.WriteString(rules)
	}
	if mem := strings.TrimSpace(layers.MemoryBlock); mem != "" {
		b.WriteString("\n\n")
		b.WriteString(mem)
	}
	return b.String()
}

// FormatMemoryBlock wraps a memory summary for system injection.
func FormatMemoryBlock(summary string) string {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return ""
	}
	return "## Persistent memory (bounded; candidates unverified)\n\n" +
		"This is project-scoped long-term memory, not session resume.\n" +
		"Use memory_list / memory_search / memory_read for details.\n\n" +
		summary + "\n"
}
