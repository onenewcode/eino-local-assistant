package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"eino-local-assistant/internal/config"

	claude "github.com/cloudwego/eino-ext/components/model/claude"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
)

func TestAnthropicModelStreamsMessagesAPIWithTools(t *testing.T) {
	requests := make(chan anthropicRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}

		requests <- anthropicRequest{
			method:  r.Method,
			path:    r.URL.Path,
			headers: r.Header.Clone(),
			body:    body,
		}

		w.Header().Set("Content-Type", "text/event-stream")
		writeAnthropicSSE(w, "message_start", `{"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","content":[],"model":"claude-test","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":11,"output_tokens":0}}}`)
		writeAnthropicSSE(w, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		writeAnthropicSSE(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello "}}`)
		writeAnthropicSSE(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Claude"}}`)
		writeAnthropicSSE(w, "content_block_stop", `{"type":"content_block_stop","index":0}`)
		writeAnthropicSSE(w, "content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_weather","name":"get_weather","input":{}}}`)
		writeAnthropicSSE(w, "content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\""}}`)
		writeAnthropicSSE(w, "content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"Paris\"}"}}`)
		writeAnthropicSSE(w, "content_block_stop", `{"type":"content_block_stop","index":1}`)
		writeAnthropicSSE(w, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"input_tokens":11,"output_tokens":7}}`)
		writeAnthropicSSE(w, "message_stop", `{"type":"message_stop"}`)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()

	baseModel, err := NewAnthropicModel(context.Background(), config.ModelConfig{
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
	callbackModel, ok := baseModel.(interface {
		GetType() string
		IsCallbacksEnabled() bool
	})
	if !ok {
		t.Fatal("Anthropic model does not expose callback metadata")
	}
	if got, want := callbackModel.GetType(), "Claude"; got != want {
		t.Errorf("model type = %q, want %q", got, want)
	}
	if !callbackModel.IsCallbacksEnabled() {
		t.Error("Anthropic model callbacks are disabled")
	}

	model, err := baseModel.WithTools([]*schema.ToolInfo{{
		Name: "get_weather",
		Desc: "Returns the current weather for a city.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"city": {
				Type:     schema.String,
				Desc:     "The city to look up.",
				Required: true,
			},
		}),
	}})
	if err != nil {
		t.Fatalf("WithTools() error = %v", err)
	}

	stream, err := model.Stream(context.Background(), []*schema.Message{
		schema.SystemMessage("You are a concise assistant."),
		schema.UserMessage("What is the weather in Paris?"),
		schema.SystemMessage("Use metric units."),
	})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	defer stream.Close()

	chunks := make([]*schema.Message, 0)
	for {
		chunk, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			t.Fatalf("Recv() error = %v", recvErr)
		}
		chunks = append(chunks, chunk)
	}

	request := receiveAnthropicRequest(t, requests)
	assertAnthropicRequest(t, request)

	message, err := schema.ConcatMessages(chunks)
	if err != nil {
		t.Fatalf("ConcatMessages() error = %v", err)
	}
	if got, want := message.Content, "Hello Claude"; got != want {
		t.Errorf("streamed content = %q, want %q", got, want)
	}
	if got, want := len(message.ToolCalls), 1; got != want {
		t.Fatalf("tool calls = %d, want %d", got, want)
	}
	if got, want := message.ToolCalls[0].ID, "toolu_weather"; got != want {
		t.Errorf("tool call ID = %q, want %q", got, want)
	}
	if got, want := message.ToolCalls[0].Function.Name, "get_weather"; got != want {
		t.Errorf("tool name = %q, want %q", got, want)
	}
	if got, want := message.ToolCalls[0].Function.Arguments, `{"city":"Paris"}`; got != want {
		t.Errorf("tool arguments = %q, want %q", got, want)
	}
	if message.ResponseMeta == nil || message.ResponseMeta.Usage == nil {
		t.Fatal("streamed message does not include usage")
	}
	if got, want := message.ResponseMeta.FinishReason, "tool_use"; got != want {
		t.Errorf("finish reason = %q, want %q", got, want)
	}
	if got, want := message.ResponseMeta.Usage.PromptTokens, 11; got != want {
		t.Errorf("prompt tokens = %d, want %d", got, want)
	}
	if got, want := message.ResponseMeta.Usage.CompletionTokens, 7; got != want {
		t.Errorf("completion tokens = %d, want %d", got, want)
	}
	if got, want := message.ResponseMeta.Usage.TotalTokens, 18; got != want {
		t.Errorf("total tokens = %d, want %d", got, want)
	}
}

