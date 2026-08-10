package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"eino-local-assistant/internal/store"

	"github.com/cloudwego/eino/schema"
)

func TestExportSessionMarkdownAndJSON(t *testing.T) {
	dataDir := t.TempDir()
	threadStore, err := store.NewThreadStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	state, err := threadStore.CreateThread(context.Background(), store.ThreadMeta{ID: "session-export", Title: "export me"}, "system instructions")
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.StartTurn(context.Background(), state.ID, state.Revision, store.TurnStart{TurnID: "turn-export", Input: "user input"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := threadStore.CommitTurn(context.Background(), state.ID, state.Revision, store.TurnCommit{
		TurnID: "turn-export",
		Messages: []*schema.Message{
			schema.UserMessage("user input"),
			schema.AssistantMessage("assistant output", nil),
		},
	}); err != nil {
		t.Fatal(err)
	}
	configPath := writeSessionsConfig(t, dataDir)

	var markdown bytes.Buffer
	if err := exportSession(configPath, "export me", "markdown", &markdown); err != nil {
		t.Fatalf("export markdown: %v", err)
	}
	for _, want := range []string{"# Session session-export", "Title: export me", "## System", "system instructions", "## User", "user input", "## Assistant", "assistant output"} {
		if !strings.Contains(markdown.String(), want) {
			t.Fatalf("markdown export missing %q:\n%s", want, markdown.String())
		}
	}

	var rawJSON bytes.Buffer
	if err := exportSession(configPath, "session-export", "json", &rawJSON); err != nil {
		t.Fatalf("export JSON: %v", err)
	}
	var exported sessionExport
	if err := json.Unmarshal(rawJSON.Bytes(), &exported); err != nil {
		t.Fatalf("decode export JSON %q: %v", rawJSON.String(), err)
	}
	if exported.Meta.ID != "session-export" || len(exported.Messages) != 3 || exported.Messages[2].Content != "assistant output" || strings.Contains(rawJSON.String(), "test-key") {
		t.Fatalf("exported JSON = %+v", exported)
	}
}

func TestExportSessionRejectsInvalidFormatAndMissingSession(t *testing.T) {
	for _, raw := range []string{"", "markdown", "MARKDOWN", "json", " JSON "} {
		if _, err := normalizeExportFormat(raw); err != nil {
			t.Fatalf("normalizeExportFormat(%q): %v", raw, err)
		}
	}
	if _, err := normalizeExportFormat("yaml"); err == nil || !strings.Contains(err.Error(), "markdown or json") {
		t.Fatalf("normalizeExportFormat(yaml) error = %v", err)
	}
	if err := exportSession(writeSessionsConfig(t, t.TempDir()), "", "json", &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "session ID or name is required") {
		t.Fatalf("export empty session error = %v", err)
	}
}
