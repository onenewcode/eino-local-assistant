package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"eino-local-assistant/internal/contextbuild"
	"eino-local-assistant/internal/store"

	"github.com/cloudwego/eino/schema"
)

type checkpointCompactorFunc func(context.Context, contextbuild.CompactionRequest) (contextbuild.Checkpoint, error)

func (f checkpointCompactorFunc) Compact(ctx context.Context, request contextbuild.CompactionRequest) (contextbuild.Checkpoint, error) {
	return f(ctx, request)
}

func TestThreadSessionCompactionRetainsRawTurnsAndUsesCheckpoint(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	model := &scriptedModel{streams: []Stream{
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("first answer", nil)}}},
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("second answer", nil)}}},
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("third answer", nil)}}},
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("after answer", nil)}}},
	}}
	compactor := checkpointCompactorFunc(func(_ context.Context, request contextbuild.CompactionRequest) (contextbuild.Checkpoint, error) {
		return contextbuild.DeterministicCheckpoint(request)
	})
	opts := SessionOptions{
		Store:     st,
		Title:     "thread compaction",
		Compactor: compactor,
		Context: contextbuild.Config{
			KeepRecentTurns: 1,
		},
	}
	session, err := NewSession(model, "system", opts)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	for _, input := range []string{"first unique source", "second unique source", "third live turn"} {
		if err := session.Ask(ctx, input, nil); err != nil {
			t.Fatalf("Ask(%q): %v", input, err)
		}
	}

	result, err := session.Compact(ctx, "preserve decisions")
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if result.CheckpointID == "" || len(result.SourceEventIDs) == 0 {
		t.Fatalf("compaction result = %+v", result)
	}
	state, err := st.LoadThread(ctx, session.ID())
	if err != nil {
		t.Fatalf("LoadThread: %v", err)
	}
	if state.ActiveCheckpointID != result.CheckpointID {
		t.Fatalf("active checkpoint = %q, want %q", state.ActiveCheckpointID, result.CheckpointID)
	}
	groups, err := st.LoadTurnGroups(ctx, session.ID())
	if err != nil {
		t.Fatalf("LoadTurnGroups: %v", err)
	}
	if len(groups) != 3 || groups[0].Committed == nil || groups[1].Committed == nil || groups[2].Committed == nil {
		t.Fatalf("raw committed groups were not retained: %#v", groups)
	}
	if got := groups[0].Committed.Messages[0].Content; got != "first unique source" {
		t.Fatalf("first raw source = %q", got)
	}

	resumed, err := OpenSession(model, st, session.ID(), opts)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if err := resumed.Ask(ctx, "continue", nil); err != nil {
		t.Fatalf("Ask after resume: %v", err)
	}
	request := model.requests[len(model.requests)-1]
	var checkpointVisible bool
	for _, message := range request {
		if message != nil && message.Role == schema.System && strings.Contains(message.Content, "Structured checkpoint") {
			checkpointVisible = true
		}
		if message != nil && (strings.Contains(message.Content, "first unique source") || strings.Contains(message.Content, "second unique source")) {
			t.Fatalf("covered raw turn leaked into post-compaction prompt: %#v", request)
		}
	}
	if !checkpointVisible {
		t.Fatalf("post-compaction prompt has no checkpoint: %#v", request)
	}
}

