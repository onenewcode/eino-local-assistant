package tui

import (
	"fmt"
	"strings"

	"eino-local-assistant/internal/chat"
	"eino-local-assistant/internal/usage"
)

// shortSessionID shortens a session id for the status bar.
// Prefer the trailing hex suffix when the id matches YYYYMMDD-HHMMSS-xxxxxx.
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

type idleStatusParts struct {
	model   string
	shortID string
	title   string
	tokens  string
	cost    string
	ctx     string
	queued  string
	follow  string
}

func collectIdleStatus(session *chat.Session, modelName string, queueN int, followHint bool) idleStatusParts {
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
		prompt, completion, total, cost, _ := session.UsageTotals()
		if total > 0 || prompt > 0 || completion > 0 {
			p.tokens = usage.FormatTokens(total)
		}
		if cost > 0 || total > 0 {
			p.cost = usage.FormatUSD(cost)
		}
		contextStatus := session.ContextStatus()
		if contextStatus.BudgetTokens > 0 && contextStatus.CurrentTokens > 0 {
			pct := min(100, contextStatus.CurrentTokens*100/contextStatus.BudgetTokens)
			p.ctx = fmt.Sprintf("ctx=%d%%", pct)
			if contextStatus.OmittedTurnGroups > 0 || len(contextStatus.LastFallbacks) > 0 {
				p.ctx += "*"
			}
			if contextStatus.CurrentTokens > contextStatus.BudgetTokens {
				p.ctx += "!"
			}
		}
	}
	if queueN > 0 {
		p.queued = fmt.Sprintf("queued:%d", queueN)
	}
	if followHint {
		p.follow = "↑ End to follow"
	}
	return p
}

// formatIdleStatus builds a width-aware idle status label (without styling).
// Drop order when too wide: title → shortID → model → tokens (keep ready/cost/queue/follow).
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
		{"tokens", p.tokens},
		{"cost", p.cost},
		{"ctx", p.ctx},
		{"queued", p.queued},
		{"follow", p.follow},
	}

	join := func(include map[string]bool) string {
		parts := []string{base}
		// Preferred display order (not drop order).
		order := []string{"model", "id", "title", "tokens", "cost", "ctx", "queued", "follow"}
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
		"title": true, "id": true, "model": true, "tokens": true,
		"cost": true, "ctx": true, "queued": true, "follow": true,
	}
	if width <= 0 {
		width = 80
	}
	// Drop priority when over width.
	dropOrder := []string{"title", "id", "model", "tokens", "ctx"}
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
