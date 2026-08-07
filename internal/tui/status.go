package tui

import (
	"fmt"
	"strings"

	"eino-local-assistant/internal/chat"
	"eino-local-assistant/internal/usage"
)

// shortSessionID shortens a session id for the status bar.
// Prefer the trailing random suffix when the ID carries one.
func shortSessionID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	if i := strings.LastIndex(id, "-"); i >= 0 && i+1 < len(id) {
		suf := id[i+1:]
		if len(suf) >= 4 && len(suf) <= 12 {
			return suf
		}
	}
	if len(id) <= 12 {
		return id
	}
	return id[:8] + "…"
}

// sessionCtxFragment shows provider-reported window occupancy when available.
// Before the first response, it can only show a clearly marked local estimate.
func sessionCtxFragment(session *chat.Session) string {
	if session == nil {
		return ""
	}
	snapshot := sessionContextSnapshot(session)
	if snapshot != nil {
		return usage.FormatCompactContextSnapshot(snapshot)
	}
	status := session.ContextStatus()
	if status.CurrentTokens > 0 {
		return usage.FormatCompactEstimatedContext(status.CurrentTokens, session.ContextConfig().WindowTokens)
	}
	return ""
}

// taskStatusFragment gives complex work a compact progress signal without
// exposing the controller's internal acceptance matrix as a command surface.
func taskStatusFragment(session *chat.Session) string {
	status := sessionTaskStatus(session)
	if !hasTaskStatus(status) {
		return ""
	}
	switch status.State {
	case "active":
		if status.PlanRequired {
			return "task:plan"
		}
		return fmt.Sprintf("task:%d/%d", status.DoneTasks, status.Tasks)
	case "complete":
		return "task:complete"
	case "interrupted":
		return "task:interrupted"
	default:
		return "task:" + status.State
	}
}

// statusExtras are optional fragments shared by idle and busy status lines.
type statusExtras struct {
	cmdPolicy string
	context   string
	task      string
	paused    string
	queued    string
	follow    string
}

func collectStatusExtras(session *chat.Session, queueN int, followHint bool, cmdPolicy string) statusExtras {
	e := statusExtras{
		context:   sessionCtxFragment(session),
		cmdPolicy: strings.TrimSpace(cmdPolicy),
		task:      taskStatusFragment(session),
	}
	if queueN > 0 {
		e.queued = fmt.Sprintf("queued:%d", queueN)
	}
	if followHint {
		e.follow = "↑ End to follow"
	}
	return e
}

// joinStatusSuffix joins non-empty extras with " · " separators, each prefixed
// by " · " when the result is non-empty (ready to append after a base label).
func joinStatusSuffix(e statusExtras) string {
	var parts []string
	for _, s := range []string{e.cmdPolicy, e.context, e.task, e.paused, e.queued, e.follow} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " · " + strings.Join(parts, " · ")
}

type idleStatusParts struct {
	model     string
	shortID   string
	title     string
	cmdPolicy string
	context   string
	task      string
	paused    string
	queued    string
	follow    string
}

func collectIdleStatus(session *chat.Session, modelName string, queueN int, followHint bool, cmdPolicy string) idleStatusParts {
	p := idleStatusParts{}
	if modelName != "" {
		p.model = modelName
	}
	if session != nil {
		if id := session.ID(); id != "" {
			p.shortID = shortSessionID(id)
		}
		if t := strings.TrimSpace(session.Title()); t != "" {
			// Keep status compact.
			if len([]rune(t)) > 24 {
				r := []rune(t)
				t = string(r[:23]) + "…"
			}
			p.title = t
		}
	}
	extras := collectStatusExtras(session, queueN, followHint, cmdPolicy)
	p.cmdPolicy = extras.cmdPolicy
	p.context = extras.context
	p.task = extras.task
	p.paused = extras.paused
	p.queued = extras.queued
	p.follow = extras.follow
	return p
}

// formatIdleStatus builds a width-aware idle status label (without styling).
// Drop order when too wide keeps the context signal longer than decoration.
func formatIdleStatus(width int, p idleStatusParts) string {
	type seg struct {
		key  string
		text string
	}
	// Always-on base.
	base := "ready"
	optional := []seg{
		{"title", p.title},
		{"id", p.shortID},
		{"model", p.model},
		{"cmd", p.cmdPolicy},
		{"context", p.context},
		{"task", p.task},
		{"paused", p.paused},
		{"queued", p.queued},
		{"follow", p.follow},
	}

	join := func(include map[string]bool) string {
		parts := []string{base}
		// Preferred display order (not drop order).
		order := []string{"model", "id", "title", "cmd", "context", "task", "paused", "queued", "follow"}
		for _, k := range order {
			if !include[k] {
				continue
			}
			for _, s := range optional {
				if s.key == k && s.text != "" {
					parts = append(parts, s.text)
				}
			}
		}
		return strings.Join(parts, " · ")
	}

	include := map[string]bool{
		"title": true, "id": true, "model": true, "cmd": true,
		"context": true, "task": true, "paused": true, "queued": true, "follow": true,
	}
	if width <= 0 {
		width = 80
	}
	// Drop priority when over width — keep cmd/ctx longer than decoration.
	dropOrder := []string{"title", "id", "model", "cmd", "context", "task"}
	out := join(include)
	for _, key := range dropOrder {
		if len(out) <= width {
			break
		}
		include[key] = false
		out = join(include)
	}
	if len(out) > width && width > 8 {
		// Hard clamp as last resort.
		runes := []rune(out)
		if len(runes) > width-1 {
			out = string(runes[:width-1]) + "…"
		}
	}
	return out
}