func TestCheckpointLineageKeepsHotPayloadBoundedAcrossRepeatedCompaction(t *testing.T) {
	const turns = 22
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	streams := make([]Stream, 0, turns+1)
	for i := 0; i <= turns; i++ {
		streams = append(streams, &scriptedStream{events: []streamEvent{{message: schema.AssistantMessage(fmt.Sprintf("answer-%02d", i), nil)}}})
	}
	model := &scriptedModel{streams: streams}
	compactor := checkpointCompactorFunc(func(_ context.Context, request contextbuild.CompactionRequest) (contextbuild.Checkpoint, error) {
		return contextbuild.DeterministicCheckpoint(request)
	})
	opts := SessionOptions{
		Store:     st,
		Compactor: compactor,
		Context: contextbuild.Config{
			ModelContextTokens:        8_000,
			OutputReserveTokens:       1_000,
			KeepRecentTurns:           1,
			SummaryMaxTokens:          2_048,
			AutoCompactTriggerPercent: 75,
			PostCompactTargetPercent:  45,
			LowGainThresholdPercent:   1,
		},
	}
	session, err := NewSession(model, "system", opts)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	for i := 0; i < turns; i++ {
		if err := session.Ask(ctx, fmt.Sprintf("turn-%02d", i), nil); err != nil {
			t.Fatalf("Ask(%d): %v", i, err)
		}
		if i == 0 {
			continue
		}
		if _, err := session.Compact(ctx, "retain progress"); err != nil {
			t.Fatalf("Compact after turn %d: %v", i, err)
		}
	}

	state, err := st.LoadThread(ctx, session.ID())
	if err != nil {
		t.Fatalf("LoadThread: %v", err)
	}
	lineage, err := st.LoadCheckpointLineage(ctx, session.ID(), state.ActiveCheckpointID)
	if err != nil {
		t.Fatalf("LoadCheckpointLineage: %v", err)
	}
	if got, want := len(lineage), turns-1; got != want {
		t.Fatalf("lineage length = %d, want %d", got, want)
	}
	var directIDs []string
	for _, checkpoint := range lineage {
		directIDs = append(directIDs, checkpoint.SourceEventIDs...)
	}
	if got := len(uniqueSourceEventIDs(directIDs)); got <= contextbuild.MaxCheckpointEvidenceRefs {
		t.Fatalf("cold lineage did not retain more than the hot evidence cap: %d", got)
	}
	persisted, err := st.LoadCheckpoint(ctx, session.ID(), state.ActiveCheckpointID)
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	checkpoint, err := contextbuild.ParseCheckpointJSON(persisted.Payload)
	if err != nil {
		t.Fatalf("ParseCheckpointJSON: %v", err)
	}
	if len(checkpoint.SourceEventIDs) > contextbuild.MaxCheckpointEvidenceRefs {
		t.Fatalf("hot checkpoint leaked complete source manifest: %d refs", len(checkpoint.SourceEventIDs))
	}
	if checkpoint.EstimatedTokens() > opts.Context.Normalize().SummaryMaxTokens {
		t.Fatalf("hot checkpoint exceeds summary budget: %d", checkpoint.EstimatedTokens())
	}

	resumed, err := OpenSession(model, st, session.ID(), opts)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if err := resumed.Ask(ctx, "continue", nil); err != nil {
		t.Fatalf("Ask after resume: %v", err)
	}
	request := model.requests[len(model.requests)-1]
	for _, message := range request {
		if message != nil && strings.Contains(message.Content, "turn-00") {
			t.Fatalf("ancestor-covered raw turn leaked into resumed prompt: %#v", request)
		}
	}
}

