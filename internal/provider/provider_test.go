package provider

import (
	"context"
	"testing"

	"eino-local-assistant/internal/config"

	"github.com/cloudwego/eino/components"
)

func TestNewChatModelSelectsConfiguredProvider(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		wantType string
	}{
		{name: "default OpenAI", wantType: "OpenAI"},
		{name: "explicit OpenAI", provider: config.ProviderOpenAI, wantType: "OpenAI"},
		{name: "Anthropic", provider: config.ProviderAnthropic, wantType: "Claude"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chatModel, err := NewChatModel(context.Background(), config.ModelConfig{
				Provider: tt.provider,
				BaseURL:  "https://api.example.test",
				APIKey:   "test-key",
				Name:     "test-model",
				Context: config.ModelContextConfig{
					WindowTokens: 32_000,
				},
				TimeoutSeconds: 5,
			})
			if err != nil {
				t.Fatalf("NewChatModel() error = %v", err)
			}
			gotType, ok := components.GetType(chatModel)
			if !ok {
				t.Fatal("NewChatModel() result does not expose a component type")
			}
			if gotType != tt.wantType {
				t.Errorf("component type = %q, want %q", gotType, tt.wantType)
			}
		})
	}
}
