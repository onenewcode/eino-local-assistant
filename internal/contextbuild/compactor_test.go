package contextbuild

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"eino-local-assistant/internal/usage"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type recordingCheckpointCompactor struct {
	calls          []CompactionRequest
	callID         string
	err            error
	usage          usage.Turn
	usageAvailable bool
	reportUsage    bool
}

type checkpointCompactorFunc func(context.Context, CompactionRequest, CompactionUsageObserver) (Checkpoint, error)

func (f checkpointCompactorFunc) Compact(ctx context.Context, request CompactionRequest, observer CompactionUsageObserver) (Checkpoint, error) {
	return f(ctx, request, observer)
}

func (c *recordingCheckpointCompactor) Compact(_ context.Context, request CompactionRequest, observer CompactionUsageObserver) (Checkpoint, error) {
	c.calls = append(c.calls, request)
	if c.reportUsage && observer != nil {
		callID := c.callID
		if callID == "" {
			callID = "model"
		}
		observer(callID, c.usage, c.usageAvailable)
	}
	if c.err != nil {
		return Checkpoint{}, c.err
	}
	return DeterministicCheckpoint(request)
}

type scriptedCompactorModel struct {
	response *schema.Message
	err      error
	calls    int
}

func (m *scriptedCompactorModel) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.calls++
	return m.response, m.err
}

func (m *scriptedCompactorModel) Stream(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if m.err != nil {
		return nil, m.err
	}
	return schema.StreamReaderFromArray([]*schema.Message{m.response}), nil
}

func compactionTestRequest() CompactionRequest {
	return CompactionRequest{SourceGroups: []TurnGroup{{
		ID:             "group-1",
		SourceEventIDs: []string{"event-1"},
		Messages:       []*schema.Message{schema.UserMessage("source")},
	}}}
}

func TestModelCompactorReportsUsageBeforeCheckpointValidation(t *testing.T) {
	expected := usage.Turn{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}
	chatModel := &scriptedCompactorModel{response: &schema.Message{
		Role:    schema.Assistant,
		Content: "not checkpoint JSON",
		ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
			PromptTokens:     expected.PromptTokens,
			CompletionTokens: expected.CompletionTokens,
			TotalTokens:      expected.TotalTokens,
		}},
	}}
	compactor, err := NewModelCompactor(chatModel, DefaultConfig())
	if err != nil {
		t.Fatalf("NewModelCompactor: %v", err)
	}
	var observed []usage.Turn
	_, err = compactor.Compact(context.Background(), compactionTestRequest(), func(_ string, turn usage.Turn, available bool) {
		if !available {
			t.Fatal("provider usage reported unavailable")
		}
		observed = append(observed, turn)
	})
	if err == nil || !strings.Contains(err.Error(), "parse generated checkpoint") {
		t.Fatalf("Compact error = %v, want checkpoint parse error", err)
	}
	if len(observed) != 1 || observed[0] != expected {
		t.Fatalf("usage reports = %#v, want %#v", observed, []usage.Turn{expected})
	}
}

func TestModelCompactorDoesNotReportUsageWhenGenerateFailsWithoutResponse(t *testing.T) {
	chatModel := &scriptedCompactorModel{err: errors.New("provider unavailable")}
	compactor, err := NewModelCompactor(chatModel, DefaultConfig())
	if err != nil {
		t.Fatalf("NewModelCompactor: %v", err)
	}
	reports := 0
	available := true
	observed := usage.Turn{PromptTokens: -1}
	_, err = compactor.Compact(context.Background(), compactionTestRequest(), func(_ string, turn usage.Turn, gotUsage bool) {
		reports++
		available = gotUsage
		observed = turn
	})
	if err == nil || !strings.Contains(err.Error(), "provider unavailable") {
		t.Fatalf("Compact error = %v, want provider error", err)
	}
	if reports != 0 || !available || observed != (usage.Turn{PromptTokens: -1}) {
		t.Fatalf("usage report = %+v available=%v reports=%d, want no report", observed, available, reports)
	}
}

func TestModelCompactorDoesNotReportUsageForEmptyResponse(t *testing.T) {
	compactor, err := NewModelCompactor(&scriptedCompactorModel{}, DefaultConfig())
	if err != nil {
		t.Fatalf("NewModelCompactor: %v", err)
	}
	reports := 0
	_, err = compactor.Compact(context.Background(), compactionTestRequest(), func(_ string, _ usage.Turn, _ bool) {
		reports++
	})
	if err == nil || !strings.Contains(err.Error(), "empty response") {
		t.Fatalf("Compact error = %v, want empty response", err)
	}
	if reports != 0 {
		t.Fatalf("empty response produced %d usage reports, want none", reports)
	}
}