func TestThreadSessionCancelledCompactionLeavesActiveCheckpointUnchanged(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	model := &scriptedModel{streams: []Stream{
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("one", nil)}}},
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("two", nil)}}},
	}}
	compactor := checkpointCompactorFunc(func(context.Context, contextbuild.CompactionRequest) (contextbuild.Checkpoint, error) {
		return contextbuild.Checkpoint{}, context.Canceled
	})
	session, err := NewSession(model, "system", SessionOptions{
		Store:     st,
		Compactor: compactor,
		Context: contextbuild.Config{
			KeepRecentTurns:         1,
			LowGainThresholdPercent: 1,
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	for _, input := range []string{"first", "second"} {
		if err := session.Ask(ctx, input, nil); err != nil {
			t.Fatalf("Ask(%q): %v", input, err)
		}
	}
	_, err = session.Compact(ctx, "preserve facts")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Compact error = %v, want context.Canceled", err)
	}
	state, err := st.LoadThread(ctx, session.ID())
	if err != nil {
		t.Fatalf("LoadThread: %v", err)
	}
	if state.ActiveCheckpointID != "" {
		t.Fatalf("cancelled compact installed checkpoint %q", state.ActiveCheckpointID)
	}
}

func TestCompactionCandidatesPromotePlannerOmittedHotTurn(t *testing.T) {
	group := contextbuild.TurnGroup{
		ID:             "hot-overflow",
		SourceEventIDs: []string{"event-hot"},
		TokenEstimate:  600,
		Messages:       []*schema.Message{schema.UserMessage("large retained turn")},
	}
	plan, err := contextbuild.PlanContext(contextbuild.PlannerInput{
		ImmutableMessages: []*schema.Message{schema.SystemMessage("system")},
		TurnGroups:        []contextbuild.TurnGroup{group},
	}, contextbuild.Config{
		ModelContextTokens:        300,
		OutputReserveTokens:       100,
		KeepRecentTurns:           12,
		AutoCompactTriggerPercent: 75,
		PostCompactTargetPercent:  45,
	})
	if err != nil {
		t.Fatalf("PlanContext: %v", err)
	}
	if got := strings.Join(plan.OmittedGroupIDs, ","); got != "hot-overflow" {
		t.Fatalf("omitted groups = %q", got)
	}
	candidates := compactionCandidates([]contextbuild.TurnGroup{group}, nil, 12, plan.OmittedGroupIDs)
	if len(candidates) != 1 || candidates[0].ID != group.ID {
		t.Fatalf("hot overflow was not promoted to a compaction candidate: %#v", candidates)
	}
}

func TestAutomaticCompactionCompactsSingleOversizedRecentTurn(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	model := &scriptedModel{streams: []Stream{
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage(strings.Repeat("large answer ", 700), nil)}}},
	}}
	var requests []contextbuild.CompactionRequest
	compactor := checkpointCompactorFunc(func(_ context.Context, request contextbuild.CompactionRequest) (contextbuild.Checkpoint, error) {
		requests = append(requests, request)
		return contextbuild.DeterministicCheckpoint(request)
	})
	session, err := NewSession(model, "system", SessionOptions{
		Store:     st,
		Compactor: compactor,
		Context: contextbuild.Config{
			ModelContextTokens:        1_200,
			OutputReserveTokens:       200,
			KeepRecentTurns:           12,
			SummaryMaxTokens:          2_048,
			AutoCompactTriggerPercent: 75,
			PostCompactTargetPercent:  45,
			LowGainThresholdPercent:   1,
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := session.Ask(ctx, "small input", nil); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if !session.NeedsAutoCompaction() {
		t.Fatalf("oversized hot turn did not request automatic compaction: %+v", session.ContextStatus())
	}
	result, err := session.CompactAutomatically(ctx)
	if err != nil {
		t.Fatalf("CompactAutomatically: %v", err)
	}
	if result.CheckpointID == "" || len(requests) == 0 || len(requests[0].SourceGroups) != 1 {
		t.Fatalf("automatic compaction did not receive the oversized hot group: result=%+v requests=%#v", result, requests)
	}
}

func TestCandidateCheckpointMustFitBeforeInstallation(t *testing.T) {
	group := contextbuild.TurnGroup{
		ID:             "turn-1",
		SourceEventIDs: []string{"event-1"},
		Messages:       []*schema.Message{schema.UserMessage("source")},
	}
	checkpoint, err := contextbuild.DeterministicCheckpoint(contextbuild.CompactionRequest{
		SourceGroups: []contextbuild.TurnGroup{group},
	})
	if err != nil {
		t.Fatalf("DeterministicCheckpoint: %v", err)
	}
	session := &Session{
		systemPrompt: "system",
		contextCfg: contextbuild.Config{
			ModelContextTokens:  1_000,
			OutputReserveTokens: 900,
		},
	}
	plan, err := session.planWithCheckpoint([]contextbuild.TurnGroup{group}, &checkpoint, group.SourceEventIDs, nil)
	if err != nil {
		t.Fatalf("planWithCheckpoint: %v", err)
	}
	if !planHasFallback(plan, "checkpoint_omitted") {
		t.Fatalf("oversized candidate checkpoint was unexpectedly admitted: %+v", plan)
	}
}

func TestThreadSessionDoesNotInstallCheckpointThatCannotFit(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	model := &scriptedModel{streams: []Stream{
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("one", nil)}}},
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("two", nil)}}},
	}}
	compactor := checkpointCompactorFunc(func(_ context.Context, request contextbuild.CompactionRequest) (contextbuild.Checkpoint, error) {
		return contextbuild.DeterministicCheckpoint(request)
	})
	session, err := NewSession(model, "system", SessionOptions{
		Store:     st,
		Compactor: compactor,
		Context: contextbuild.Config{
			KeepRecentTurns:         1,
			LowGainThresholdPercent: 1,
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	for _, input := range []string{"first", "second"} {
		if err := session.Ask(ctx, input, nil); err != nil {
			t.Fatalf("Ask(%q): %v", input, err)
		}
	}
	// Keep the completed raw ledger, but reduce the next prompt capacity until
	// even a valid structured checkpoint cannot be installed alongside system.
	session.contextCfg = contextbuild.Config{
		ModelContextTokens:        1_000,
		OutputReserveTokens:       900,
		KeepRecentTurns:           1,
		LowGainThresholdPercent:   1,
		AutoCompactTriggerPercent: 75,
		PostCompactTargetPercent:  45,
	}
	_, err = session.Compact(ctx, "preserve facts")
	if !errors.Is(err, ErrCheckpointNotInstallable) {
		t.Fatalf("Compact error = %v, want ErrCheckpointNotInstallable", err)
	}
	state, err := st.LoadThread(ctx, session.ID())
	if err != nil {
		t.Fatalf("LoadThread: %v", err)
	}
	if state.ActiveCheckpointID != "" {
		t.Fatalf("unusable checkpoint was installed: %q", state.ActiveCheckpointID)
	}
}

func TestThreadSessionPersistsRawToolArtifactsWithoutUITruncation(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	rawOutput := strings.Repeat("raw tool output ", 300)
	model := &eventScriptedModel{
		stream: &scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("done", nil)}}},
		raw:    rawOutput,
	}
	session, err := NewSession(model, "system", SessionOptions{Store: st})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	var observed string
	if err := session.AskWithEvents(ctx, "inspect", nil, func(event TurnEvent) {
		if event.Kind == TurnEventToolEnd {
			observed = event.Output
		}
	}); err != nil {
		t.Fatalf("AskWithEvents: %v", err)
	}
	if observed != rawOutput {
		t.Fatalf("UI event output was truncated: got %d, want %d", len(observed), len(rawOutput))
	}
	groups, err := st.LoadTurnGroups(ctx, session.ID())
	if err != nil {
		t.Fatalf("LoadTurnGroups: %v", err)
	}
	if len(groups) != 1 || len(groups[0].Tools) != 1 || groups[0].Tools[0].Completed == nil {
		t.Fatalf("tool lifecycle missing: %#v", groups)
	}
	completed := groups[0].Tools[0].Completed
	if completed.Artifact == nil || completed.Artifact.OriginalSize != int64(len(rawOutput)) {
		t.Fatalf("raw artifact missing: %#v", completed)
	}
	if strings.Contains(completed.Output, rawOutput) {
		t.Fatalf("journal completion duplicated full artifact output")
	}
	if !strings.Contains(completed.Output, "read_artifact") {
		t.Fatalf("tool completion does not advertise bounded evidence retrieval: %q", completed.Output)
	}
	read, err := st.ReadArtifact(ctx, session.ID(), completed.Artifact.ID, 0, 64)
	if err != nil {
		t.Fatalf("ReadArtifact: %v", err)
	}
	if len(read.Data) == 0 && !read.Ref.Truncated {
		t.Fatalf("retained artifact cannot be read: %#v", read)
	}
}

