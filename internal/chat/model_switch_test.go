package chat

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"eino-local-assistant/internal/contextbuild"
	"eino-local-assistant/internal/store"
	"eino-local-assistant/internal/usage"

	"github.com/cloudwego/eino/schema"
)

func TestSessionReplaceModelPersistsIdentityAndResume(t *testing.T) {
	ctx := context.Background()
	threadStore := newDurableThreadStore(t)
	initialModel := &scriptedModel{streams: []Stream{
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("answer", nil)}}},
	}}
	initialCompactor := &modelBindingTestCompactor{}
	initialPricing := usage.Pricing{InputPerMillion: 1, OutputPerMillion: 2}
	session, err := NewSession(initialModel, "frozen system", SessionOptions{
		Store:           threadStore,
		ModelName:       "model-a",
		ReasoningEffort: "medium",
		Compactor:       initialCompactor,
		Pricing:         initialPricing,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := session.Ask(ctx, "hello", nil); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	id := session.ID()
	beforeTranscript := session.Transcript()
	beforeState, err := threadStore.LoadThread(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if beforeState.Meta.Model != "model-a" || beforeState.Meta.ReasoningEffort != "medium" {
		t.Fatalf("created model binding = %q/%q, want model-a/medium", beforeState.Meta.Model, beforeState.Meta.ReasoningEffort)
	}

	replacement := &scriptedModel{}
	replacementCompactor := &modelBindingTestCompactor{}
	replacementPricing := usage.Pricing{InputPerMillion: 3, OutputPerMillion: 4}
	if err := session.ReplaceModel(ctx, ModelBinding{
		Model:     replacement,
		ModelName: " model-b ",
		Compactor: replacementCompactor,
		Pricing:   replacementPricing,
	}); err != nil {
		t.Fatalf("ReplaceModel: %v", err)
	}
	if session.ID() != id || session.SystemPrompt() != "frozen system" {
		t.Fatalf("identity or system prompt changed: id=%q prompt=%q", session.ID(), session.SystemPrompt())
	}
	if session.Model() != replacement || session.ModelName() != "model-b" {
		t.Fatalf("active model = %p/%q, want %p/model-b", session.Model(), session.ModelName(), replacement)
	}
	if session.ReasoningEffort() != "medium" {
		t.Fatalf("active reasoning effort = %q, want medium", session.ReasoningEffort())
	}
	if session.compactor != replacementCompactor || session.pricing != replacementPricing {
		t.Fatalf("auxiliary binding = %p/%#v, want %p/%#v", session.compactor, session.pricing, replacementCompactor, replacementPricing)
	}
	if !reflect.DeepEqual(session.Transcript(), beforeTranscript) {
		t.Fatalf("transcript changed during model replacement: before=%#v after=%#v", beforeTranscript, session.Transcript())
	}
	afterState, err := threadStore.LoadThread(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if afterState.Meta.Model != "model-b" || afterState.Meta.ReasoningEffort != "medium" || afterState.Meta.MessageCount != beforeState.Meta.MessageCount || afterState.Meta.ModelCallCount != beforeState.Meta.ModelCallCount {
		t.Fatalf("persisted model replacement changed unrelated projection: before=%#v after=%#v", beforeState.Meta, afterState.Meta)
	}
	if afterState.Revision != beforeState.Revision+1 {
		t.Fatalf("revision after model replacement = %d, want %d", afterState.Revision, beforeState.Revision+1)
	}

	resumedProvider := &scriptedModel{}
	resumed, err := OpenSession(resumedProvider, threadStore, id, SessionOptions{Store: threadStore})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if resumed.ID() != id || resumed.SystemPrompt() != "frozen system" || resumed.ModelName() != "model-b" || resumed.ReasoningEffort() != "medium" {
		t.Fatalf("resumed identity = id=%q prompt=%q model=%q effort=%q", resumed.ID(), resumed.SystemPrompt(), resumed.ModelName(), resumed.ReasoningEffort())
	}
	if resumed.Model() != resumedProvider {
		t.Fatalf("resume did not retain caller-provided model object")
	}
	if !reflect.DeepEqual(resumed.Transcript(), beforeTranscript) {
		t.Fatalf("resume transcript changed after model replacement: %#v", resumed.Transcript())
	}
}

func TestSessionReplaceModelAllowsProviderDefaultIdentity(t *testing.T) {
	ctx := context.Background()
	threadStore := newDurableThreadStore(t)
	initialModel := &scriptedModel{}
	session, err := NewSession(initialModel, "system", SessionOptions{Store: threadStore, ModelName: "model-a"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	replacement := &scriptedModel{}
	if err := session.ReplaceModel(ctx, ModelBinding{Model: replacement}); err != nil {
		t.Fatalf("ReplaceModel provider default: %v", err)
	}
	if session.Model() != replacement || session.ModelName() != "" {
		t.Fatalf("provider-default replacement = %p/%q, want %p/empty", session.Model(), session.ModelName(), replacement)
	}
	state, err := threadStore.LoadThread(ctx, session.ID())
	if err != nil {
		t.Fatal(err)
	}
	if state.Meta.Model != "" {
		t.Fatalf("provider-default model identity = %q, want empty", state.Meta.Model)
	}
}

func TestSessionReplaceModelWithOptionsClearsAndSetsProviderDefaultEffort(t *testing.T) {
	ctx := context.Background()
	threadStore := newDurableThreadStore(t)
	session, err := NewSession(&scriptedModel{}, "system", SessionOptions{
		Store:           threadStore,
		ModelName:       "model-a",
		ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	if err := session.ReplaceModelWithOptions(ctx, ModelBinding{Model: &scriptedModel{}}); err != nil {
		t.Fatalf("clear provider default effort: %v", err)
	}
	if session.ModelName() != "model-a" || session.ReasoningEffort() != "" {
		t.Fatalf("cleared provider default effort = %q/%q, want model-a/empty", session.ModelName(), session.ReasoningEffort())
	}
	state, err := threadStore.LoadThread(ctx, session.ID())
	if err != nil {
		t.Fatalf("LoadThread after clear: %v", err)
	}
	if state.Meta.Model != "model-a" || state.Meta.ReasoningEffort != "" {
		t.Fatalf("persisted cleared provider default effort = %q/%q, want model-a/empty", state.Meta.Model, state.Meta.ReasoningEffort)
	}

	if err := session.ReplaceModelWithOptions(ctx, ModelBinding{
		Model:           &scriptedModel{},
		ReasoningEffort: "low",
	}); err != nil {
		t.Fatalf("set provider default effort: %v", err)
	}
	if session.ModelName() != "model-a" || session.ReasoningEffort() != "low" {
		t.Fatalf("set provider default effort = %q/%q, want model-a/low", session.ModelName(), session.ReasoningEffort())
	}
	state, err = threadStore.LoadThread(ctx, session.ID())
	if err != nil {
		t.Fatalf("LoadThread after set: %v", err)
	}
	if state.Meta.Model != "model-a" || state.Meta.ReasoningEffort != "low" {
		t.Fatalf("persisted provider default binding = %q/%q, want model-a/low", state.Meta.Model, state.Meta.ReasoningEffort)
	}
}

func TestOpenSessionLegacyEffortPayloadFallsBackToProviderDefault(t *testing.T) {
	ctx := context.Background()
	threadStore := newDurableThreadStore(t)
	session, err := NewSession(&scriptedModel{}, "system", SessionOptions{
		Store:           threadStore,
		ModelName:       "model-a",
		ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	state, err := threadStore.LoadThread(ctx, session.ID())
	if err != nil {
		t.Fatalf("LoadThread: %v", err)
	}
	if _, err := threadStore.SetThreadModel(ctx, session.ID(), state.Revision, "model-a"); err != nil {
		t.Fatalf("SetThreadModel: %v", err)
	}

	legacyOpen, err := OpenSession(&scriptedModel{}, threadStore, session.ID(), SessionOptions{
		Store:           threadStore,
		ReasoningEffort: "low",
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if legacyOpen.ReasoningEffort() != "" {
		t.Fatalf("legacy effort = %q, want provider default", legacyOpen.ReasoningEffort())
	}
}

func TestSessionReplaceModelCASFailureKeepsBinding(t *testing.T) {
	ctx := context.Background()
	threadStore := newDurableThreadStore(t)
	initialModel := &scriptedModel{}
	initialCompactor := &modelBindingTestCompactor{}
	initialPricing := usage.Pricing{InputPerMillion: 1, OutputPerMillion: 2}
	session, err := NewSession(initialModel, "system", SessionOptions{
		Store:     threadStore,
		ModelName: "model-a",
		Compactor: initialCompactor,
		Pricing:   initialPricing,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	state, err := threadStore.LoadThread(ctx, session.ID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := threadStore.SetThreadTitle(ctx, session.ID(), state.Revision, "external writer"); err != nil {
		t.Fatal(err)
	}
	replacementCompactor := &modelBindingTestCompactor{}
	replacementPricing := usage.Pricing{InputPerMillion: 3, OutputPerMillion: 4}
	if err := session.ReplaceModel(ctx, ModelBinding{
		Model:     &scriptedModel{},
		ModelName: "model-b",
		Compactor: replacementCompactor,
		Pricing:   replacementPricing,
	}); !errors.Is(err, store.ErrRevisionConflict) {
		t.Fatalf("stale ReplaceModel error = %v, want ErrRevisionConflict", err)
	}
	if session.Model() != initialModel || session.ModelName() != "model-a" || session.compactor != initialCompactor || session.pricing != initialPricing {
		t.Fatalf("failed model replacement changed active binding: model=%p name=%q compactor=%p pricing=%#v", session.Model(), session.ModelName(), session.compactor, session.pricing)
	}
	loaded, err := threadStore.LoadThread(ctx, session.ID())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Meta.Model != "model-a" {
		t.Fatalf("failed model replacement changed persisted identity to %q", loaded.Meta.Model)
	}
}

func TestSessionReplaceModelDoesNotWaitForActiveTurn(t *testing.T) {
	threadStore := newDurableThreadStore(t)
	stream := &blockingStream{closed: make(chan struct{})}
	started := make(chan struct{})
	initialCompactor := &modelBindingTestCompactor{}
	initialPricing := usage.Pricing{InputPerMillion: 1, OutputPerMillion: 2}
	initialModel := &scriptedModel{
		streams: []Stream{stream},
		beforeStream: func() {
			close(started)
		},
	}
	session, err := NewSession(initialModel, "system", SessionOptions{
		Store:     threadStore,
		ModelName: "model-a",
		Compactor: initialCompactor,
		Pricing:   initialPricing,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	turnCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	turnDone := make(chan error, 1)
	go func() {
		turnDone <- session.Ask(turnCtx, "long-running", nil)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("model turn did not start")
	}

	replacement := &scriptedModel{}
	replacementCompactor := &modelBindingTestCompactor{}
	replacementPricing := usage.Pricing{InputPerMillion: 3, OutputPerMillion: 4}
	if err := session.ReplaceModel(context.Background(), ModelBinding{
		Model:     replacement,
		ModelName: "model-b",
		Compactor: replacementCompactor,
		Pricing:   replacementPricing,
	}); !errors.Is(err, ErrModelChangeBusy) {
		t.Fatalf("busy ReplaceModel error = %v, want ErrModelChangeBusy", err)
	}
	state, err := threadStore.LoadThread(context.Background(), session.ID())
	if err != nil {
		t.Fatal(err)
	}
	if state.Meta.Model != "model-a" {
		t.Fatalf("busy model replacement changed persisted identity to %q", state.Meta.Model)
	}
	if session.Model() != initialModel || session.ModelName() != "model-a" || session.compactor != initialCompactor || session.pricing != initialPricing {
		t.Fatalf("busy model replacement changed active binding: model=%p name=%q compactor=%p pricing=%#v", session.Model(), session.ModelName(), session.compactor, session.pricing)
	}
	cancel()
	select {
	case <-turnDone:
	case <-time.After(time.Second):
		t.Fatal("cancelled model turn did not finish")
	}
}

func TestSessionReplaceModelRejectsDurableActiveAndPendingOperations(t *testing.T) {
	ctx := context.Background()
	threadStore := newDurableThreadStore(t)
	initialModel := &scriptedModel{}
	session, err := NewSession(initialModel, "system", SessionOptions{Store: threadStore, ModelName: "model-a"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	state, err := threadStore.LoadThread(ctx, session.ID())
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.StartTurn(ctx, state.ID, state.Revision, store.TurnStart{TurnID: "active-turn", Input: "work"})
	if err != nil {
		t.Fatal(err)
	}
	session.applyThreadState(state)
	if err := session.ReplaceModel(ctx, ModelBinding{Model: &scriptedModel{}, ModelName: "model-b"}); !errors.Is(err, store.ErrModelChangeActiveTurn) {
		t.Fatalf("durable active ReplaceModel error = %v, want ErrModelChangeActiveTurn", err)
	}
	state, err = threadStore.FailTurn(ctx, state.ID, state.Revision, store.TurnFailure{TurnID: "active-turn", Error: "test cleanup"})
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.StartCompaction(ctx, state.ID, state.Revision, store.CompactionStart{OperationID: "pending-compaction"})
	if err != nil {
		t.Fatal(err)
	}
	session.applyThreadState(state)
	if err := session.ReplaceModel(ctx, ModelBinding{Model: &scriptedModel{}, ModelName: "model-b"}); !errors.Is(err, store.ErrModelChangePendingCompaction) {
		t.Fatalf("durable pending ReplaceModel error = %v, want ErrModelChangePendingCompaction", err)
	}
	loaded, err := threadStore.LoadThread(ctx, session.ID())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Meta.Model != "model-a" || loaded.Revision != state.Revision || loaded.PendingCompaction == nil {
		t.Fatalf("durable rejection mutated thread: %#v", loaded)
	}
}

type legacyModelSwitchRepository struct {
	store.ThreadRepository
}

func TestSessionReplaceModelKeepsLegacyRepositoryCompatibility(t *testing.T) {
	legacy := &legacyModelSwitchRepository{ThreadRepository: newDurableThreadStore(t)}
	session, err := NewSession(&scriptedModel{}, "system", SessionOptions{Store: legacy})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := session.ReplaceModel(context.Background(), ModelBinding{Model: &scriptedModel{}, ModelName: "model-b"}); !errors.Is(err, ErrModelChangeUnsupported) {
		t.Fatalf("legacy repository error = %v, want ErrModelChangeUnsupported", err)
	}
}

func TestSessionReplaceModelRejectsEffortOnLegacyRepository(t *testing.T) {
	legacy := &legacyModelSwitchRepository{ThreadRepository: newDurableThreadStore(t)}
	session, err := NewSession(&scriptedModel{}, "system", SessionOptions{Store: legacy, ModelName: "model-a"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	before, err := legacy.LoadThread(context.Background(), session.ID())
	if err != nil {
		t.Fatalf("LoadThread: %v", err)
	}
	replacement := &scriptedModel{}
	if err := session.ReplaceModel(context.Background(), ModelBinding{
		Model:           replacement,
		ModelName:       "model-b",
		ReasoningEffort: "high",
	}); !errors.Is(err, ErrModelChangeUnsupported) {
		t.Fatalf("legacy effort error = %v, want ErrModelChangeUnsupported", err)
	}
	if session.ModelName() != "model-a" || session.ReasoningEffort() != "" || session.Model() == replacement {
		t.Fatalf("failed legacy effort replacement changed local binding: model=%p name=%q effort=%q", session.Model(), session.ModelName(), session.ReasoningEffort())
	}
	after, err := legacy.LoadThread(context.Background(), session.ID())
	if err != nil {
		t.Fatalf("LoadThread after rejection: %v", err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("failed legacy effort replacement changed durable state:\nbefore=%#v\nafter=%#v", before, after)
	}
}

func TestSessionReplaceModelUsesBindingRepositoryForEffortOnlyChange(t *testing.T) {
	ctx := context.Background()
	threadStore := newDurableThreadStore(t)
	session, err := NewSession(&scriptedModel{}, "system", SessionOptions{
		Store:           threadStore,
		ModelName:       "model-a",
		ReasoningEffort: "low",
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	replacement := &scriptedModel{}
	if err := session.ReplaceModel(ctx, ModelBinding{Model: replacement, ReasoningEffort: "high"}); err != nil {
		t.Fatalf("effort-only ReplaceModel: %v", err)
	}
	if session.Model() != replacement || session.ModelName() != "model-a" || session.ReasoningEffort() != "high" {
		t.Fatalf("effort-only local binding = %p/%q/%q, want replacement/model-a/high", session.Model(), session.ModelName(), session.ReasoningEffort())
	}
	state, err := threadStore.LoadThread(ctx, session.ID())
	if err != nil {
		t.Fatalf("LoadThread: %v", err)
	}
	if state.Meta.Model != "model-a" || state.Meta.ReasoningEffort != "high" {
		t.Fatalf("effort-only durable binding = %q/%q, want model-a/high", state.Meta.Model, state.Meta.ReasoningEffort)
	}
}

type modelBindingTestCompactor struct{}

func (*modelBindingTestCompactor) Compact(context.Context, contextbuild.CompactionRequest, contextbuild.CompactionUsageObserver) (contextbuild.Checkpoint, error) {
	return contextbuild.Checkpoint{}, nil
}
