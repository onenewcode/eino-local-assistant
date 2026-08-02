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

func TestOpenAIModelStreamsCompatibleSSE(t *testing.T) {
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
		fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()

	model, err := NewOpenAIModel(context.Background(), config.ModelConfig{
		BaseURL:        server.URL + "/v1",
		APIKey:         "test-key",
		Name:           "test-model",
		TimeoutSeconds: 5,
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

	if got, want := output.String(), "Hello world!"; got != want {
		t.Errorf("streamed content = %q, want %q", got, want)
	}

	select {
	case request := <-requests:
		if got, want := request.Model, "test-model"; got != want {
			t.Errorf("model = %q, want %q", got, want)
		}
		if !request.Stream {
			t.Error("stream flag = false, want true")
		}
		if got, want := len(request.Messages), 2; got != want {
			t.Fatalf("messages = %d, want %d", got, want)
		}
	default:
		t.Fatal("server did not receive a completion request")
	}
}

type completionRequest struct {
	Model    string `json:"model"`
	Stream   bool   `json:"stream"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}
