package tui

import "strings"

type slashAction int

const (
	slashNone slashAction = iota
	slashHelp
	slashExit
	slashClear
	slashStatus
	slashContext
	slashCompact
	slashNew
	slashSessions
	slashResume
	slashTitle
	slashDelete
	slashQueue
	slashUsage
	slashPermissions
	slashMemory
	slashUnknown
)

// slashCommand is one row in the live composer suggestion menu.
// Name is the canonical token written on accept; Aliases only participate in prefix match.
type slashCommand struct {
	Name        string
	Aliases     []string
	Description string
	NeedsArg    bool
}

// maxSlashMenuRows caps how many suggestion rows are painted at once.
const maxSlashMenuRows = 8

// slashCatalog is the single source of truth for menu rows (order matches help).
func slashCatalog() []slashCommand {
	return []slashCommand{
		{Name: "/help", Aliases: []string{"/?"}, Description: "show this help"},
		{Name: "/status", Description: "model, session, tokens, cost, max_step, context"},
		{Name: "/usage", Description: "toggle turn usage footer (on|off|toggle)", NeedsArg: true},
		{Name: "/context", Description: "context budget, checkpoints, and compaction status"},
		{Name: "/compact", Description: "summarize stable turns and free context", NeedsArg: true},
		{Name: "/sessions", Description: "list saved sessions (tokens/cost)"},
		{Name: "/new", Description: "start a new session", NeedsArg: true},
		{Name: "/resume", Description: "resume a saved session ([--recover] after confirming prior process stopped)", NeedsArg: true},
		{Name: "/delete", Description: "delete a saved session (not the active one)", NeedsArg: true},
		{Name: "/title", Description: "rename the current session", NeedsArg: true},
		{Name: "/queue", Description: "list queued follow-ups (or: clear)", NeedsArg: true},
		{Name: "/permissions", Aliases: []string{"/policy"}, Description: "shell/apply_patch policy and session allows"},
		{Name: "/memory", Description: "project memory (list|add|delete|accept|on|off|generate|status|rebuild)", NeedsArg: true},
		{Name: "/clear", Description: "clear screen and start a new thread (previous thread retained)"},
		{Name: "/exit", Aliases: []string{"/quit"}, Description: "quit"},
	}
}

// slashMenuActive reports whether the composer value should show slash suggestions.
// Active for a single-line command token with optional leading indent, leading "/",
// and no whitespace yet. Trailing space counts as args phase (menu closes) so
// completing NeedsArg commands with a trailing space hides suggestions.
func slashMenuActive(input string) bool {
	if strings.ContainsAny(input, "\n\r") {
		return false
	}
	// Allow leading indent only; keep trailing spaces so "/new " closes the menu.
	token := strings.TrimLeft(input, " \t")
	if token == "" || !strings.HasPrefix(token, "/") {
		return false
	}
	// Args phase: any whitespace means the command token is finished.
	if strings.ContainsAny(token, " \t") {
		return false
	}
	return true
}

// filterSlashCommands returns catalog rows whose Name or Alias has prefix match.
// prefix should already be a menu-active token (e.g. "/", "/c"); empty/inactive → nil.
// Results keep catalog order and are deduped by canonical Name.
func filterSlashCommands(prefix string) []slashCommand {
	if !slashMenuActive(prefix) {
		return nil
	}
	p := strings.ToLower(strings.TrimLeft(prefix, " \t"))
	if p == "" {
		return nil
	}
	out := make([]slashCommand, 0, len(slashCatalog()))
	for _, cmd := range slashCatalog() {
		if slashCommandMatches(cmd, p) {
			out = append(out, cmd)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func slashCommandMatches(cmd slashCommand, prefix string) bool {
	if strings.HasPrefix(strings.ToLower(cmd.Name), prefix) {
		return true
	}
	for _, alias := range cmd.Aliases {
		if strings.HasPrefix(strings.ToLower(alias), prefix) {
			return true
		}
	}
	return false
}

// completeSlashCommand returns the text written into the composer on accept.
// NeedsArg commands get a trailing space so the user can type the argument.
func completeSlashCommand(cmd slashCommand) string {
	if cmd.NeedsArg {
		return cmd.Name + " "
	}
	return cmd.Name
}

func parseSlash(input string) (slashAction, string) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" || !strings.HasPrefix(trimmed, "/") {
		return slashNone, trimmed
	}

	fields := strings.Fields(trimmed)
	cmd := strings.ToLower(fields[0])
	arg := ""
	if len(fields) > 1 {
		// Keep original casing for title / resume id; strip only the command.
		arg = strings.TrimSpace(trimmed[len(fields[0]):])
	}

	switch cmd {
	case "/help", "/?":
		return slashHelp, arg
	case "/exit", "/quit":
		return slashExit, arg
	case "/clear":
		return slashClear, arg
	case "/status":
		return slashStatus, arg
	case "/usage":
		return slashUsage, arg
	case "/context":
		return slashContext, arg
	case "/compact":
		return slashCompact, arg
	case "/new":
		return slashNew, arg
	case "/sessions":
		return slashSessions, arg
	case "/resume":
		return slashResume, arg
	case "/title":
		return slashTitle, arg
	case "/delete":
		return slashDelete, arg
	case "/queue":
		return slashQueue, arg
	case "/permissions", "/policy":
		return slashPermissions, arg
	case "/memory":
		return slashMemory, arg
	default:
		return slashUnknown, trimmed
	}
}

func helpText() string {
	return strings.Join([]string{
		"Commands:",
		"  /help              show this help",
		"  /status            model, session, tokens, cost, max_step, context",
		"  /usage [on|off]    show/toggle per-turn API usage footer (default on)",
		"  /context           context budget, checkpoints, and compaction status",
		"  /compact [focus]   summarize stable turns and free context",
		"  /sessions          list saved sessions (tokens/cost)",
		"  /new [title]       start a new session",
		"  /resume <id> [--recover]  resume a saved session; explicitly recover an interrupted operation",
		"  /delete <id>       delete a saved session (not the active one)",
		"  /title <text>      rename the current session",
		"  /queue             list queued follow-ups",
		"  /queue clear       drop all queued follow-ups",
		"  /permissions       tool policy, workspace clamp, session allows",
		"  /memory            project memory (list|add|delete|accept|on|off|generate|status|rebuild)",
		"  /clear             clear screen and start a new thread (previous thread retained)",
		"  /exit              quit",
		"",
		"Keys:",
		"  enter     send message (queues if a turn is running)",
		"  up/down   input history (first/last composer line); slash menu when open",
		"  tab       complete selected slash command",
		"  ctrl+j    newline",
		"  ctrl+t    show/hide complex task progress when available",
		"  pgup/pgdn scroll transcript (or review a long host-escalation command)",
		"  home/end  jump to top / bottom of transcript",
		"  esc       dismiss slash menu, deny approval, or interrupt turn/compaction",
		"  ctrl+c    interrupt turn/compaction, or quit when idle",
		"",
		"While busy, /help /context /status /usage /sessions /queue /permissions /memory status|list run immediately; /queue clear drops follow-ups.",
		"Mutative commands (/compact /clear /new /resume /title /delete /exit) cannot be queued.",
		"shell/apply_patch may prompt for approval (once / session / deny). Status shows cmd=ask|auto.",
		"Sessions auto-save each successful turn. Costs use provider usage when available.",
		"Persistent memory is project-scoped (not /resume). See docs/memory.md.",
	}, "\n")
}
