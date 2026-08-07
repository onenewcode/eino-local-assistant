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

	"github.com/cloudwego/eino/schema"
)

func TestOpenAIModelStreamsSSE(t *testing.T) {
	tests := []struct {
		name                string
		modelName           string
		reasoningEffort     string
		wantReasoningEffort string
	}{
		{name: "standard chat model", modelName: "test-model", wantReasoningEffort: config.DefaultReasoningEffort},
		{name: "o-series model", modelName: "o3-mini", wantReasoningEffort: config.DefaultReasoningEffort},
		{name: "versioned GPT-5 model", modelName: "gpt-5.1-codex", wantReasoningEffort: config.DefaultReasoningEffort},
		{
			name:                "explicit provider value",
			modelName:           "test-model",
			reasoningEffort:     " high ",
			wantReasoningEffort: "high",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testOpenAIModelStreamsSSE(t, tt.modelName, tt.reasoningEffort, tt.wantReasoningEffort)
		})
	}
}

func TestOpenAIModelWithToolsCanRestoreUnboundFinalModel(t *testing.T) {
	requests := make(chan completionRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request completionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		requests <- request

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"chatcmpl-final\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Final answer\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()

	base, err := NewOpenAIModel(context.Background(), config.ModelConfig{
		BaseURL:        server.URL + "/v1",
		APIKey:         "test-key",
		Name:           "test-model",
		TimeoutSeconds: 5,
		Context:        config.ModelContextConfig{WindowTokens: 1_000},
	})
	if err != nil {
		t.Fatalf("NewOpenAIModel() error = %v", err)
	}
	withTools, err := base.WithTools([]*schema.ToolInfo{{
		Name: "get_weather",
		Desc: "Returns weather.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"city": {Type: schema.String, Required: true},
		}),
	}})
	if err != nil {
		t.Fatalf("WithTools() error = %v", err)
	}
	finalModel, err := withTools.WithTools(nil)
	if err != nil {
		t.Fatalf("WithTools(nil) error = %v", err)
	}

	stream, err := finalModel.Stream(context.Background(), []*schema.Message{schema.UserMessage("answer now")})
	if err != nil {
		t.Fatalf("final Stream() error = %v", err)
	}
	defer stream.Close()
	for {
		_, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			t.Fatalf("final Recv() error = %v", recvErr)
		}
	}

	select {
	case request := <-requests:
		if len(request.Tools) != 0 && string(request.Tools) != "null" {
			t.Fatalf("final request tools = %s, want omitted", request.Tools)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not receive final completion request")
	}
}

func testOpenAIModelStreamsSSE(t *testing.T, modelName, reasoningEffort, wantReasoningEffort string) {
	t.Helper()
	requests := make(chan completionRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method must be POST", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		if got, want := r.Header.Get("Authorization"), "Bearer test-key"; got != want {
			http.Error(w, "unexpected authorization", http.StatusUnauthorized)
			return
		}

		var request completionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		requests <- request

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"chatcmpl-test\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hello \"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"chatcmpl-test\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"world!\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"chatcmpl-test\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"test-model\",\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":2,\"total_tokens\":13}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()

	model, err := NewOpenAIModel(context.Background(), config.ModelConfig{
		BaseURL:         server.URL + "/v1",
		APIKey:          "test-key",
		Name:            modelName,
		ReasoningEffort: reasoningEffort,
		TimeoutSeconds:  5,
		Context: config.ModelContextConfig{
			WindowTokens: 1_000,
		},
	})
	if err != nil {
		t.Fatalf("NewOpenAIModel() error = %v", err)
	}

	stream, err := model.Stream(context.Background(), []*schema.Message{
		schema.SystemMessage("system prompt"),
		schema.UserMessage("hello"),
	})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	defer stream.Close()

	var output strings.Builder
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
		output.WriteString(chunk.Content)
	}

	if got, want := output.String(), "Hello world!"; got != want {
		t.Errorf("streamed content = %q, want %q", got, want)
	}
	message, err := schema.ConcatMessages(chunks)
	if err != nil {
		t.Fatalf("ConcatMessages() error = %v", err)
	}
	if message.ResponseMeta == nil || message.ResponseMeta.Usage == nil || message.ResponseMeta.Usage.TotalTokens != 13 {
		t.Fatalf("streamed usage = %#v, want total 13", message.ResponseMeta)
	}

	select {
	case request := <-requests:
		if got, want := request.Model, modelName; got != want {
			t.Errorf("model = %q, want %q", got, want)
		}
		if !request.Stream {
			t.Error("stream flag = false, want true")
		}
		if request.StreamOptions == nil || !request.StreamOptions.IncludeUsage {
			t.Errorf("stream_options = %#v, want include_usage=true", request.StreamOptions)
		}
		if wantReasoningEffort == "" {
			if request.ReasoningEffort != nil {
				t.Errorf("reasoning_effort = %q, want omitted", *request.ReasoningEffort)
			}
		} else if request.ReasoningEffort == nil || *request.ReasoningEffort != wantReasoningEffort {
			var got string
			if request.ReasoningEffort != nil {
				got = *request.ReasoningEffort
			}
			t.Errorf("reasoning_effort = %q, want %q", got, wantReasoningEffort)
		}
		if request.MaxTokens != nil || request.MaxCompletionTokens != nil {
			t.Errorf("OpenAI output limit fields = max_tokens:%v max_completion_tokens:%v, want both omitted", request.MaxTokens, request.MaxCompletionTokens)
		}
		if got, want := len(request.Messages), 2; got != want {
			t.Fatalf("messages = %d, want %d", got, want)
		}
	default:
		t.Fatal("server did not receive a completion request")
	}
}

type completionRequest struct {
	Model         string `json:"model"`
	Stream        bool   `json:"stream"`
	StreamOptions *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
	MaxTokens           *int            `json:"max_tokens"`
	MaxCompletionTokens *int            `json:"max_completion_tokens"`
	ReasoningEffort     *string         `json:"reasoning_effort"`
	Tools               json.RawMessage `json:"tools"`
	Messages            []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}
