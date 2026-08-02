package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"eino-local-assistant/internal/config"
	"eino-local-assistant/internal/provider"
	"eino-local-assistant/internal/store"

	"github.com/cloudwego/eino/schema"
)

func TestRecoveredToolHistoryReplaysThroughAnthropicProvider(t *testing.T) {
	ctx := context.Background()
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore() error = %v", err)
	}
	state, err := threadStore.CreateThread(ctx, store.ThreadMeta{ID: "provider-resume"}, "system")
	if err != nil {
		t.Fatalf("CreateThread() error = %v", err)
	}
	state, err = threadStore.StartTurn(ctx, state.ID, state.Revision, store.TurnStart{
		TurnID: "turn-1",
		Input:  "inspect files",
	})
	if err != nil {
		t.Fatalf("StartTurn() error = %v", err)
	}
	recorder := newThreadTurnRecorder(threadStore, state.ID, state.Revision, "turn-1")
	// Older OpenAI-compatible providers can leave non-JSON arguments in the
	// ledger. Anthropic replay must still be a valid Messages API request.
	recorder.record(TurnEvent{Kind: TurnEventToolStart, Tool: "search", ToolCallID: "call-a", Input: "A"})
	recorder.record(TurnEvent{Kind: TurnEventToolStart, Tool: "search", ToolCallID: "call-b", Input: "B"})
	recorder.record(TurnEvent{Kind: TurnEventToolEnd, Tool: "search", ToolCallID: "call-b", Output: "output B"})
	recorder.record(TurnEvent{Kind: TurnEventToolEnd, Tool: "search", ToolCallID: "call-a", Output: "output A"})
	if err := recorder.err(); err != nil {
		t.Fatalf("record tool lifecycle: %v", err)
	}
	state, err = recorder.commit(store.TurnCommit{
		TurnID: "turn-1",
		Messages: []*schema.Message{
			schema.UserMessage("inspect files"),
			schema.AssistantMessage("inspection complete", nil),
		},
	})
	if err != nil {
		t.Fatalf("CommitTurn() error = %v", err)
	}

	groups, err := threadStore.LoadTurnGroups(ctx, state.ID)
	if err != nil {
		t.Fatalf("LoadTurnGroups() error = %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("turn groups = %d, want 1", len(groups))
	}
	replay := append([]*schema.Message{schema.SystemMessage("system")}, turnGroupMessages(groups[0])...)

	requests := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		requests <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"msg-resume","type":"message","role":"assistant","content":[{"type":"text","text":"continued"}],"model":"claude-test","stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":12,"output_tokens":2}}`)
	}))
	defer server.Close()

	base, err := provider.NewAnthropicModel(ctx, config.ModelConfig{
		Provider: config.ProviderAnthropic,
		BaseURL:  server.URL,
		APIKey:   "test-key",
		Name:     "claude-test",
		Context: config.ModelContextConfig{
			WindowTokens:    32_000,
			MaxOutputTokens: 128,
		},
		TimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatalf("NewAnthropicModel() error = %v", err)
	}
	model, err := base.WithTools([]*schema.ToolInfo{{Name: "search", Desc: "Search files."}})
	if err != nil {
		t.Fatalf("WithTools() error = %v", err)
	}
	if _, err := model.Generate(ctx, replay); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	select {
	case body := <-requests:
		assertRecoveredAnthropicToolHistory(t, body)
	case <-time.After(2 * time.Second):
		t.Fatal("Anthropic provider did not receive recovered history")
	}
}

func assertRecoveredAnthropicToolHistory(t *testing.T, body map[string]any) {
	t.Helper()
	messages, ok := body["messages"].([]any)
	if !ok {
		t.Fatalf("messages = %#v, want array", body["messages"])
	}

	toolUseIDs := make(map[string]bool)
	toolResultIDs := make(map[string]bool)
	for _, rawMessage := range messages {
		message, ok := rawMessage.(map[string]any)
		if !ok {
			t.Fatalf("message = %#v, want object", rawMessage)
		}
		content, ok := message["content"].([]any)
		if !ok {
			continue
		}
		for _, rawBlock := range content {
			block, ok := rawBlock.(map[string]any)
			if !ok {
				t.Fatalf("content block = %#v, want object", rawBlock)
			}
			switch block["type"] {
			case "tool_use":
				id, _ := block["id"].(string)
				toolUseIDs[id] = true
				input, ok := block["input"].(map[string]any)
				if !ok || len(input) != 0 {
					t.Errorf("tool_use %q input = %#v, want empty object", id, block["input"])
				}
			case "tool_result":
				id, _ := block["tool_use_id"].(string)
				toolResultIDs[id] = true
			}
		}
	}

	for _, id := range []string{"call-a", "call-b"} {
		if !toolUseIDs[id] {
			t.Errorf("missing recovered tool_use %q in %#v", id, messages)
		}
		if !toolResultIDs[id] {
			t.Errorf("missing recovered tool_result %q in %#v", id, messages)
		}
	}
}