func TestModelCompactorClassifiesBlankResponseAndPreservesUsage(t *testing.T) {
	expected := usage.Turn{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}
	chatModel := &scriptedCompactorModel{response: &schema.Message{
		Role: schema.Assistant,
		ResponseMeta: &schema.ResponseMeta{
			FinishReason: "length",
			Usage: &schema.TokenUsage{
				PromptTokens:     expected.PromptTokens,
				CompletionTokens: expected.CompletionTokens,
				TotalTokens:      expected.TotalTokens,
			},
		},
	}}
	compactor, err := NewModelCompactor(chatModel, DefaultConfig())
	if err != nil {
		t.Fatalf("NewModelCompactor: %v", err)
	}
	var observed []usage.Turn
	_, err = compactor.Compact(context.Background(), compactionTestRequest(), func(_ string, turn usage.Turn, available bool) {
		if !available {
			t.Fatal("blank provider response lost reported usage")
		}
		observed = append(observed, turn)
	})
	if !errors.Is(err, ErrEmptyCheckpointResponse) {
		t.Fatalf("Compact() error = %v, want empty checkpoint response", err)
	}
	var empty *EmptyCheckpointResponseError
	if !errors.As(err, &empty) || empty.FinishReason != "length" {
		t.Fatalf("empty response detail = %#v, want finish reason length", empty)
	}
	if len(observed) != 1 || observed[0] != expected {
		t.Fatalf("usage reports = %#v, want %#v", observed, []usage.Turn{expected})
	}
}

func TestModelCompactorBlocksOverBudgetRequestBeforeProvider(t *testing.T) {
	chatModel := &scriptedCompactorModel{}
	compactor, err := NewModelCompactor(chatModel, Config{WindowTokens: 500})
	if err != nil {
		t.Fatalf("NewModelCompactor: %v", err)
	}
	request := compactionTestRequest()
	request.SourceGroups[0].Messages = []*schema.Message{schema.UserMessage(strings.Repeat("source ", 2_000))}
	_, err = compactor.Compact(context.Background(), request, nil)
	if !errors.Is(err, ErrRequestAdmissionExceeded) {
		t.Fatalf("Compact() error = %v, want prompt budget admission failure", err)
	}
	if chatModel.calls != 0 {
		t.Fatalf("provider calls = %d, want 0 after local admission rejection", chatModel.calls)
	}
}

func TestModelCompactorReportsExactUsageWhenGenerateFailsWithResponse(t *testing.T) {
	expected := usage.Turn{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}
	chatModel := &scriptedCompactorModel{
		err: errors.New("stream interrupted"),
		response: &schema.Message{ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
			PromptTokens:     expected.PromptTokens,
			CompletionTokens: expected.CompletionTokens,
			TotalTokens:      expected.TotalTokens,
		}}},
	}
	compactor, err := NewModelCompactor(chatModel, DefaultConfig())
	if err != nil {
		t.Fatalf("NewModelCompactor: %v", err)
	}
	var observed []usage.Turn
	_, err = compactor.Compact(context.Background(), compactionTestRequest(), func(callID string, turn usage.Turn, available bool) {
		if callID == "" || !available {
			t.Fatalf("usage callback = id=%q available=%v", callID, available)
		}
		observed = append(observed, turn)
	})
	if err == nil || !strings.Contains(err.Error(), "stream interrupted") {
		t.Fatalf("Compact error = %v, want provider error", err)
	}
	if len(observed) != 1 || observed[0] != expected {
		t.Fatalf("usage reports = %#v, want %#v", observed, []usage.Turn{expected})
	}
}

func TestModelCompactorReportsUnavailableUsageWhenProviderOmitsIt(t *testing.T) {
	chatModel := &scriptedCompactorModel{response: schema.AssistantMessage("not checkpoint JSON", nil)}
	compactor, err := NewModelCompactor(chatModel, DefaultConfig())
	if err != nil {
		t.Fatalf("NewModelCompactor: %v", err)
	}
	reports := 0
	available := true
	_, err = compactor.Compact(context.Background(), compactionTestRequest(), func(_ string, _ usage.Turn, gotUsage bool) {
		reports++
		available = gotUsage
	})
	if err == nil || !strings.Contains(err.Error(), "parse generated checkpoint") {
		t.Fatalf("Compact error = %v, want checkpoint parse error", err)
	}
	if reports != 1 || available {
		t.Fatalf("usage availability = %v after %d reports, want one unavailable report", available, reports)
	}
}