func TestThreadTurnRecorderCorrelatesSameNamedToolsByCallID(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	state, err := st.CreateThread(ctx, store.ThreadMeta{ID: "thread-tool-ids"}, "system")
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	state, err = st.StartTurn(ctx, state.ID, state.Revision, store.TurnStart{TurnID: "turn-1", Input: "inspect"})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	recorder := newThreadTurnRecorder(st, state.ID, state.Revision, "turn-1")
	recorder.record(TurnEvent{Kind: TurnEventToolStart, Tool: "search", ToolCallID: "call-a", Input: "A"})
	recorder.record(TurnEvent{Kind: TurnEventToolStart, Tool: "search", ToolCallID: "call-b", Input: "B"})
	// Completion order intentionally differs from start order.
	recorder.record(TurnEvent{Kind: TurnEventToolEnd, Tool: "search", ToolCallID: "call-b", Output: "output B"})
	recorder.record(TurnEvent{Kind: TurnEventToolEnd, Tool: "search", ToolCallID: "call-a", Output: "output A"})
	if err := recorder.err(); err != nil {
		t.Fatalf("recorder error: %v", err)
	}
	state, err = recorder.commit(store.TurnCommit{
		TurnID: "turn-1",
		Messages: []*schema.Message{
			schema.UserMessage("inspect"),
			schema.AssistantMessage("done", nil),
		},
	})
	if err != nil {
		t.Fatalf("CommitTurn: %v", err)
	}
	groups, err := st.LoadTurnGroups(ctx, state.ID)
	if err != nil {
		t.Fatalf("LoadTurnGroups: %v", err)
	}
	if len(groups) != 1 || len(groups[0].Tools) != 2 {
		t.Fatalf("tool groups = %#v", groups)
	}
	outputs := map[string]string{}
	for _, tool := range groups[0].Tools {
		if tool.Completed == nil || tool.Completed.Artifact == nil {
			t.Fatalf("tool completion missing artifact: %#v", tool)
		}
		read, readErr := st.ReadArtifact(ctx, state.ID, tool.Completed.Artifact.ID, 0, 64)
		if readErr != nil {
			t.Fatalf("ReadArtifact(%s): %v", tool.ToolCallID, readErr)
		}
		outputs[tool.ToolCallID] = string(read.Data)
	}
	if outputs["call-a"] != "output A" || outputs["call-b"] != "output B" {
		t.Fatalf("tool outputs were cross-wired: %#v", outputs)
	}
	compactionGroups, err := durableCompactionGroups(ctx, st, state.ID, groups)
	if err != nil {
		t.Fatalf("durableCompactionGroups: %v", err)
	}
	if len(compactionGroups) != 1 || len(compactionGroups[0].Artifacts) != 2 {
		t.Fatalf("compaction artifacts = %#v", compactionGroups)
	}
	var hydrated strings.Builder
	for _, artifact := range compactionGroups[0].Artifacts {
		hydrated.WriteString(artifact.Digest)
	}
	if !strings.Contains(hydrated.String(), "output A") || !strings.Contains(hydrated.String(), "output B") {
		t.Fatalf("compactor source omitted retained tool evidence: %q", hydrated.String())
	}
}

