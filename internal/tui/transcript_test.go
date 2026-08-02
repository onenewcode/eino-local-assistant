package tui

import (
	"context"
	"io"
	"testing"

	"eino-local-assistant/internal/chat"
	"eino-local-assistant/internal/store"

	"github.com/cloudwego/eino/schema"
)

func TestHasReplayableTranscript(t *testing.T) {
	if hasReplayableTranscript(nil) {
		t.Fatal("nil transcript should not be replayable")
	}
	if hasReplayableTranscript([]*schema.Message{{Role: schema.System, Content: "sys"}}) {
		t.Fatal("system-only should not be replayable")
	}
	if !hasReplayableTranscript([]*schema.Message{
		{Role: schema.System, Content: "sys"},
		{Role: schema.User, Content: "hi"},
	}) {
		t.Fatal("user message should be replayable")
	}
	if hasReplayableTranscript([]*schema.Message{
		{Role: schema.Assistant, Content: "   "},
	}) {
		t.Fatal("blank assistant should not count")
	}
	if !hasReplayableTranscript([]*schema.Message{
		{Role: schema.Assistant, Content: "hello"},
	}) {
		t.Fatal("non-empty assistant should be replayable")
	}
}

func TestSeedLinesFromTranscript(t *testing.T) {
	hist := []*schema.Message{
		{Role: schema.System, Content: "system prompt"},
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "world"},
		{Role: schema.Assistant, Content: "  "},
		{Role: schema.User, Content: "again"},
	}
	lines := seedLinesFromTranscript(hist, "resumed abc (5 messages)", "my title")
	if !hasLineContaining(lines, lineSystem, "resumed abc") {
		t.Fatalf("banner missing: %#v", lines)
	}
	if !hasLineContaining(lines, lineSystem, "title: my title") {
		t.Fatalf("title missing: %#v", lines)
	}
	if !hasLineContaining(lines, lineUser, "hello") || !hasLineContaining(lines, lineUser, "again") {
		t.Fatalf("user lines missing: %#v", lines)
	}
	if !hasLineContaining(lines, lineAssistant, "world") {
		t.Fatalf("assistant line missing: %#v", lines)
	}
	// Blank assistant skipped; system skipped; last is sep.
	var assistants int
	for _, l := range lines {
		if l.kind == lineAssistant {
			assistants++
		}
	}
	if assistants != 1 {
		t.Fatalf("assistant count = %d, want 1", assistants)
	}
	if lines[len(lines)-1].kind != lineSep {
		t.Fatalf("last line should be separator")
	}
}

func TestNewModelSeedsOnResumeTranscript(t *testing.T) {
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	ctx := context.Background()
	seedModel := &transcriptSeedModel{responses: []string{"prior assistant"}}
	session, err := chat.NewSession(seedModel, "system", chat.SessionOptions{
		Store: st,
		Title: "seed-test",
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := session.Ask(ctx, "prior user", nil); err != nil {
		t.Fatalf("seed Ask: %v", err)
	}

	opened, err := chat.OpenSession(&staticModel{}, st, session.ID(), chat.SessionOptions{Store: st})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	m := newModel(Deps{
		Ctx:     ctx,
		Session: opened,
		Store:   st,
		Status:  StatusInfo{Model: "test-model"},
	})
	if !hasLineContaining(m.lines, lineSystem, "resumed "+opened.ID()) {
		t.Fatalf("resume banner missing: %#v", m.lines)
	}
	if !hasLineContaining(m.lines, lineUser, "prior user") {
		t.Fatalf("seeded user missing: %#v", m.lines)
	}
	if !hasLineContaining(m.lines, lineAssistant, "prior assistant") {
		t.Fatalf("seeded assistant missing: %#v", m.lines)
	}
	// Welcome line must not appear when transcript is replayable.
	if hasLineContaining(m.lines, lineSystem, "type /help") {
		t.Fatalf("welcome should not appear on resume seed: %#v", m.lines)
	}
}

func TestNewModelWelcomeWithoutTranscript(t *testing.T) {
	session := mustSession(t, &staticModel{}, "system")
	m := newModel(Deps{Ctx: context.Background(), Session: session})
	if !hasLineContaining(m.lines, lineSystem, "type /help") {
		t.Fatalf("welcome missing for fresh session: %#v", m.lines)
	}
}

// transcriptSeedModel drives a completed durable turn through chat.Session.
type transcriptSeedModel struct {
	responses []string
}

func (m *transcriptSeedModel) Stream(_ context.Context, _ []*schema.Message) (chat.Stream, error) {
	if len(m.responses) == 0 {
		return &transcriptSeedStream{}, nil
	}
	response := m.responses[0]
	m.responses = m.responses[1:]
	return &transcriptSeedStream{message: schema.AssistantMessage(response, nil)}, nil
}

type transcriptSeedStream struct {
	message *schema.Message
	sent    bool
}

func (s *transcriptSeedStream) Recv() (*schema.Message, error) {
	if s.sent || s.message == nil {
		return nil, io.EOF
	}
	s.sent = true
	return s.message, nil
}

func (*transcriptSeedStream) Close() {}
