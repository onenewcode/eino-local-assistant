package tui

import (
	"fmt"
	"strings"

	"eino-local-assistant/internal/chat"
	"eino-local-assistant/internal/usage"
)

const (
	statusFieldModel    = "model"
	statusFieldEffort   = "effort"
	statusFieldContext  = "context"
	statusFieldActivity = "activity"
	statusFieldSession  = "session"
	statusFieldTitle    = "title"
	statusFieldPolicy   = "policy"
	statusFieldTask     = "task"
	statusFieldQueue    = "queue"
	statusFieldFollow   = "follow"
)

var defaultStatusLineFields = []string{
	statusFieldModel,
	statusFieldEffort,
	statusFieldContext,
	statusFieldActivity,
}

var statusLineFieldSet = map[string]struct{}{
	statusFieldModel: {}, statusFieldEffort: {}, statusFieldContext: {}, statusFieldActivity: {},
	statusFieldSession: {}, statusFieldTitle: {}, statusFieldPolicy: {}, statusFieldTask: {},
	statusFieldQueue: {}, statusFieldFollow: {},
}

type statusLineSegment struct {
	field string
	text  string
}

// normalizeStatusLineFields keeps direct TUI embedders safe when they bypass
// config validation. The production configuration rejects invalid values.
func normalizeStatusLineFields(fields []string) []string {
	if len(fields) == 0 {
		return append([]string(nil), defaultStatusLineFields...)
	}
	seen := make(map[string]struct{}, len(fields))
	normalized := make([]string, 0, len(fields))
	for _, raw := range fields {
		field := strings.ToLower(strings.TrimSpace(raw))
		if _, ok := statusLineFieldSet[field]; !ok {
			continue
		}
		if _, duplicate := seen[field]; duplicate {
			continue
		}
		seen[field] = struct{}{}
		normalized = append(normalized, field)
	}
	if len(normalized) == 0 {
		return append([]string(nil), defaultStatusLineFields...)
	}
	return normalized
}

func statusLineModelName(modelName string) string {
	modelName = strings.TrimSpace(modelName)
	if i := strings.LastIndex(modelName, "/"); i >= 0 {
		modelName = modelName[i+1:]
	}
	return modelName
}

func statusLineContext(session *chat.Session) string {
	fragment := sessionCtxFragment(session)
	if fragment == "" {
		return ""
	}
	if start := strings.LastIndex(fragment, "("); start >= 0 && strings.HasSuffix(fragment, ")") {
		return "Context " + fragment[start+1:len(fragment)-1] + " used"
	}
	return "Context " + strings.TrimPrefix(strings.TrimPrefix(fragment, "ctx≈"), "ctx=")
}

func (m *model) statusActivity() string {
	switch {
	case m.backtrackStatus() != "":
		return m.backtrackStatus()
	case m.hasPendingApproval():
		return "awaiting approval"
	case m.mode == modeCompacting:
		return "compacting"
	case m.mode == modeBusy && m.interruptFeedbackShown:
		return "stopping"
	case m.mode == modeBusy && m.currentTool != "":
		return m.currentTool
	case m.mode == modeBusy && m.streamingAssistant:
		return "streaming"
	case m.mode == modeBusy:
		return "thinking"
	default:
		return ""
	}
}

func (m *model) statusLineSegments() []statusLineSegment {
	session := m.activeSession()
	follow := !m.stickBottom && !m.viewport.AtBottom()
	extras := collectStatusExtras(session, len(m.queue), follow, m.statusPolicyFragment())
	if m.queuePaused {
		extras.paused = "queue:paused"
	}

	values := map[string]string{
		statusFieldModel:    statusLineModelName(m.deps.Status.Model),
		statusFieldEffort:   strings.TrimSpace(m.deps.Status.ReasoningEffort),
		statusFieldContext:  statusLineContext(session),
		statusFieldActivity: m.statusActivity(),
		statusFieldPolicy:   extras.cmdPolicy,
		statusFieldTask:     extras.task,
		statusFieldQueue:    joinStatusFields(extras.paused, extras.queued),
		statusFieldFollow:   extras.follow,
	}
	if session != nil {
		values[statusFieldSession] = shortSessionID(session.ID())
		values[statusFieldTitle] = compactStatusTitle(session.Title())
	}

	segments := make([]statusLineSegment, 0, len(m.deps.StatusLineFields))
	for _, field := range m.deps.StatusLineFields {
		if text := values[field]; text != "" {
			segments = append(segments, statusLineSegment{field: field, text: text})
		}
	}
	return fitStatusLineSegments(max(20, m.width-2), segments)
}

func joinStatusFields(values ...string) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, " · ")
}

func compactStatusTitle(title string) string {
	title = strings.TrimSpace(title)
	if len([]rune(title)) <= 24 {
		return title
	}
	runes := []rune(title)
	return string(runes[:23]) + "…"
}

func fitStatusLineSegments(width int, segments []statusLineSegment) []statusLineSegment {
	if width <= 0 || len(segments) == 0 {
		return segments
	}
	kept := append([]statusLineSegment(nil), segments...)
	for len(kept) > 1 && statusLineSegmentsWidth(kept) > width {
		// Preserve the first configured field (normally model) and remove the
		// least important trailing field at a constrained terminal width.
		kept = kept[:len(kept)-1]
	}
	if len(kept) == 1 && statusLineSegmentsWidth(kept) > width && width > 1 {
		runes := []rune(kept[0].text)
		if len(runes) > width-1 {
			kept[0].text = string(runes[:width-1]) + "…"
		}
	}
	return kept
}

func statusLineSegmentsWidth(segments []statusLineSegment) int {
	parts := make([]string, 0, len(segments))
	for _, segment := range segments {
		parts = append(parts, segment.text)
	}
	return len([]rune(strings.Join(parts, " · ")))
}

func plainStatusLine(segments []statusLineSegment) string {
	parts := make([]string, 0, len(segments))
	for _, segment := range segments {
		parts = append(parts, segment.text)
	}
	return strings.Join(parts, " · ")
}

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
	status := session.ContextStatus()
	// Tool calls can add a large bounded result after the previous provider
	// response. Prefer the newer local projection until the next provider
	// usage snapshot arrives, rather than displaying a stale exact number.
	if status.CurrentEstimateIsNewer && status.CurrentTokens > 0 {
		return usage.FormatCompactEstimatedContext(status.CurrentTokens, session.ContextConfig().WindowTokens)
	}
	snapshot := sessionContextSnapshot(session)
	if snapshot != nil {
		return usage.FormatCompactContextSnapshot(snapshot)
	}
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