func TestThreadSessionResumeLoadsRecentTranscriptThenPagesOlderTranscript(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	state, err := st.CreateThread(ctx, store.ThreadMeta{ID: "thread-page"}, "system")
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	for i := 0; i < 60; i++ {
		turnID := fmt.Sprintf("turn-%d", i)
		state, err = st.StartTurn(ctx, state.ID, state.Revision, store.TurnStart{TurnID: turnID, Input: fmt.Sprintf("user-%02d", i)})
		if err != nil {
			t.Fatalf("StartTurn(%d): %v", i, err)
		}
		state, err = st.CommitTurn(ctx, state.ID, state.Revision, store.TurnCommit{
			TurnID: turnID,
			Messages: []*schema.Message{
				schema.UserMessage(fmt.Sprintf("user-%02d", i)),
				schema.AssistantMessage(fmt.Sprintf("assistant-%02d", i), nil),
			},
		})
		if err != nil {
			t.Fatalf("CommitTurn(%d): %v", i, err)
		}
	}
	resumed, err := OpenSession(&scriptedModel{}, st, state.ID, SessionOptions{Store: st})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	status := resumed.ContextStatus()
	if status.CurrentTokens == 0 || status.OriginalTokens == 0 || status.HotTurnGroups == 0 {
		t.Fatalf("resumed context status was not projected from the ledger: %+v", status)
	}
	initial := resumed.Transcript()
	if got, want := len(initial), 101; got != want {
		t.Fatalf("initial transcript len = %d, want system + 100 latest messages", got)
	}
	if initial[1].Content != "user-10" {
		t.Fatalf("first initial body = %q, want user-10", initial[1].Content)
	}
	page, hasMore, err := resumed.LoadOlderTranscript(ctx, 100)
	if err != nil {
		t.Fatalf("LoadOlderTranscript: %v", err)
	}
	if hasMore || len(page) != 20 || page[0].Content != "user-00" {
		t.Fatalf("older page = %d hasMore=%v first=%q", len(page), hasMore, page[0].Content)
	}
	if got, want := len(resumed.Transcript()), 121; got != want {
		t.Fatalf("paged transcript len = %d, want %d", got, want)
	}
}