func TestRecursiveCompactorNormalizesEveryChunkIdentity(t *testing.T) {
	compactor := &recordingCheckpointCompactor{
		reportUsage:    true,
		usageAvailable: true,
		usage: usage.Turn{
			PromptTokens:     11,
			CompletionTokens: 3,
			TotalTokens:      14,
		},
	}
	cfg := Config{
		WindowTokens: 7_000,
	}
	recursive, err := NewRecursiveCompactor(compactor, cfg)
	if err != nil {
		t.Fatalf("NewRecursiveCompactor: %v", err)
	}
	groups := make([]TurnGroup, 0, 3)
	for i := 1; i <= 3; i++ {
		groups = append(groups, TurnGroup{
			ID:             fmt.Sprintf("group-%d", i),
			SourceEventIDs: []string{fmt.Sprintf("event-%d", i)},
			Messages:       []*schema.Message{schema.UserMessage(strings.Repeat("x", 8_000))},
		})
	}
	observed := make([]usage.Turn, 0)
	result, err := recursive.CompactWithResult(context.Background(), CompactionRequest{
		TaskGoal:     "retain decisions",
		SourceGroups: groups,
	}, func(_ string, turn usage.Turn, available bool) {
		if !available {
			t.Fatal("recursive compactor forwarded unavailable usage")
		}
		observed = append(observed, turn)
	})
	if err != nil {
		t.Fatalf("CompactWithResult: %v", err)
	}
	if result.Checkpoint.SchemaVersion != CheckpointSchemaVersion {
		t.Fatalf("checkpoint schema = %d, want %d", result.Checkpoint.SchemaVersion, CheckpointSchemaVersion)
	}
	if len(compactor.calls) < 3 {
		t.Fatalf("compactor calls = %d, want multiple chunks plus merge", len(compactor.calls))
	}
	for i, call := range compactor.calls {
		provenance, identityErr := call.sourceIdentity()
		if identityErr != nil || len(provenance.DirectSource.EventIDs) == 0 || provenance.DirectSource.ContentHash == "" {
			t.Fatalf("call %d has no normalized source identity: %+v", i, call)
		}
	}
	if len(observed) != len(compactor.calls) {
		t.Fatalf("usage reports = %d, want one per compactor call (%d)", len(observed), len(compactor.calls))
	}
	for i, turn := range observed {
		if turn != compactor.usage {
			t.Fatalf("usage report %d = %+v, want %+v", i, turn, compactor.usage)
		}
	}
}

func TestRecursiveCompactorPropagatesCancellation(t *testing.T) {
	compactor := &recordingCheckpointCompactor{err: context.Canceled}
	recursive, err := NewRecursiveCompactor(compactor, DefaultConfig())
	if err != nil {
		t.Fatalf("NewRecursiveCompactor: %v", err)
	}
	_, err = recursive.CompactWithResult(context.Background(), CompactionRequest{
		SourceGroups: []TurnGroup{{
			ID:             "group-1",
			SourceEventIDs: []string{"event-1"},
			Messages:       []*schema.Message{schema.UserMessage("source")},
		}},
	}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CompactWithResult error = %v, want context.Canceled", err)
	}
}

