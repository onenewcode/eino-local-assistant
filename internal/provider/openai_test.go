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

	"eino-local-assistant/internal/config"

	"github.com/cloudwego/eino/schema"
)

func TestOpenAIModelStreamsSSE(t *testing.T) {
	tests := []struct {
		name                    string
		modelName               string
		wantMaxCompletionTokens bool
	}{
		{name: "standard chat model", modelName: "test-model"},
		{name: "o-series model", modelName: "o3-mini", wantMaxCompletionTokens: true},
		{name: "versioned GPT-5 model", modelName: "gpt-5.1-codex", wantMaxCompletionTokens: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testOpenAIModelStreamsSSE(t, tt.modelName, tt.wantMaxCompletionTokens)
		})
	}
}

func TestUsesMaxCompletionTokens(t *testing.T) {
	for _, tt := range []struct {
		model string
		want  bool
	}{
		{model: "o1", want: true},
		{model: "o3-mini", want: true},
		{model: "O4-MINI", want: true},
		{model: "gpt-5", want: true},
		{model: "gpt-5.1-codex", want: true},
		{model: "gpt-50"},
		{model: "gpt-4.1"},
		{model: "deepseek-v4-flash"},
	} {
		if got := usesMaxCompletionTokens(tt.model); got != tt.want {
			t.Errorf("usesMaxCompletionTokens(%q) = %v, want %v", tt.model, got, tt.want)
		}
	}
}

func testOpenAIModelStreamsSSE(t *testing.T, modelName string, wantMaxCompletionTokens bool) {
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
		BaseURL:        server.URL + "/v1",
		APIKey:         "test-key",
		Name:           modelName,
		TimeoutSeconds: 5,
		Context: config.ModelContextConfig{
			WindowTokens:    1_000,
			MaxOutputTokens: 321,
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
		if wantMaxCompletionTokens {
			if request.MaxCompletionTokens == nil || *request.MaxCompletionTokens != 321 {
				t.Errorf("max_completion_tokens = %v, want 321", request.MaxCompletionTokens)
			}
			if request.MaxTokens != nil {
				t.Errorf("max_tokens = %v, want omitted", request.MaxTokens)
			}
		} else {
			if request.MaxTokens == nil || *request.MaxTokens != 321 {
				t.Errorf("max_tokens = %v, want 321", request.MaxTokens)
			}
			if request.MaxCompletionTokens != nil {
				t.Errorf("max_completion_tokens = %v, want omitted", request.MaxCompletionTokens)
			}
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
	MaxTokens           *int `json:"max_tokens"`
	MaxCompletionTokens *int `json:"max_completion_tokens"`
	Messages            []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}
