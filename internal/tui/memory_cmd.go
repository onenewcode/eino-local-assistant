package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// projectContextRefreshHint is shown after durable memory mutations so users
// know when AGENTS.md / memory re-enter the system prefix.
const projectContextRefreshHint = "system injection refreshes on /new or /clear"

const memoryResetUsage = "usage: /memory reset --confirm"

func (m *model) cmdMemory(arg string) (tea.Model, tea.Cmd) {
	if m.deps.Memory == nil {
		m.appendLine(lineError, "memory store is not configured")
		return m, nil
	}
	arg = strings.TrimSpace(arg)
	fields := strings.Fields(arg)
	cmd := ""
	if len(fields) > 0 {
		cmd = strings.ToLower(fields[0])
	}
	rest := ""
	if len(fields) > 1 {
		rest = strings.TrimSpace(arg[len(fields[0]):])
	}

	// Mutations require idle so store writes cannot race a concurrent turn.
	if memoryCommandMutates(cmd) && m.mode != modeIdle {
		m.appendLine(lineError, "busy: finish or interrupt the current turn first")
		return m, nil
	}

	switch cmd {
	case "", "list":
		entries, err := m.deps.Memory.ListActive()
		if err != nil {
			m.appendLine(lineError, "memory list: "+err.Error())
			return m, nil
		}
		if len(entries) == 0 {
			m.appendLine(lineSystem, "memory: (empty)  — use /memory add <text>")
			m.appendLine(lineSep, "")
			return m, nil
		}
		var b strings.Builder
		b.WriteString(fmt.Sprintf("memory: %d active\n", len(entries)))
		for _, e := range entries {
			fmt.Fprintf(&b, "  [%s] %s  %s=%s\n", e.Trust, e.ID, e.Key, e.Claim)
		}
		m.appendLine(lineSystem, strings.TrimRight(b.String(), "\n"))
		m.appendLine(lineSep, "")
		return m, nil
	case "status":
		rep, err := m.deps.Memory.Report()
		if err != nil {
			m.appendLine(lineError, "memory status: "+err.Error())
			return m, nil
		}
		last := "never"
		if rep.LastConsolidate != nil {
			last = rep.LastConsolidate.Format(time.RFC3339)
		}
		msg := fmt.Sprintf(
			"memory status\n  root: %s\n  use: %v  generate: %v\n  user: %d  candidates: %d\n  last consolidate: %s",
			rep.Root, rep.UseEnabled, rep.GenerateEnabled, rep.UserActive, rep.CandidateActive, last,
		)
		if rep.LastError != "" {
			msg += "\n  last error: " + rep.LastError
		}
		m.appendLine(lineSystem, msg)
		m.appendLine(lineSep, "")
		return m, nil
	case "add":
		if rest == "" {
			m.appendLine(lineError, "usage: /memory add [key=slug] <claim>")
			return m, nil
		}
		key, claim := parseMemoryAdd(rest)
		e, err := m.deps.Memory.AddUser(key, claim)
		if err != nil {
			m.appendLine(lineError, "memory add: "+err.Error())
			return m, nil
		}
		// Durable store updates immediately; system prefix stays frozen until
		// /new or /clear. memory_* tools still see live entries.
		m.appendLine(lineSystem, fmt.Sprintf("memory saved: %s (%s); %s", e.ID, e.Key, projectContextRefreshHint))
		m.appendLine(lineSep, "")
		return m, nil
	case "update", "edit", "correct":
		idOrKey, claim := parseMemoryUpdate(rest)
		if idOrKey == "" || claim == "" {
			m.appendLine(lineError, "usage: /memory update <id|key> <claim>")
			return m, nil
		}
		e, err := m.deps.Memory.UpdateUser(idOrKey, claim)
		if err != nil {
			m.appendLine(lineError, "memory update: "+err.Error())
			return m, nil
		}
		m.appendLine(lineSystem, fmt.Sprintf(
			"memory updated: %s (%s), supersedes %s; %s",
			e.ID, e.Key, e.Supersedes, projectContextRefreshHint,
		))
		m.appendLine(lineSep, "")
		return m, nil
	case "delete":
		if rest == "" {
			m.appendLine(lineError, "usage: /memory delete <id|key>")
			return m, nil
		}
		e, err := m.deps.Memory.Delete(rest)
		if err != nil {
			m.appendLine(lineError, "memory delete: "+err.Error())
			return m, nil
		}
		m.appendLine(lineSystem, fmt.Sprintf("memory deleted: %s (%s); %s", e.ID, e.Key, projectContextRefreshHint))
		m.appendLine(lineSep, "")
		return m, nil
	case "accept":
		if rest == "" {
			m.appendLine(lineError, "usage: /memory accept <id>")
			return m, nil
		}
		e, err := m.deps.Memory.Accept(rest)
		if err != nil {
			m.appendLine(lineError, "memory accept: "+err.Error())
			return m, nil
		}
		m.appendLine(lineSystem, fmt.Sprintf("memory accepted as user trust: %s (%s); %s", e.ID, e.Key, projectContextRefreshHint))
		m.appendLine(lineSep, "")
		return m, nil
	case "on":
		if err := m.deps.Memory.SetUseEnabled(true); err != nil {
			m.appendLine(lineError, "memory on: "+err.Error())
			return m, nil
		}
		m.appendLine(lineSystem, "memory use: on (summary inject + read tools); "+projectContextRefreshHint)
		m.appendLine(lineSep, "")
		return m, nil
	case "off":
		if err := m.deps.Memory.SetUseEnabled(false); err != nil {
			m.appendLine(lineError, "memory off: "+err.Error())
			return m, nil
		}
		m.appendLine(lineSystem, "memory use: off; "+projectContextRefreshHint)
		m.appendLine(lineSep, "")
		return m, nil
	case "generate":
		sub := strings.ToLower(strings.TrimSpace(rest))
		switch sub {
		case "on", "":
			if err := m.deps.Memory.SetGenerateEnabled(true); err != nil {
				m.appendLine(lineError, "memory generate: "+err.Error())
				return m, nil
			}
			m.appendLine(lineSystem, "memory generate: on (idle auto-extract)")
		case "off":
			if err := m.deps.Memory.SetGenerateEnabled(false); err != nil {
				m.appendLine(lineError, "memory generate: "+err.Error())
				return m, nil
			}
			m.appendLine(lineSystem, "memory generate: off")
		default:
			m.appendLine(lineError, "usage: /memory generate on|off")
			return m, nil
		}
		m.appendLine(lineSep, "")
		return m, nil
	case "rebuild":
		if err := m.deps.Memory.RebuildSummary(); err != nil {
			m.appendLine(lineError, "memory rebuild: "+err.Error())
			return m, nil
		}
		m.appendLine(lineSystem, "memory summary rebuilt on disk; "+projectContextRefreshHint)
		m.appendLine(lineSep, "")
		return m, nil
	case "reset":
		if rest != "--confirm" {
			m.appendLine(lineError, memoryResetUsage)
			return m, nil
		}
		if err := m.deps.Memory.Reset(); err != nil {
			m.appendLine(lineError, "memory reset: "+err.Error())
			return m, nil
		}
		m.appendLine(lineSystem, "memory reset: current workspace semantic memory cleared; session threads retained; "+projectContextRefreshHint)
		m.appendLine(lineSep, "")
		return m, nil
	default:
		m.appendLine(lineError, "usage: /memory [list|add|update|delete|accept|on|off|generate|status|rebuild|reset --confirm]")
		return m, nil
	}
}

func memoryCommandMutates(cmd string) bool {
	switch cmd {
	case "", "list", "status":
		return false
	default:
		return true
	}
}

// memoryCommandAllowsBusy reports whether /memory can run while a turn is active.
func memoryCommandAllowsBusy(arg string) bool {
	fields := strings.Fields(strings.TrimSpace(arg))
	cmd := ""
	if len(fields) > 0 {
		cmd = strings.ToLower(fields[0])
	}
	return !memoryCommandMutates(cmd)
}

func parseMemoryAdd(rest string) (key, claim string) {
	rest = strings.TrimSpace(rest)
	if strings.HasPrefix(rest, "key=") {
		rest = strings.TrimPrefix(rest, "key=")
		parts := strings.SplitN(rest, " ", 2)
		if len(parts) == 2 {
			return parts[0], parts[1]
		}
		return parts[0], parts[0]
	}
	return "", rest
}

func parseMemoryUpdate(rest string) (idOrKey, claim string) {
	rest = strings.TrimSpace(rest)
	idx := strings.IndexAny(rest, " \t")
	if idx < 0 {
		return rest, ""
	}
	return rest[:idx], strings.TrimSpace(rest[idx:])
}
