package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// Draft is one extracted candidate before store write.
type Draft struct {
	Key   string `json:"key"`
	Claim string `json:"claim"`
}

// extractPayload is the strict JSON shape expected from the model.
type extractPayload struct {
	Memories []Draft `json:"memories"`
}

// Extractor pulls candidate memories from a transcript using a chat model.
type Extractor struct {
	Model model.BaseChatModel
}

// ExtractCandidates asks the model for durable project facts from transcript text.
func (e *Extractor) ExtractCandidates(ctx context.Context, transcript string) ([]Draft, error) {
	if e == nil || e.Model == nil {
		return nil, fmt.Errorf("memory extractor model is required")
	}
	transcript = strings.TrimSpace(transcript)
	if transcript == "" {
		return nil, nil
	}
	// Bound input size (~12k tokens rough).
	if est := len([]rune(transcript)); est > 40_000 {
		runes := []rune(transcript)
		transcript = string(runes[len(runes)-40_000:])
	}
	transcript = redactSecrets(transcript)

	system := `You extract durable project-scoped memories from a coding-agent transcript.
Return ONLY a JSON object: {"memories":[{"key":"short-slug","claim":"one sentence fact"}]}.
Rules:
- Prefer stable preferences, build commands, architecture facts, and explicit user requests to remember.
- Skip ephemeral debugging steps, one-off errors, and secrets.
- key: lowercase kebab-case, max 48 chars.
- claim: concise, self-contained, no secrets.
- If nothing durable, return {"memories":[]}.
- No markdown fences, no commentary.`

	user := "Transcript:\n\n" + transcript
	msg, err := e.Model.Generate(ctx, []*schema.Message{
		schema.SystemMessage(system),
		schema.UserMessage(user),
	})
	if err != nil {
		return nil, fmt.Errorf("memory extract: %w", err)
	}
	if msg == nil {
		return nil, nil
	}
	return parseExtractJSON(msg.Content)
}

func parseExtractJSON(raw string) ([]Draft, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	// Strip optional ```json fences.
	if strings.HasPrefix(raw, "```") {
		raw = strings.TrimPrefix(raw, "```json")
		raw = strings.TrimPrefix(raw, "```JSON")
		raw = strings.TrimPrefix(raw, "```")
		if i := strings.LastIndex(raw, "```"); i >= 0 {
			raw = raw[:i]
		}
		raw = strings.TrimSpace(raw)
	}
	// Find first { ... } if the model added prose.
	if !strings.HasPrefix(raw, "{") {
		start := strings.Index(raw, "{")
		end := strings.LastIndex(raw, "}")
		if start >= 0 && end > start {
			raw = raw[start : end+1]
		}
	}
	var payload extractPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, fmt.Errorf("parse extract json: %w", err)
	}
	out := make([]Draft, 0, len(payload.Memories))
	for _, d := range payload.Memories {
		d.Key = strings.TrimSpace(d.Key)
		d.Claim = strings.TrimSpace(d.Claim)
		if d.Claim == "" {
			continue
		}
		if d.Key == "" {
			d.Key = slugify(d.Claim)
		} else {
			d.Key = slugify(d.Key)
		}
		out = append(out, d)
	}
	return out, nil
}

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(api[_-]?key|token|secret|password)\s*[:=]\s*\S+`),
	regexp.MustCompile(`(?i)bearer\s+[a-z0-9\-._~+/]+=*`),
	regexp.MustCompile(`sk-[a-zA-Z0-9]{10,}`),
}

func redactSecrets(s string) string {
	out := s
	for _, re := range secretPatterns {
		out = re.ReplaceAllString(out, "[REDACTED]")
	}
	return out
}
