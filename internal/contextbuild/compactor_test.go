package contextbuild

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"eino-local-assistant/internal/usage"

	"github.com/cloudwego/eino/schema"
)

type recordingCheckpointCompactor struct {
	calls []CompactionRequest
	err   error
}

func (c *recordingCheckpointCompactor) Compact(_ context.Context, request CompactionRequest) (Checkpoint, error) {
	c.calls = append(c.calls, request)
	if c.err != nil {
		return Checkpoint{}, c.err
	}
	return DeterministicCheckpoint(request)
}

func TestRecursiveCompactorNormalizesEveryChunkIdentity(t *testing.T) {
	compactor := &recordingCheckpointCompactor{}
	cfg := Config{
		ModelContextTokens:        7_000,
		OutputReserveTokens:       1_000,
		SummaryMaxTokens:          2_048,
		LowGainThresholdPercent:   1,
		MaxLowGainAttempts:        3,
		AutoCompactTriggerPercent: 75,
		PostCompactTargetPercent:  45,
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
	result, err := recursive.CompactWithResult(context.Background(), CompactionRequest{
		TaskGoal:     "retain decisions",
		SourceGroups: groups,
	})
	if err != nil {
		t.Fatalf("CompactWithResult: %v", err)
	}
	if result.UsedFallback {
		t.Fatalf("valid chunk checkpoints unexpectedly fell back: %+v", result)
	}
	if len(compactor.calls) < 3 {
		t.Fatalf("compactor calls = %d, want multiple chunks plus merge", len(compactor.calls))
	}
	for i, call := range compactor.calls {
		if len(call.SourceEventIDs) == 0 || call.SourceHash == "" {
			t.Fatalf("call %d has no normalized source identity: %+v", i, call)
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
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CompactWithResult error = %v, want context.Canceled", err)
	}
}

func TestRecursiveCompactorRejectsFallbackOverSummaryBudget(t *testing.T) {
	compactor := &recordingCheckpointCompactor{err: errors.New("provider unavailable")}
	recursive, err := NewRecursiveCompactor(compactor, Config{
		ModelContextTokens:        2_000,
		OutputReserveTokens:       1_000,
		SummaryMaxTokens:          1,
		AutoCompactTriggerPercent: 75,
		PostCompactTargetPercent:  45,
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
	})
	if !errors.Is(err, ErrCheckpointTooLarge) {
		t.Fatalf("CompactWithResult error = %v, want ErrCheckpointTooLarge", err)
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
	ids, _, err := (CompactionRequest{SourceGroups: groups}).sourceIdentity()
	if err != nil {
		t.Fatalf("sourceIdentity: %v", err)
	}
	if len(ids) != MaxCheckpointEvidenceRefs {
		t.Fatalf("evidence refs = %d, want %d", len(ids), MaxCheckpointEvidenceRefs)
	}
	if ids[0] != "event-00" || ids[len(ids)-1] != fmt.Sprintf("event-%02d", MaxCheckpointEvidenceRefs+7) {
		t.Fatalf("source anchors did not preserve range ends: %#v", ids)
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