func TestRecursiveCompactorReturnsProviderFailureWithoutFallback(t *testing.T) {
	compactor := &recordingCheckpointCompactor{err: errors.New("provider unavailable")}
	recursive, err := NewRecursiveCompactor(compactor, Config{
		WindowTokens: 2_000,
	})
	if err != nil {
		t.Fatalf("NewRecursiveCompactor: %v", err)
	}
	_, err = recursive.CompactWithResult(context.Background(), CompactionRequest{
		SourceGroups: []TurnGroup{{
			ID:             "group-1",
			SourceEventIDs: []string{"event-1"},
			Messages:       []*schema.Message{schema.UserMessage("source")},
		}},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "provider unavailable") {
		t.Fatalf("CompactWithResult error = %v, want provider failure", err)
	}
}

func TestRecursiveCompactorRejectsLowGainWithoutCheckpoint(t *testing.T) {
	recursive, err := NewRecursiveCompactor(&recordingCheckpointCompactor{}, Config{
		WindowTokens: 8_000,
	})
	if err != nil {
		t.Fatalf("NewRecursiveCompactor: %v", err)
	}
	result, err := recursive.CompactWithResult(context.Background(), compactionTestRequest(), nil)
	if !errors.Is(err, ErrCompactionLowGain) {
		t.Fatalf("CompactWithResult error = %v, want ErrCompactionLowGain", err)
	}
	if result.Checkpoint.SchemaVersion != 0 {
		t.Fatalf("low-gain result installed checkpoint: %#v", result)
	}
}

func TestCompactionSourceIdentityBoundsModelVisibleEvidenceRefs(t *testing.T) {
	groups := make([]TurnGroup, 0, MaxCheckpointEvidenceRefs+8)
	for i := 0; i < MaxCheckpointEvidenceRefs+8; i++ {
		groups = append(groups, TurnGroup{
			ID:             fmt.Sprintf("group-%02d", i),
			SourceEventIDs: []string{fmt.Sprintf("event-%02d", i)},
			Messages:       []*schema.Message{schema.UserMessage("source")},
		})
	}
	provenance, err := (CompactionRequest{SourceGroups: groups}).sourceIdentity()
	if err != nil {
		t.Fatalf("sourceIdentity: %v", err)
	}
	ids := provenance.DirectSource.EventIDs
	if len(ids) != MaxCheckpointEvidenceRefs {
		t.Fatalf("evidence refs = %d, want %d", len(ids), MaxCheckpointEvidenceRefs)
	}
	if ids[0] != "event-00" || ids[len(ids)-1] != fmt.Sprintf("event-%02d", MaxCheckpointEvidenceRefs+7) {
		t.Fatalf("source anchors did not preserve range ends: %#v", ids)
	}
}

func TestCompactionRequestRejectsMismatchedExplicitDirectIdentity(t *testing.T) {
	request := compactionTestRequest()
	request.DirectSourceEventIDs = []string{"foreign-event"}
	if _, err := request.sourceIdentity(); err == nil || !strings.Contains(err.Error(), "do not match source groups") {
		t.Fatalf("mismatched direct source ids error = %v", err)
	}

	request = compactionTestRequest()
	request.DirectSourceHash = strings.Repeat("a", 64)
	if _, err := request.sourceIdentity(); err == nil || !strings.Contains(err.Error(), "does not match source groups") {
		t.Fatalf("mismatched direct source hash error = %v", err)
	}
}

func TestChunkBudgetIncludesRootParentCheckpoint(t *testing.T) {
	parent, err := DeterministicCheckpoint(compactionTestRequest())
	if err != nil {
		t.Fatal(err)
	}
	parent.ID = "budget-parent"
	parent.StorageHash = strings.Repeat("a", 64)
	groups := []TurnGroup{
		{ID: "budget-group-1", SourceEventIDs: []string{"budget-event-1"}, Messages: []*schema.Message{schema.UserMessage(strings.Repeat("one ", 100))}},
		{ID: "budget-group-2", SourceEventIDs: []string{"budget-event-2"}, Messages: []*schema.Message{schema.UserMessage(strings.Repeat("two ", 100))}},
	}
	request := CompactionRequest{SourceGroups: groups, Previous: &parent}
	withParent, err := compactionRequestTokens(request, groups)
	if err != nil {
		t.Fatal(err)
	}
	withoutParent := request
	withoutParent.Previous = nil
	without, err := compactionRequestTokens(withoutParent, groups)
	if err != nil {
		t.Fatal(err)
	}
	if withParent <= without {
		t.Fatalf("parent was not included in request budget: with=%d without=%d", withParent, without)
	}
	recursive, err := NewRecursiveCompactor(&recordingCheckpointCompactor{}, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := recursive.chunkForCompaction(request, (withParent+without)/2)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 {
		t.Fatalf("root request was not split with parent-aware budget: %#v", chunks)
	}
}

func TestRecursiveCompactorSeparatesParentFromSingleOverflowedGroup(t *testing.T) {
	parent, err := DeterministicCheckpoint(compactionTestRequest())
	if err != nil {
		t.Fatal(err)
	}
	parent.ID = "single-overflow-parent"
	parent.StorageHash = strings.Repeat("a", 64)

	raw := TurnGroup{
		ID:             "single-overflow-raw",
		SourceEventIDs: []string{"single-overflow-event"},
		Messages:       []*schema.Message{schema.UserMessage(strings.Repeat("raw evidence ", 3_000))},
	}
	root := CompactionRequest{SourceGroups: []TurnGroup{raw}, Previous: &parent}
	scope, err := root.directSourceScope()
	if err != nil {
		t.Fatal(err)
	}
	root.DirectSourceEventIDs = append([]string(nil), scope.EventIDs...)
	root.DirectSourceHash = scope.Hash
	root.sourceScope = &scope

	child := root
	child.Previous = nil
	child.DirectSourceEventIDs = nil
	child.DirectSourceHash = ""
	child.sourceScope = nil
	childTokens, err := compactionRequestTokens(child, child.SourceGroups)
	if err != nil {
		t.Fatal(err)
	}
	childCheckpoint, err := DeterministicCheckpoint(child)
	if err != nil {
		t.Fatal(err)
	}
	merge := root
	merge.SourceGroups = []TurnGroup{{
		ID:                        "checkpoint-merge-1",
		SourceEventIDs:            childCheckpoint.DirectEvidenceEventIDs(),
		Messages:                  []*schema.Message{schema.UserMessage(childCheckpoint.PromptText())},
		TokenEstimate:             childCheckpoint.EstimatedTokens(),
		derivedCheckpoint:         true,
		visibleCheckpointEventIDs: childCheckpoint.modelVisibleDirectEventIDs(),
	}}
	mergeTokens, err := compactionRequestTokens(merge, merge.SourceGroups)
	if err != nil {
		t.Fatal(err)
	}
	rootTokens, err := compactionRequestTokens(root, root.SourceGroups)
	if err != nil {
		t.Fatal(err)
	}
	budget := max(childTokens, mergeTokens) + 1
	if rootTokens <= budget {
		t.Fatalf("fixture did not overflow only with the parent: root=%d child=%d merge=%d budget=%d", rootTokens, childTokens, mergeTokens, budget)
	}

	compactor := &recordingCheckpointCompactor{}
	recursive, err := NewRecursiveCompactor(compactor, Config{
		WindowTokens: budget + 128,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recursive.CompactWithResult(context.Background(), CompactionRequest{SourceGroups: []TurnGroup{raw}, Previous: &parent}, nil); err != nil {
		t.Fatalf("CompactWithResult: %v", err)
	}
	if len(compactor.calls) != 2 {
		t.Fatalf("provider calls = %d, want child plus merge", len(compactor.calls))
	}
	if compactor.calls[0].Previous != nil || compactor.calls[1].Previous == nil {
		t.Fatalf("parent was not separated into child and merge requests: %#v", compactor.calls)
	}
	for i, call := range compactor.calls {
		tokens, tokenErr := compactionRequestTokens(call, call.SourceGroups)
		if tokenErr != nil {
			t.Fatalf("call %d tokens: %v", i, tokenErr)
		}
		if tokens > budget {
			t.Fatalf("call %d was over budget: %d > %d", i, tokens, budget)
		}
	}
}

func TestRecursiveCompactorRejectsOversizedSingleSyntheticMerge(t *testing.T) {
	parent, err := DeterministicCheckpoint(compactionTestRequest())
	if err != nil {
		t.Fatal(err)
	}
	parent.ID = "oversized-merge-parent"
	parent.StorageHash = strings.Repeat("b", 64)
	parent.TaskGoal = strings.Repeat("retained parent evidence ", 1_000)
	if err := parent.Validate(); err != nil {
		t.Fatalf("parent.Validate: %v", err)
	}

	raw := TurnGroup{
		ID:             "oversized-merge-raw",
		SourceEventIDs: []string{"oversized-merge-event"},
		Messages:       []*schema.Message{schema.UserMessage("small raw source")},
	}
	root := CompactionRequest{SourceGroups: []TurnGroup{raw}, Previous: &parent}
	scope, err := root.directSourceScope()
	if err != nil {
		t.Fatal(err)
	}
	root.DirectSourceEventIDs = append([]string(nil), scope.EventIDs...)
	root.DirectSourceHash = scope.Hash
	root.sourceScope = &scope

	child := root
	child.Previous = nil
	child.DirectSourceEventIDs = nil
	child.DirectSourceHash = ""
	child.sourceScope = nil
	childTokens, err := compactionRequestTokens(child, child.SourceGroups)
	if err != nil {
		t.Fatal(err)
	}
	childCheckpoint, err := DeterministicCheckpoint(child)
	if err != nil {
		t.Fatal(err)
	}
	merge := root
	merge.SourceGroups = []TurnGroup{{
		ID:                        "checkpoint-merge-1",
		SourceEventIDs:            childCheckpoint.DirectEvidenceEventIDs(),
		Messages:                  []*schema.Message{schema.UserMessage(childCheckpoint.PromptText())},
		TokenEstimate:             childCheckpoint.EstimatedTokens(),
		derivedCheckpoint:         true,
		visibleCheckpointEventIDs: childCheckpoint.modelVisibleDirectEventIDs(),
	}}
	mergeTokens, err := compactionRequestTokens(merge, merge.SourceGroups)
	if err != nil {
		t.Fatal(err)
	}
	rootTokens, err := compactionRequestTokens(root, root.SourceGroups)
	if err != nil {
		t.Fatal(err)
	}
	budget := childTokens + 1
	if rootTokens <= budget || mergeTokens <= budget {
		t.Fatalf("fixture did not force an oversized merge: root=%d child=%d merge=%d budget=%d", rootTokens, childTokens, mergeTokens, budget)
	}

	compactor := &recordingCheckpointCompactor{}
	recursive, err := NewRecursiveCompactor(compactor, Config{
		WindowTokens: budget + 128,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = recursive.CompactWithResult(context.Background(), CompactionRequest{SourceGroups: []TurnGroup{raw}, Previous: &parent}, nil)
	if !errors.Is(err, ErrCompactionRecursionLimit) {
		t.Fatalf("CompactWithResult error = %v, want ErrCompactionRecursionLimit", err)
	}
	if len(compactor.calls) != 1 {
		t.Fatalf("oversized synthetic merge made %d provider calls, want only the child", len(compactor.calls))
	}
}

func TestRecursiveMergeAllowsVerifiedInteriorDirectEventReferences(t *testing.T) {
	groups := make([]TurnGroup, 0, MaxCheckpointEvidenceRefs+8)
	for i := 0; i < MaxCheckpointEvidenceRefs+8; i++ {
		groups = append(groups, TurnGroup{
			ID:             fmt.Sprintf("merge-group-%02d", i),
			SourceEventIDs: []string{fmt.Sprintf("merge-event-%02d", i)},
			Messages:       []*schema.Message{schema.UserMessage(strings.Repeat("source ", 1_000))},
		})
	}
	compactor := checkpointCompactorFunc(func(_ context.Context, request CompactionRequest, _ CompactionUsageObserver) (Checkpoint, error) {
		checkpoint, err := DeterministicCheckpoint(request)
		if err != nil {
			return Checkpoint{}, err
		}
		if len(request.SourceGroups) > 0 && strings.HasPrefix(request.SourceGroups[0].ID, "checkpoint-merge-") && request.sourceScope != nil && len(request.sourceScope.EventIDs) == len(groups) {
			// event-17 is outside the root's head/tail 32 anchors, but it is a
			// direct event preserved in an intermediate checkpoint source ref.
			checkpoint.Constraints[0].SourceRefs[0].ID = "merge-event-17"
		}
		return checkpoint, nil
	})
	recursive, err := NewRecursiveCompactor(compactor, Config{
		WindowTokens: 24_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := recursive.CompactWithResult(context.Background(), CompactionRequest{SourceGroups: groups}, nil)
	if err != nil {
		t.Fatalf("CompactWithResult: %v", err)
	}
	if len(result.Checkpoint.Provenance.DirectSource.EventIDs) != MaxCheckpointEvidenceRefs {
		t.Fatalf("model-visible anchor count = %d", len(result.Checkpoint.Provenance.DirectSource.EventIDs))
	}
	if result.Checkpoint.Constraints[0].SourceRefs[0].ID != "merge-event-17" {
		t.Fatalf("interior event reference was not preserved: %#v", result.Checkpoint.Constraints[0])
	}
	if err := result.Checkpoint.Validate(); err != nil {
		t.Fatalf("verified recursive checkpoint did not retain its cold source scope: %v", err)
	}
	payload, err := json.Marshal(result.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	directIDs := make([]string, 0, len(groups))
	for _, group := range groups {
		directIDs = append(directIDs, group.SourceEventIDs...)
	}
	if _, err := ParseCheckpointJSONForSource(payload, result.Checkpoint.Provenance, directIDs); err != nil {
		t.Fatalf("cold-manifest parser rejected recursive checkpoint: %v", err)
	}
}

func TestRecursiveMergeRejectsUnexposedInteriorEventReference(t *testing.T) {
	makeGroup := func(id, eventPrefix string) TurnGroup {
		eventIDs := make([]string, 0, MaxCheckpointEvidenceRefs+8)
		for i := 0; i < MaxCheckpointEvidenceRefs+8; i++ {
			eventIDs = append(eventIDs, fmt.Sprintf("%s-%02d", eventPrefix, i))
		}
		return TurnGroup{
			ID:             id,
			SourceEventIDs: eventIDs,
			Messages:       []*schema.Message{schema.UserMessage(strings.Repeat("source ", 9_000))},
		}
	}
	const hiddenEventID = "first-event-20"
	finalMergeCalled := false
	compactor := checkpointCompactorFunc(func(_ context.Context, request CompactionRequest, _ CompactionUsageObserver) (Checkpoint, error) {
		checkpoint, err := DeterministicCheckpoint(request)
		if err != nil {
			return Checkpoint{}, err
		}
		if hasDerivedCheckpointGroups(request.SourceGroups) {
			finalMergeCalled = true
			// This event is in the root cold manifest but is absent from the
			// child checkpoint anchors and every child source_ref.
			checkpoint.Constraints[0].SourceRefs[0].ID = hiddenEventID
		}
		return checkpoint, nil
	})
	recursive, err := NewRecursiveCompactor(compactor, Config{
		WindowTokens: 12_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = recursive.CompactWithResult(context.Background(), CompactionRequest{SourceGroups: []TurnGroup{
		makeGroup("first-group", "first-event"),
		makeGroup("second-group", "second-event"),
	}}, nil)
	if !finalMergeCalled {
		t.Fatal("test fixture did not reach a recursive final merge")
	}
	if err == nil || !strings.Contains(err.Error(), "unknown direct source event") {
		t.Fatalf("CompactWithResult error = %v, want unexposed event rejection", err)
	}
}

func TestRecursiveCompactorDefersLowGainUntilFinalMerge(t *testing.T) {
	compactor := checkpointCompactorFunc(func(_ context.Context, request CompactionRequest, _ CompactionUsageObserver) (Checkpoint, error) {
		checkpoint, err := DeterministicCheckpoint(request)
		if err != nil {
			return Checkpoint{}, err
		}
		if !hasDerivedCheckpointGroups(request.SourceGroups) {
			// Leaf summaries are intentionally too large for the final quality
			// threshold, but the merged root result is much smaller.
			checkpoint.TaskGoal = strings.Repeat("intermediate detail ", 200)
		}
		return checkpoint, nil
	})
	recursive, err := NewRecursiveCompactor(compactor, Config{
		WindowTokens: 6_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := recursive.CompactWithResult(context.Background(), CompactionRequest{SourceGroups: []TurnGroup{
		{ID: "one", SourceEventIDs: []string{"event-one"}, Messages: []*schema.Message{schema.UserMessage(strings.Repeat("source ", 3_000))}},
		{ID: "two", SourceEventIDs: []string{"event-two"}, Messages: []*schema.Message{schema.UserMessage(strings.Repeat("source ", 3_000))}},
	}}, nil)
	if err != nil {
		t.Fatalf("CompactWithResult: %v", err)
	}
	if result.GainPercent < 75 {
		t.Fatalf("final gain = %d%%, want at least 75%%", result.GainPercent)
	}
}

func TestRecursiveCompactorCountsPreviousCheckpointInFinalGain(t *testing.T) {
	parent, err := DeterministicCheckpoint(compactionTestRequest())
	if err != nil {
		t.Fatal(err)
	}
	parent.ID = "existing-parent"
	parent.StorageHash = strings.Repeat("a", 64)
	parent.TaskGoal = strings.Repeat("retained parent context ", 4_000)
	if err := parent.Validate(); err != nil {
		t.Fatalf("validate parent: %v", err)
	}
	recursive, err := NewRecursiveCompactor(&recordingCheckpointCompactor{}, Config{
		WindowTokens: 64_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := recursive.CompactWithResult(context.Background(), CompactionRequest{
		SourceGroups: []TurnGroup{{
			ID:             "new-group",
			SourceEventIDs: []string{"new-event"},
			Messages:       []*schema.Message{schema.UserMessage("new source")},
		}},
		Previous: &parent,
	}, nil)
	if err != nil {
		t.Fatalf("CompactWithResult: %v", err)
	}
	if result.GainPercent < 15 {
		t.Fatalf("gain = %d%%, want parent-inclusive gain", result.GainPercent)
	}
}

func TestRecursiveCompactorRejectsSecondMergeOfDerivedCheckpoints(t *testing.T) {
	compactor := &recordingCheckpointCompactor{}
	recursive, err := NewRecursiveCompactor(compactor, Config{
		WindowTokens: 1_500,
	})
	if err != nil {
		t.Fatal(err)
	}
	groups := []TurnGroup{
		{ID: "derived-1", SourceEventIDs: []string{"event-1"}, Messages: []*schema.Message{schema.UserMessage(strings.Repeat("one ", 1_000))}, derivedCheckpoint: true},
		{ID: "derived-2", SourceEventIDs: []string{"event-2"}, Messages: []*schema.Message{schema.UserMessage(strings.Repeat("two ", 1_000))}, derivedCheckpoint: true},
	}
	_, err = recursive.CompactWithResult(context.Background(), CompactionRequest{SourceGroups: groups}, nil)
	if !errors.Is(err, ErrCompactionRecursionLimit) {
		t.Fatalf("CompactWithResult error = %v, want ErrCompactionRecursionLimit", err)
	}
	if len(compactor.calls) != 0 {
		t.Fatalf("derived merge overflow made %d provider calls, want none", len(compactor.calls))
	}
}

func TestCheckpointEstimateIncludesInstalledMessageFraming(t *testing.T) {
	checkpoint, err := DeterministicCheckpoint(CompactionRequest{SourceGroups: []TurnGroup{{
		ID:             "group-1",
		SourceEventIDs: []string{"event-1"},
		Messages:       []*schema.Message{schema.UserMessage("source")},
	}}})
	if err != nil {
		t.Fatalf("DeterministicCheckpoint: %v", err)
	}
	want := usage.EstimateMessages([]*schema.Message{schema.SystemMessage(checkpoint.PromptText())})
	if got := checkpoint.EstimatedTokens(); got != want {
		t.Fatalf("checkpoint tokens = %d, want installed message estimate %d", got, want)
	}
}

func TestCheckpointV2RejectsForeignClaimsAndV1Payload(t *testing.T) {
	request := compactionTestRequest()
	checkpoint, err := DeterministicCheckpoint(request)
	if err != nil {
		t.Fatalf("DeterministicCheckpoint: %v", err)
	}
	checkpoint.Constraints[0].SourceRefs[0].ID = "foreign-event"
	if err := checkpoint.Validate(); err == nil || !strings.Contains(err.Error(), "unknown direct source event") {
		t.Fatalf("Validate foreign event error = %v", err)
	}

	if _, err := ParseCheckpointJSON([]byte(`{"schema_version":1,"source_event_ids":["event-1"],"source_hash":"legacy"}`)); err == nil {
		t.Fatal("v1 checkpoint payload was accepted")
	}
	valid, err := DeterministicCheckpoint(request)
	if err != nil {
		t.Fatalf("DeterministicCheckpoint valid payload: %v", err)
	}
	payloadBytes, err := json.Marshal(valid)
	if err != nil {
		t.Fatalf("Marshal valid checkpoint: %v", err)
	}
	payload := strings.TrimSuffix(string(payloadBytes), "}") + `,"source_event_ids":["event-1"]}`
	if _, err := ParseCheckpointJSON([]byte(payload)); err == nil {
		t.Fatal("v2 payload with v1 source_event_ids field was accepted")
	}
}

func TestValidateLegacyV1CheckpointAllowsInheritedClaimReferences(t *testing.T) {
	const payload = `{"schema_version":1,"source_range":{"from":"event-current","to":"event-current","content_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","event_ids":["event-current"]},"source_event_ids":["event-current"],"source_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","task_goal":"resume","constraints":[{"text":"inherited constraint","source_event_ids":["event-ancestor"],"confidence":"observed"}],"confirmed_facts":[{"text":"fact","source_event_ids":["event-current"],"confidence":"observed"}],"decisions":[{"decision":"decision","reason":"reason","source_event_ids":["event-current"],"confidence":"observed"}],"attempts_and_results":[{"text":"attempt","result":"result","source_event_ids":["event-current"],"confidence":"observed"}],"files_or_artifacts":[{"ref":"event://event-current","description":"source","source_event_ids":["event-current"],"confidence":"observed"}],"open_questions":[{"text":"question","source_event_ids":["event-current"],"confidence":"unknown"}],"next_actions":[{"text":"next","source_event_ids":["event-current"],"confidence":"inferred"}]}`
	if err := ValidateLegacyV1CheckpointJSON([]byte(payload)); err != nil {
		t.Fatalf("ValidateLegacyV1CheckpointJSON: %v", err)
	}
}

func TestCompactionRequestRequiresDurablyValidParent(t *testing.T) {
	parent, err := DeterministicCheckpoint(compactionTestRequest())
	if err != nil {
		t.Fatalf("DeterministicCheckpoint parent: %v", err)
	}
	parent.ID = "parent-checkpoint"
	parent.StorageHash = strings.Repeat("a", 64)
	childRequest := CompactionRequest{
		SourceGroups: []TurnGroup{{
			ID:             "group-2",
			SourceEventIDs: []string{"event-2"},
			Messages:       []*schema.Message{schema.UserMessage("child source")},
		}},
		Previous: &parent,
	}
	child, err := DeterministicCheckpoint(childRequest)
	if err != nil {
		t.Fatalf("DeterministicCheckpoint child: %v", err)
	}
	if ref := child.ParentRef(); ref == nil || ref.ID != parent.ID || ref.Hash != parent.StorageHash || ref.LineageHash != parent.Provenance.LineageHash {
		t.Fatalf("child parent binding = %#v", ref)
	}

	parent.Provenance.LineageHash = strings.Repeat("b", 64)
	childRequest.Previous = &parent
	if _, err := childRequest.sourceIdentity(); err == nil || !strings.Contains(err.Error(), "validate compaction previous checkpoint") {
		t.Fatalf("invalid parent source identity error = %v", err)
	}
}