func TestOpenSessionRecoversInterruptedTurnBeforeNextAsk(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	state, err := st.CreateThread(ctx, store.ThreadMeta{ID: "thread-resume-interrupted"}, "system")
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	state, err = st.StartTurn(ctx, state.ID, state.Revision, store.TurnStart{TurnID: "turn-crashed", Input: "unfinished"})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	model := &scriptedModel{streams: []Stream{
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("recovered answer", nil)}}},
	}}
	if _, err := OpenSession(model, st, state.ID, SessionOptions{Store: st}); !errors.Is(err, ErrThreadHasActiveTurn) {
		t.Fatalf("normal OpenSession error = %v, want ErrThreadHasActiveTurn", err)
	}
	groups, err := st.LoadTurnGroups(ctx, state.ID)
	if err != nil {
		t.Fatalf("LoadTurnGroups before recovery: %v", err)
	}
	if len(groups) != 1 || groups[0].Failed != nil {
		t.Fatalf("normal resume modified active turn: %#v", groups)
	}
	session, err := OpenSession(model, st, state.ID, SessionOptions{Store: st, RecoverInterrupted: true})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	groups, err = st.LoadTurnGroups(ctx, state.ID)
	if err != nil {
		t.Fatalf("LoadTurnGroups: %v", err)
	}
	if len(groups) != 1 || groups[0].Failed == nil || groups[0].Committed != nil {
		t.Fatalf("interrupted turn was not terminally recovered: %#v", groups)
	}
	if err := session.Ask(ctx, "continue", nil); err != nil {
		t.Fatalf("Ask after recovery: %v", err)
	}
}

type eventScriptedModel struct {
	stream *scriptedStream
	raw    string
}

func (m *eventScriptedModel) Stream(ctx context.Context, messages []*schema.Message) (Stream, error) {
	return m.StreamWithEvents(ctx, messages, nil)
}

func (m *eventScriptedModel) StreamWithEvents(_ context.Context, _ []*schema.Message, emit EventEmitter) (Stream, error) {
	if emit != nil {
		emit(TurnEvent{Kind: TurnEventToolStart, Tool: "read_file", Input: `{"path":"large.log"}`})
		emit(TurnEvent{Kind: TurnEventToolEnd, Tool: "read_file", Output: m.raw})
	}
	return m.stream, nil
}
