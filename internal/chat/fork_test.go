package chat

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"eino-local-assistant/internal/contextbuild"
	"eino-local-assistant/internal/store"
	"eino-local-assistant/internal/usage"

	"github.com/cloudwego/eino/schema"
)

func TestSessionForkOpensChildWithProvenanceAndPreservesSource(t *testing.T) {
	ctx := context.Background()
	threadStore := newDurableThreadStore(t)
	model := &scriptedModel{streams: []Stream{
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("source answer", nil)}}},
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("child answer", nil)}}},
	}}
	compactor := checkpointCompactorFunc(func(context.Context, contextbuild.CompactionRequest, contextbuild.CompactionUsageObserver) (contextbuild.Checkpoint, error) {
		return contextbuild.Checkpoint{}, errors.New("compactor should not run during fork")
	})
	validator := func(string) error { return nil }
	source, err := NewSession(model, "frozen system prompt", SessionOptions{
		Store:                  threadStore,
		Title:                  "source title",
		ModelName:              "source-model",
		Pricing:                usage.Pricing{InputPerMillion: 1.25, OutputPerMillion: 4.5},
		Context:                contextbuild.Config{WindowTokens: 1200, MaxOutputTokens: 180, KeepRecentTurns: 3, AutoCompactTriggerPercent: 70, PostCompactTargetPercent: 40, SummaryMaxTokens: 160, LowGainThresholdPercent: 5},
		MaxLowGainAttempts:     7,
		Compactor:              compactor,
		FinalResponseValidator: validator,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := source.Ask(ctx, "source question", nil); err != nil {
		t.Fatalf("source Ask: %v", err)
	}

	sourceTranscriptBefore := source.Transcript()
	sourceStateBefore, err := threadStore.LoadThread(ctx, source.ID())
	if err != nil {
		t.Fatalf("LoadThread source before fork: %v", err)
	}

	child, result, err := source.Fork(ctx, "fork-child", "")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if child == nil || child == source {
		t.Fatalf("Fork child = %p, source = %p", child, source)
	}
	if result.SourceID != source.ID() || result.ChildID != "fork-child" || result.LastTurnID == "" || result.SourceHash == "" {
		t.Fatalf("fork result = %#v", result)
	}
	if result.ChildState.Meta.ParentID != source.ID() || result.ChildState.Meta.ForkBoundaryTurnID != result.LastTurnID || result.ChildState.Meta.ForkSourceHash != result.SourceHash {
		t.Fatalf("child provenance = %#v", result.ChildState.Meta)
	}
	childState, err := threadStore.LoadThread(ctx, child.ID())
	if err != nil {
		t.Fatalf("LoadThread child: %v", err)
	}
	if !reflect.DeepEqual(childState, result.ChildState) {
		t.Fatalf("child state = %#v, fork result state = %#v", childState, result.ChildState)
	}
	assertMessages(t, child.Transcript(), []messageExpectation{
		{role: schema.System, content: "frozen system prompt"},
		{role: schema.User, content: "source question"},
		{role: schema.Assistant, content: "source answer"},
	})

	if source.SystemPrompt() != "frozen system prompt" {
		t.Fatalf("source system prompt changed: %q", source.SystemPrompt())
	}
	if !reflect.DeepEqual(source.Transcript(), sourceTranscriptBefore) {
		t.Fatal("fork changed source transcript")
	}
	sourceStateAfterFork, err := threadStore.LoadThread(ctx, source.ID())
	if err != nil {
		t.Fatalf("LoadThread source after fork: %v", err)
	}
	if !reflect.DeepEqual(sourceStateAfterFork, sourceStateBefore) {
		t.Fatalf("fork changed source state:\nbefore=%#v\nafter=%#v", sourceStateBefore, sourceStateAfterFork)
	}

	if child.model != source.model || child.threads != source.threads {
		t.Fatal("child did not inherit the source model and repository")
	}
	if child.systemPrompt != source.systemPrompt || child.contextCfg != source.contextCfg || child.pricing != source.pricing || child.maxLowGainAttempts != source.maxLowGainAttempts {
		t.Fatalf("child runtime configuration = %#v, source = %#v", child, source)
	}
	if reflect.ValueOf(child.compactor).Pointer() != reflect.ValueOf(source.compactor).Pointer() {
		t.Fatal("child did not inherit the compactor")
	}
	if reflect.ValueOf(child.finalResponseValidator).Pointer() != reflect.ValueOf(source.finalResponseValidator).Pointer() {
		t.Fatal("child did not inherit the final-response validator")
	}

	if err := child.Ask(ctx, "child question", nil); err != nil {
		t.Fatalf("child Ask: %v", err)
	}
	assertMessages(t, child.Transcript(), []messageExpectation{
		{role: schema.System, content: "frozen system prompt"},
		{role: schema.User, content: "source question"},
		{role: schema.Assistant, content: "source answer"},
		{role: schema.User, content: "child question"},
		{role: schema.Assistant, content: "child answer"},
	})
	if !reflect.DeepEqual(source.Transcript(), sourceTranscriptBefore) {
		t.Fatal("child Ask changed source transcript")
	}
	sourceStateAfterChildAsk, err := threadStore.LoadThread(ctx, source.ID())
	if err != nil {
		t.Fatalf("LoadThread source after child Ask: %v", err)
	}
	if !reflect.DeepEqual(sourceStateAfterChildAsk, sourceStateBefore) {
		t.Fatal("child Ask changed source state")
	}
}

func TestSessionForkAfterModelSwitchUsesCurrentDurableIdentity(t *testing.T) {
	ctx := context.Background()
	threadStore := newDurableThreadStore(t)
	initialModel := &scriptedModel{streams: []Stream{
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("source answer", nil)}}},
	}}
	source, err := NewSession(initialModel, "frozen system prompt", SessionOptions{
		Store:     threadStore,
		ModelName: "model-before",
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := source.Ask(ctx, "source question", nil); err != nil {
		t.Fatalf("source Ask: %v", err)
	}
	boundaryState, err := threadStore.LoadThread(ctx, source.ID())
	if err != nil {
		t.Fatal(err)
	}
	replacement := &scriptedModel{}
	if err := source.ReplaceModel(ctx, ModelBinding{Model: replacement, ModelName: "model-after"}); err != nil {
		t.Fatalf("ReplaceModel: %v", err)
	}
	sourceState, err := threadStore.LoadThread(ctx, source.ID())
	if err != nil {
		t.Fatal(err)
	}
	if sourceState.Meta.Model != "model-after" || sourceState.Revision != boundaryState.Revision+1 {
		t.Fatalf("source model switch = %#v, boundary = %#v", sourceState, boundaryState)
	}

	child, result, err := source.Fork(ctx, "fork-model-session-child", "")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if child.Model() != replacement || child.ModelName() != "model-after" {
		t.Fatalf("fork runtime binding = %p/%q, want %p/model-after", child.Model(), child.ModelName(), replacement)
	}
	childState, err := threadStore.LoadThread(ctx, child.ID())
	if err != nil {
		t.Fatal(err)
	}
	if childState.Meta.Model != "model-after" || result.ChildState.Meta.Model != "model-after" {
		t.Fatalf("fork durable model = child=%q result=%q, want model-after", childState.Meta.Model, result.ChildState.Meta.Model)
	}
	if result.ChildState.Revision != boundaryState.Revision || result.SourceHash == sourceState.LastHash {
		t.Fatalf("fork boundary included post-boundary model event: result=%#v source=%#v boundary=%#v", result, sourceState, boundaryState)
	}

	beforeFirst, _, err := source.ForkBeforeFirstTurn(ctx, "fork-model-session-before-first")
	if err != nil {
		t.Fatalf("ForkBeforeFirstTurn: %v", err)
	}
	if beforeFirst.Model() != replacement || beforeFirst.ModelName() != "model-after" {
		t.Fatalf("before-first runtime binding = %p/%q, want %p/model-after", beforeFirst.Model(), beforeFirst.ModelName(), replacement)
	}
	beforeFirstState, err := threadStore.LoadThread(ctx, beforeFirst.ID())
	if err != nil {
		t.Fatal(err)
	}
	if beforeFirstState.Meta.Model != "model-after" || beforeFirstState.Revision != 1 {
		t.Fatalf("before-first durable model = %#v, want current model and creation-only prefix", beforeFirstState)
	}
}

func TestSessionForkBeforeFirstTurnOpensEmptyChildAndPreservesSource(t *testing.T) {
	ctx := context.Background()
	threadStore := newDurableThreadStore(t)
	source, err := NewSession(&scriptedModel{}, "frozen system prompt", SessionOptions{
		Store: threadStore,
		Title: "source title",
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	sourceTranscriptBefore := source.Transcript()
	sourceStateBefore, err := threadStore.LoadThread(ctx, source.ID())
	if err != nil {
		t.Fatalf("LoadThread source before fork: %v", err)
	}

	child, result, err := source.ForkBeforeFirstTurn(ctx, "before-first-child")
	if err != nil {
		t.Fatalf("ForkBeforeFirstTurn: %v", err)
	}
	if child == nil || child == source {
		t.Fatalf("ForkBeforeFirstTurn child = %p, source = %p", child, source)
	}
	if result.SourceID != source.ID() || result.ChildID != child.ID() || result.LastTurnID != "" || result.SourceHash == "" {
		t.Fatalf("fork result = %#v", result)
	}
	if result.ChildState.Meta.ParentID != source.ID() || result.ChildState.Meta.ForkBoundaryTurnID != "" || result.ChildState.Meta.ForkSourceHash != result.SourceHash {
		t.Fatalf("child provenance = %#v", result.ChildState.Meta)
	}
	assertMessages(t, child.Transcript(), []messageExpectation{{role: schema.System, content: "frozen system prompt"}})

	groups, err := threadStore.LoadTurnGroups(ctx, child.ID())
	if err != nil {
		t.Fatalf("LoadTurnGroups child: %v", err)
	}
	if len(groups) != 0 {
		t.Fatalf("child turn groups = %#v, want empty", groups)
	}
	if !reflect.DeepEqual(source.Transcript(), sourceTranscriptBefore) {
		t.Fatal("before-first fork changed source transcript")
	}
	sourceStateAfter, err := threadStore.LoadThread(ctx, source.ID())
	if err != nil {
		t.Fatalf("LoadThread source after fork: %v", err)
	}
	if !reflect.DeepEqual(sourceStateAfter, sourceStateBefore) {
		t.Fatalf("before-first fork changed source state:\nbefore=%#v\nafter=%#v", sourceStateBefore, sourceStateAfter)
	}
}

func TestSessionForkReturnsStableUnsupportedError(t *testing.T) {
	threadStore := newDurableThreadStore(t)
	repository := &forkUnsupportedRepository{ThreadRepository: threadStore}
	source, err := NewSession(&scriptedModel{}, "system", SessionOptions{Store: repository})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	child, result, err := source.Fork(context.Background(), "unsupported-child", "")
	if child != nil {
		t.Fatalf("unsupported fork child = %p, want nil", child)
	}
	if !reflect.DeepEqual(result, store.ForkResult{}) {
		t.Fatalf("unsupported fork result = %#v, want zero", result)
	}
	if err != ErrForkUnsupported || !errors.Is(err, ErrForkUnsupported) {
		t.Fatalf("unsupported fork error = %v, want stable sentinel", err)
	}

	child, result, err = source.ForkBeforeFirstTurn(context.Background(), "unsupported-before-first-child")
	if child != nil {
		t.Fatalf("unsupported before-first child = %p, want nil", child)
	}
	if !reflect.DeepEqual(result, store.ForkResult{}) {
		t.Fatalf("unsupported before-first result = %#v, want zero", result)
	}
	if err != ErrForkUnsupported || !errors.Is(err, ErrForkUnsupported) {
		t.Fatalf("unsupported before-first error = %v, want stable sentinel", err)
	}
}

func TestSessionForkPropagatesActiveAndPendingRejections(t *testing.T) {
	tests := []struct {
		name       string
		childID    string
		prepare    func(context.Context, *store.ThreadStore, store.ThreadState) store.ThreadState
		wantErr    error
		wantActive bool
	}{
		{
			name:    "active turn",
			childID: "active-child",
			prepare: func(ctx context.Context, threadStore *store.ThreadStore, state store.ThreadState) store.ThreadState {
				next, err := threadStore.StartTurn(ctx, state.ID, state.Revision, store.TurnStart{TurnID: "active-turn", Input: "unfinished"})
				if err != nil {
					t.Fatalf("StartTurn: %v", err)
				}
				return next
			},
			wantErr:    store.ErrForkActiveTurn,
			wantActive: true,
		},
		{
			name:    "pending compaction",
			childID: "pending-child",
			prepare: func(ctx context.Context, threadStore *store.ThreadStore, state store.ThreadState) store.ThreadState {
				state, err := threadStore.StartTurn(ctx, state.ID, state.Revision, store.TurnStart{TurnID: "committed-turn", Input: "finished"})
				if err != nil {
					t.Fatalf("StartTurn: %v", err)
				}
				state, err = threadStore.CommitTurn(ctx, state.ID, state.Revision, store.TurnCommit{
					TurnID:   "committed-turn",
					Messages: []*schema.Message{schema.UserMessage("finished"), schema.AssistantMessage("answer", nil)},
				})
				if err != nil {
					t.Fatalf("CommitTurn: %v", err)
				}
				state, err = threadStore.StartCompaction(ctx, state.ID, state.Revision, store.CompactionStart{OperationID: "pending-compaction"})
				if err != nil {
					t.Fatalf("StartCompaction: %v", err)
				}
				return state
			},
			wantErr: store.ErrForkPendingCompaction,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			threadStore := newDurableThreadStore(t)
			source, err := NewSession(&scriptedModel{}, "system", SessionOptions{Store: threadStore})
			if err != nil {
				t.Fatalf("NewSession: %v", err)
			}
			state, err := threadStore.LoadThread(ctx, source.ID())
			if err != nil {
				t.Fatalf("LoadThread before prepare: %v", err)
			}
			state = test.prepare(ctx, threadStore, state)
			beforeFork, err := threadStore.LoadThread(ctx, source.ID())
			if err != nil {
				t.Fatalf("LoadThread before fork: %v", err)
			}

			_, _, err = source.Fork(ctx, test.childID, "")
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Fork error = %v, want %v", err, test.wantErr)
			}
			afterFork, err := threadStore.LoadThread(ctx, source.ID())
			if err != nil {
				t.Fatalf("LoadThread after fork: %v", err)
			}
			if !reflect.DeepEqual(afterFork, beforeFork) {
				t.Fatalf("rejected fork changed source:\nbefore=%#v\nafter=%#v", beforeFork, afterFork)
			}
			if test.wantActive && afterFork.Revision != beforeFork.Revision {
				t.Fatal("active rejection unexpectedly recovered the source turn")
			}
			if _, loadErr := threadStore.LoadThread(ctx, test.childID); loadErr == nil {
				t.Fatal("rejected fork published a child")
			}
		})
	}
}

type forkUnsupportedRepository struct {
	store.ThreadRepository
}
