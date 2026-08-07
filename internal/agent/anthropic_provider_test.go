package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"eino-local-assistant/internal/config"
	"eino-local-assistant/internal/provider"
	"eino-local-assistant/internal/tools"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func TestReActModelRunsNoArgumentAnthropicToolUse(t *testing.T) {
	var requestsMu sync.Mutex
	requests := make([]map[string]any, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
			http.Error(w, "unexpected Anthropic request", http.StatusNotFound)
			return
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}

		requestsMu.Lock()
		requestIndex := len(requests)
		requests = append(requests, body)
		requestsMu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		switch requestIndex {
		case 0:
			// Anthropic sends an object at block start for a no-argument tool.
			// The Eino adapter intentionally suppresses that duplicate "{}" chunk.
			writeReActAnthropicSSE(w, "message_start", `{"type":"message_start","message":{"id":"msg-tool","type":"message","role":"assistant","content":[],"model":"claude-test","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":0}}}`)
			writeReActAnthropicSSE(w, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_time","name":"get_current_time","input":{}}}`)
			writeReActAnthropicSSE(w, "content_block_stop", `{"type":"content_block_stop","index":0}`)
			writeReActAnthropicSSE(w, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"input_tokens":10,"output_tokens":1}}`)
			writeReActAnthropicSSE(w, "message_stop", `{"type":"message_stop"}`)
		case 1:
			writeReActAnthropicSSE(w, "message_start", `{"type":"message_start","message":{"id":"msg-final","type":"message","role":"assistant","content":[],"model":"claude-test","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":20,"output_tokens":0}}}`)
			writeReActAnthropicSSE(w, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
			writeReActAnthropicSSE(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"The time tool completed."}}`)
			writeReActAnthropicSSE(w, "content_block_stop", `{"type":"content_block_stop","index":0}`)
			writeReActAnthropicSSE(w, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":20,"output_tokens":5}}`)
			writeReActAnthropicSSE(w, "message_stop", `{"type":"message_stop"}`)
		default:
			http.Error(w, "unexpected extra model request", http.StatusInternalServerError)
		}
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()

	base, err := provider.NewAnthropicModel(context.Background(), config.ModelConfig{
		Provider: config.ProviderAnthropic,
		BaseURL:  server.URL,
		APIKey:   "test-key",
		Name:     "claude-test",
		Context: config.ModelContextConfig{
			WindowTokens: 32_000,
		},
		TimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatalf("NewAnthropicModel() error = %v", err)
	}

	fixed := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	timeTool, err := tools.NewGetCurrentTime(func() time.Time { return fixed })
	if err != nil {
		t.Fatalf("NewGetCurrentTime() error = %v", err)
	}
	reactModel, err := NewReActModel(context.Background(), base, []tool.BaseTool{timeTool})
	if err != nil {
		t.Fatalf("NewReActModel() error = %v", err)
	}

	stream, err := reactModel.Stream(context.Background(), []*schema.Message{
		schema.SystemMessage("Use tools when needed."),
		schema.UserMessage("What time is it?"),
	})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	defer stream.Close()

	var output strings.Builder
	for {
		chunk, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			t.Fatalf("Recv() error = %v", recvErr)
		}
		output.WriteString(chunk.Content)
	}
	if got, want := output.String(), "The time tool completed."; got != want {
		t.Errorf("final output = %q, want %q", got, want)
	}

	requestsMu.Lock()
	gotRequests := append([]map[string]any(nil), requests...)
	requestsMu.Unlock()
	if got, want := len(gotRequests), 2; got != want {
		t.Fatalf("Anthropic requests = %d, want %d", got, want)
	}
	assertNoArgumentToolRoundTrip(t, gotRequests[1], fixed.Unix())
}

func writeReActAnthropicSSE(w io.Writer, event string, data string) {
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
}

func assertNoArgumentToolRoundTrip(t *testing.T, request map[string]any, wantUnix int64) {
	t.Helper()
	messages, ok := request["messages"].([]any)
	if !ok {
		t.Fatalf("messages = %#v, want array", request["messages"])
	}

	var toolUse map[string]any
	var toolResult map[string]any
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
				toolUse = block
			case "tool_result":
				toolResult = block
			}
		}
	}
	if toolUse == nil {
		t.Fatalf("second request missing tool_use: %#v", messages)
	}
	if got, want := toolUse["id"], "toolu_time"; got != want {
		t.Errorf("tool_use ID = %#v, want %q", got, want)
	}
	input, ok := toolUse["input"].(map[string]any)
	if !ok || len(input) != 0 {
		t.Errorf("tool_use input = %#v, want empty object", toolUse["input"])
	}
	if toolResult == nil {
		t.Fatalf("second request missing tool_result: %#v", messages)
	}
	if got, want := toolResult["tool_use_id"], "toolu_time"; got != want {
		t.Errorf("tool_result ID = %#v, want %q", got, want)
	}
	encoded, err := json.Marshal(toolResult)
	if err != nil {
		t.Fatalf("marshal tool result: %v", err)
	}
	if !strings.Contains(string(encoded), fmt.Sprintf("%d", wantUnix)) {
		t.Errorf("tool_result = %s, want successful time output containing unix %d", encoded, wantUnix)
	}
}