func TestAnthropicModelPassesReasoningEffortWithoutDroppingOutputFormat(t *testing.T) {
	requests := make(chan anthropicRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		requests <- anthropicRequest{
			method:  r.Method,
			path:    r.URL.Path,
			headers: r.Header.Clone(),
			body:    body,
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"msg_effort","type":"message","role":"assistant","content":[{"type":"text","text":"Done."}],"model":"claude-test","stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer server.Close()

	model, err := NewAnthropicModel(context.Background(), config.ModelConfig{
		Provider:        config.ProviderAnthropic,
		BaseURL:         server.URL,
		APIKey:          "test-key",
		Name:            "claude-test",
		ReasoningEffort: " high ",
		Context: config.ModelContextConfig{
			WindowTokens: 32_000,
		},
		TimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatalf("NewAnthropicModel() error = %v", err)
	}

	_, err = model.Generate(context.Background(), []*schema.Message{schema.UserMessage("hello")}, claude.WithResponseFormat(&claude.ResponseFormat{
		Schema: &jsonschema.Schema{Type: "object"},
	}))
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	request := receiveAnthropicRequest(t, requests)
	outputConfig := objectValue(t, request.body["output_config"])
	if got, want := stringValue(t, outputConfig["effort"]), "high"; got != want {
		t.Errorf("output_config.effort = %q, want %q", got, want)
	}
	format := objectValue(t, outputConfig["format"])
	if got, want := stringValue(t, format["type"]), "json_schema"; got != want {
		t.Errorf("output_config.format.type = %q, want %q", got, want)
	}
	schemaObject := objectValue(t, format["schema"])
	if got, want := stringValue(t, schemaObject["type"]), "object"; got != want {
		t.Errorf("output_config.format.schema.type = %q, want %q", got, want)
	}
}

func TestAnthropicModelReducesRequiredMaxTokensForLargerPrompt(t *testing.T) {
	requests := make(chan anthropicRequest, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		requests <- anthropicRequest{body: body}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"msg_limit","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"claude-test","stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer server.Close()

	chatModel, err := NewAnthropicModel(context.Background(), config.ModelConfig{
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
		t.Fatalf("NewAnthropicModel: %v", err)
	}
	if _, err := chatModel.Generate(context.Background(), []*schema.Message{schema.UserMessage("short")}); err != nil {
		t.Fatalf("Generate short prompt: %v", err)
	}
	if _, err := chatModel.Generate(context.Background(), []*schema.Message{schema.UserMessage(strings.Repeat("long prompt ", 2_000))}); err != nil {
		t.Fatalf("Generate long prompt: %v", err)
	}

	shortRequest := receiveAnthropicRequest(t, requests)
	longRequest := receiveAnthropicRequest(t, requests)
	shortLimit := integerValue(t, shortRequest.body["max_tokens"])
	longLimit := integerValue(t, longRequest.body["max_tokens"])
	if longLimit >= shortLimit {
		t.Fatalf("max_tokens for long prompt = %d, want less than short prompt limit %d", longLimit, shortLimit)
	}
}

type anthropicRequest struct {
	method  string
	path    string
	headers http.Header
	body    map[string]any
}

func writeAnthropicSSE(w io.Writer, event string, data string) {
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
}

func receiveAnthropicRequest(t *testing.T, requests <-chan anthropicRequest) anthropicRequest {
	t.Helper()
	select {
	case request := <-requests:
		return request
	case <-time.After(2 * time.Second):
		t.Fatal("server did not receive an Anthropic Messages API request")
		return anthropicRequest{}
	}
}

func assertAnthropicRequest(t *testing.T, request anthropicRequest) {
	t.Helper()
	if got, want := request.method, http.MethodPost; got != want {
		t.Errorf("method = %q, want %q", got, want)
	}
	if got, want := request.path, "/v1/messages"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	if got, want := request.headers.Get("X-Api-Key"), "test-key"; got != want {
		t.Errorf("X-Api-Key = %q, want %q", got, want)
	}
	if got, want := request.headers.Get("anthropic-version"), "2023-06-01"; got != want {
		t.Errorf("anthropic-version = %q, want %q", got, want)
	}
	if got, want := stringValue(t, request.body["model"]), "claude-test"; got != want {
		t.Errorf("model = %q, want %q", got, want)
	}
	if got := integerValue(t, request.body["max_tokens"]); got < 1 || got > 30_400 {
		t.Errorf("max_tokens = %d, want a dynamically computed positive limit within the 95%% ceiling", got)
	}
	if got, ok := request.body["stream"].(bool); !ok || !got {
		t.Errorf("stream = %#v, want true", request.body["stream"])
	}
	if _, ok := request.body["output_config"]; ok {
		t.Errorf("output_config = %#v, want omitted by default", request.body["output_config"])
	}

	system := arrayValue(t, request.body["system"])
	if got, want := len(system), 2; got != want {
		t.Fatalf("system blocks = %d, want %d", got, want)
	}
	systemBlock := objectValue(t, system[0])
	if got, want := stringValue(t, systemBlock["type"]), "text"; got != want {
		t.Errorf("system block type = %q, want %q", got, want)
	}
	if got, want := stringValue(t, systemBlock["text"]), "You are a concise assistant."; got != want {
		t.Errorf("system text = %q, want %q", got, want)
	}
	if got, want := stringValue(t, objectValue(t, system[1])["text"]), "Use metric units."; got != want {
		t.Errorf("second system text = %q, want %q", got, want)
	}

	messages := arrayValue(t, request.body["messages"])
	if got, want := len(messages), 1; got != want {
		t.Fatalf("messages = %d, want %d", got, want)
	}
	for _, rawMessage := range messages {
		if role := stringValue(t, objectValue(t, rawMessage)["role"]); role == "system" {
			t.Error("system message was sent in messages instead of top-level system")
		}
	}

	tools := arrayValue(t, request.body["tools"])
	if got, want := len(tools), 1; got != want {
		t.Fatalf("tools = %d, want %d", got, want)
	}
	tool := objectValue(t, tools[0])
	if got, want := stringValue(t, tool["name"]), "get_weather"; got != want {
		t.Errorf("tool name = %q, want %q", got, want)
	}
	if got, want := stringValue(t, tool["description"]), "Returns the current weather for a city."; got != want {
		t.Errorf("tool description = %q, want %q", got, want)
	}
	inputSchema := objectValue(t, tool["input_schema"])
	if got, want := stringValue(t, inputSchema["type"]), "object"; got != want {
		t.Errorf("input schema type = %q, want %q", got, want)
	}
	properties := objectValue(t, inputSchema["properties"])
	city := objectValue(t, properties["city"])
	if got, want := stringValue(t, city["type"]), "string"; got != want {
		t.Errorf("city schema type = %q, want %q", got, want)
	}
	required := arrayValue(t, inputSchema["required"])
	if got, want := len(required), 1; got != want {
		t.Fatalf("required tool fields = %d, want %d", got, want)
	}
	if got, want := stringValue(t, required[0]), "city"; got != want {
		t.Errorf("required tool field = %q, want %q", got, want)
	}
}

func arrayValue(t *testing.T, value any) []any {
	t.Helper()
	array, ok := value.([]any)
	if !ok {
		t.Fatalf("JSON value = %#v, want array", value)
	}
	return array
}

func objectValue(t *testing.T, value any) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("JSON value = %#v, want object", value)
	}
	return object
}

func stringValue(t *testing.T, value any) string {
	t.Helper()
	stringValue, ok := value.(string)
	if !ok {
		t.Fatalf("JSON value = %#v, want string", value)
	}
	return stringValue
}

func integerValue(t *testing.T, value any) int {
	t.Helper()
	floatValue, ok := value.(float64)
	if !ok || floatValue != float64(int(floatValue)) {
		t.Fatalf("JSON value = %#v, want integer", value)
	}
	return int(floatValue)
}

func TestAnthropicModelSendsEmptyToolResult(t *testing.T) {
	requests := make(chan anthropicRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		requests <- anthropicRequest{
			method:  r.Method,
			path:    r.URL.Path,
			headers: r.Header.Clone(),
			body:    body,
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"msg_done","type":"message","role":"assistant","content":[{"type":"text","text":"Done."}],"model":"claude-test","stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":13,"output_tokens":2}}`)
	}))
	defer server.Close()

	baseModel, err := NewAnthropicModel(context.Background(), config.ModelConfig{
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
	model, err := baseModel.WithTools([]*schema.ToolInfo{{
		Name: "get_weather",
		Desc: "Returns the current weather for a city.",
	}})
	if err != nil {
		t.Fatalf("WithTools() error = %v", err)
	}

	response, err := model.Generate(context.Background(), []*schema.Message{
		schema.UserMessage("Look up Paris weather."),
		schema.AssistantMessage("", []schema.ToolCall{{
			ID: "toolu_weather",
			Function: schema.FunctionCall{
				Name:      "get_weather",
				Arguments: `{"city":"Paris"}`,
			},
		}}),
		schema.ToolMessage("", "toolu_weather", schema.WithToolName("get_weather")),
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if got, want := response.Content, "Done."; got != want {
		t.Errorf("generated content = %q, want %q", got, want)
	}

	request := receiveAnthropicRequest(t, requests)
	messages := arrayValue(t, request.body["messages"])
	if got, want := len(messages), 3; got != want {
		t.Fatalf("messages = %d, want %d", got, want)
	}
	toolResultMessage := objectValue(t, messages[2])
	if got, want := stringValue(t, toolResultMessage["role"]), "user"; got != want {
		t.Errorf("tool result role = %q, want %q", got, want)
	}
	toolResultContent := arrayValue(t, toolResultMessage["content"])
	if got, want := len(toolResultContent), 1; got != want {
		t.Fatalf("tool result blocks = %d, want %d", got, want)
	}
	toolResult := objectValue(t, toolResultContent[0])
	if got, want := stringValue(t, toolResult["type"]), "tool_result"; got != want {
		t.Errorf("tool result type = %q, want %q", got, want)
	}
	if got, want := stringValue(t, toolResult["tool_use_id"]), "toolu_weather"; got != want {
		t.Errorf("tool use ID = %q, want %q", got, want)
	}
	resultContent := arrayValue(t, toolResult["content"])
	if got, want := len(resultContent), 1; got != want {
		t.Fatalf("empty tool result content blocks = %d, want %d", got, want)
	}
	if got, want := stringValue(t, objectValue(t, resultContent[0])["type"]), "text"; got != want {
		t.Errorf("empty tool result content type = %q, want %q", got, want)
	}
	if got, want := stringValue(t, objectValue(t, resultContent[0])["text"]), ""; got != want {
		t.Errorf("empty tool result text = %q, want %q", got, want)
	}
}

func TestAnthropicModelNormalizesInvalidAndBlankHistoricalToolArguments(t *testing.T) {
	requests := make(chan anthropicRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		requests <- anthropicRequest{
			method:  r.Method,
			path:    r.URL.Path,
			headers: r.Header.Clone(),
			body:    body,
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"msg_done","type":"message","role":"assistant","content":[{"type":"text","text":"Done."}],"model":"claude-test","stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":13,"output_tokens":2}}`)
	}))
	defer server.Close()

	baseModel, err := NewAnthropicModel(context.Background(), config.ModelConfig{
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
	model, err := baseModel.WithTools([]*schema.ToolInfo{{Name: "get_weather"}})
	if err != nil {
		t.Fatalf("WithTools() error = %v", err)
	}

	_, err = model.Generate(context.Background(), []*schema.Message{
		schema.UserMessage("Look up Paris weather."),
		schema.AssistantMessage("", []schema.ToolCall{
			{
				ID: "toolu_invalid",
				Function: schema.FunctionCall{
					Name:      "get_weather",
					Arguments: "A",
				},
			},
			{
				ID: "toolu_empty",
				Function: schema.FunctionCall{
					Name: "get_weather",
				},
			},
			{
				ID: "toolu_whitespace",
				Function: schema.FunctionCall{
					Name:      "get_weather",
					Arguments: " ",
				},
			},
		}),
		schema.ToolMessage("Sunny.", "toolu_invalid", schema.WithToolName("get_weather")),
		schema.ToolMessage("Cloudy.", "toolu_empty", schema.WithToolName("get_weather")),
		schema.ToolMessage("Windy.", "toolu_whitespace", schema.WithToolName("get_weather")),
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	request := receiveAnthropicRequest(t, requests)
	messages := arrayValue(t, request.body["messages"])
	if got, want := len(messages), 3; got != want {
		t.Fatalf("messages = %d, want %d", got, want)
	}
	assistantBlocks := arrayValue(t, objectValue(t, messages[1])["content"])
	if got, want := len(assistantBlocks), 3; got != want {
		t.Fatalf("assistant content blocks = %d, want %d", got, want)
	}
	assertToolUseInputIsEmptyObject(t, assistantBlocks[0], "toolu_invalid")
	assertToolUseInputIsEmptyObject(t, assistantBlocks[1], "toolu_empty")
	assertToolUseInputIsEmptyObject(t, assistantBlocks[2], "toolu_whitespace")
}

func TestAnthropicModelMergesAdjacentToolResultsWithoutLosingIDs(t *testing.T) {
	requests := make(chan anthropicRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		requests <- anthropicRequest{
			method:  r.Method,
			path:    r.URL.Path,
			headers: r.Header.Clone(),
			body:    body,
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"msg_done","type":"message","role":"assistant","content":[{"type":"text","text":"Done."}],"model":"claude-test","stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":13,"output_tokens":2}}`)
	}))
	defer server.Close()

	baseModel, err := NewAnthropicModel(context.Background(), config.ModelConfig{
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
	model, err := baseModel.WithTools([]*schema.ToolInfo{{Name: "get_weather"}})
	if err != nil {
		t.Fatalf("WithTools() error = %v", err)
	}

	_, err = model.Generate(context.Background(), []*schema.Message{
		schema.UserMessage("Look up Paris and London weather."),
		schema.AssistantMessage("", []schema.ToolCall{
			{
				ID: "toolu_paris",
				Function: schema.FunctionCall{
					Name:      "get_weather",
					Arguments: `{"city":"Paris"}`,
				},
			},
			{
				ID: "toolu_london",
				Function: schema.FunctionCall{
					Name:      "get_weather",
					Arguments: `{"city":"London"}`,
				},
			},
		}),
		schema.ToolMessage("Sunny.", "toolu_paris", schema.WithToolName("get_weather")),
		schema.ToolMessage("Rainy.", "toolu_london", schema.WithToolName("get_weather")),
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	request := receiveAnthropicRequest(t, requests)
	messages := arrayValue(t, request.body["messages"])
	if got, want := len(messages), 3; got != want {
		t.Fatalf("messages = %d, want %d", got, want)
	}
	toolResultsMessage := objectValue(t, messages[2])
	if got, want := stringValue(t, toolResultsMessage["role"]), "user"; got != want {
		t.Errorf("tool results role = %q, want %q", got, want)
	}
	toolResults := arrayValue(t, toolResultsMessage["content"])
	if got, want := len(toolResults), 2; got != want {
		t.Fatalf("tool result blocks = %d, want %d", got, want)
	}
	assertToolResultBlock(t, toolResults[0], "toolu_paris", "Sunny.")
	assertToolResultBlock(t, toolResults[1], "toolu_london", "Rainy.")
}

func TestAnthropicModelWithToolsDoesNotMutateBaseModelOrFinalOnlyBinding(t *testing.T) {
	requests := make(chan anthropicRequest, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		requests <- anthropicRequest{
			method:  r.Method,
			path:    r.URL.Path,
			headers: r.Header.Clone(),
			body:    body,
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"msg_done","type":"message","role":"assistant","content":[{"type":"text","text":"Done."}],"model":"claude-test","stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":1}}`)
	}))
	defer server.Close()

	baseModel, err := NewAnthropicModel(context.Background(), config.ModelConfig{
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
	toolModel, err := baseModel.WithTools([]*schema.ToolInfo{{Name: "get_weather"}})
	if err != nil {
		t.Fatalf("WithTools() error = %v", err)
	}
	finalOnlyModel, err := toolModel.WithTools(nil)
	if err != nil {
		t.Fatalf("WithTools(nil) error = %v", err)
	}

	if _, err := toolModel.Generate(context.Background(), []*schema.Message{schema.UserMessage("Use a tool if needed.")}); err != nil {
		t.Fatalf("tool-bound Generate() error = %v", err)
	}
	if _, err := baseModel.Generate(context.Background(), []*schema.Message{
		schema.SystemMessage("Summarize the conversation."),
		schema.UserMessage("Summarize this text."),
	}); err != nil {
		t.Fatalf("base Generate() error = %v", err)
	}
	if _, err := finalOnlyModel.Generate(context.Background(), []*schema.Message{
		schema.SystemMessage("Answer from the observations."),
		schema.UserMessage("Give the final answer."),
	}); err != nil {
		t.Fatalf("final-only Generate() error = %v", err)
	}

	toolRequest := receiveAnthropicRequest(t, requests)
	baseRequest := receiveAnthropicRequest(t, requests)
	finalOnlyRequest := receiveAnthropicRequest(t, requests)
	tools := arrayValue(t, toolRequest.body["tools"])
	if got, want := len(tools), 1; got != want {
		t.Errorf("tool-bound request tools = %d, want %d", got, want)
	}
	if _, exists := baseRequest.body["tools"]; exists {
		t.Errorf("base model request unexpectedly includes tools: %#v", baseRequest.body["tools"])
	}
	if _, exists := baseRequest.body["tool_choice"]; exists {
		t.Errorf("base model request unexpectedly includes tool_choice: %#v", baseRequest.body["tool_choice"])
	}
	if _, exists := finalOnlyRequest.body["tools"]; exists {
		t.Errorf("final-only request unexpectedly includes tools: %#v", finalOnlyRequest.body["tools"])
	}
	if _, exists := finalOnlyRequest.body["tool_choice"]; exists {
		t.Errorf("final-only request unexpectedly includes tool_choice: %#v", finalOnlyRequest.body["tool_choice"])
	}
}

func assertToolResultBlock(t *testing.T, value any, wantID string, wantText string) {
	t.Helper()
	block := objectValue(t, value)
	if got, want := stringValue(t, block["type"]), "tool_result"; got != want {
		t.Errorf("tool result type = %q, want %q", got, want)
	}
	if got, want := stringValue(t, block["tool_use_id"]), wantID; got != want {
		t.Errorf("tool use ID = %q, want %q", got, want)
	}
	content := arrayValue(t, block["content"])
	if got, want := len(content), 1; got != want {
		t.Fatalf("tool result content blocks = %d, want %d", got, want)
	}
	text := objectValue(t, content[0])
	if got, want := stringValue(t, text["type"]), "text"; got != want {
		t.Errorf("tool result text type = %q, want %q", got, want)
	}
	if got, want := stringValue(t, text["text"]), wantText; got != want {
		t.Errorf("tool result text = %q, want %q", got, want)
	}
}

func assertToolUseInputIsEmptyObject(t *testing.T, value any, wantID string) {
	t.Helper()
	toolUse := objectValue(t, value)
	if got, want := stringValue(t, toolUse["type"]), "tool_use"; got != want {
		t.Errorf("tool use type = %q, want %q", got, want)
	}
	if got, want := stringValue(t, toolUse["id"]), wantID; got != want {
		t.Errorf("tool use ID = %q, want %q", got, want)
	}
	input := objectValue(t, toolUse["input"])
	if got := len(input); got != 0 {
		t.Errorf("tool input = %#v, want empty object", input)
	}
}
