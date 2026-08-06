package runtimeguard

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestDefaultTurnOptions(t *testing.T) {
	got := DefaultTurnOptions()
	if got.MaxModelSteps != DefaultMaxModelSteps {
		t.Errorf("MaxModelSteps = %d, want %d", got.MaxModelSteps, DefaultMaxModelSteps)
	}
	if got.MaxToolCalls != DefaultMaxToolCalls {
		t.Errorf("MaxToolCalls = %d, want %d", got.MaxToolCalls, DefaultMaxToolCalls)
	}
	if got.MaxConsecutiveEquivalentToolCalls != DefaultMaxConsecutiveEquivalentToolCalls {
		t.Errorf("MaxConsecutiveEquivalentToolCalls = %d, want %d", got.MaxConsecutiveEquivalentToolCalls, DefaultMaxConsecutiveEquivalentToolCalls)
	}
	if got.Timeout != DefaultTurnTimeout {
		t.Errorf("Timeout = %s, want %s", got.Timeout, DefaultTurnTimeout)
	}
}

func TestAcquireModelStepTracksLimit(t *testing.T) {
	ctx, cancel, err := WithTurnContext(context.Background(), TurnOptions{
		MaxModelSteps: 2,
		MaxToolCalls:  1,
	})
	if err != nil {
		t.Fatalf("WithTurnContext() error = %v", err)
	}
	t.Cleanup(cancel)

	for step := 0; step < 2; step++ {
		if err := AcquireModelStep(ctx); err != nil {
			t.Fatalf("AcquireModelStep() call %d error = %v", step+1, err)
		}
	}
	if err := AcquireModelStep(ctx); !errors.Is(err, ErrModelStepBudgetExceeded) {
		t.Fatalf("AcquireModelStep() error = %v, want ErrModelStepBudgetExceeded", err)
	}

	budget, ok := FromContext(ctx)
	if !ok {
		t.Fatal("FromContext() did not return a budget")
	}
	if got, want := budget.ModelSteps(), 2; got != want {
		t.Errorf("ModelSteps() = %d, want %d", got, want)
	}
	if got, want := budget.RemainingModelSteps(), 0; got != want {
		t.Errorf("RemainingModelSteps() = %d, want %d", got, want)
	}
}

func TestAcquireFinalResponseAllowsExactlyOneForcedRequest(t *testing.T) {
	ctx, cancel, err := WithTurnContext(context.Background(), TurnOptions{})
	if err != nil {
		t.Fatalf("WithTurnContext() error = %v", err)
	}
	t.Cleanup(cancel)

	if err := AcquireFinalResponse(ctx); err != nil {
		t.Fatalf("first AcquireFinalResponse() error = %v", err)
	}
	if err := AcquireFinalResponse(ctx); !errors.Is(err, ErrFinalResponseBudgetExceeded) {
		t.Fatalf("second AcquireFinalResponse() error = %v, want ErrFinalResponseBudgetExceeded", err)
	}
}

func TestWithTurnContextNormalizesPartialOptions(t *testing.T) {
	now := time.Now()
	ctx, cancel, err := withTurnContextAt(context.Background(), TurnOptions{MaxToolCalls: 3}, func() time.Time { return now })
	if err != nil {
		t.Fatalf("withTurnContextAt() error = %v", err)
	}
	t.Cleanup(cancel)

	budget, ok := FromContext(ctx)
	if !ok {
		t.Fatal("FromContext() did not return a budget")
	}
	if got, want := budget.MaxToolCalls(), 3; got != want {
		t.Errorf("MaxToolCalls() = %d, want %d", got, want)
	}
	if got, want := budget.Deadline(), now.Add(DefaultTurnTimeout); !got.Equal(want) {
		t.Errorf("Deadline() = %s, want %s", got, want)
	}
}

func TestWithTurnContextUsesEarlierParentDeadline(t *testing.T) {
	now := time.Now()
	parentDeadline := now.Add(time.Minute)
	parent, cancelParent := context.WithDeadline(context.Background(), parentDeadline)
	t.Cleanup(cancelParent)

	ctx, cancel, err := withTurnContextAt(parent, TurnOptions{Timeout: 2 * time.Minute}, func() time.Time { return now })
	if err != nil {
		t.Fatalf("withTurnContextAt() error = %v", err)
	}
	t.Cleanup(cancel)

	budget, ok := FromContext(ctx)
	if !ok {
		t.Fatal("FromContext() did not return a budget")
	}
	if got := budget.Deadline(); !got.Equal(parentDeadline) {
		t.Errorf("Deadline() = %s, want parent deadline %s", got, parentDeadline)
	}
}

func TestWithTurnContextRejectsInvalidOptions(t *testing.T) {
	tests := []struct {
		name   string
		parent context.Context
		opts   TurnOptions
	}{
		{
			name: "nil parent",
			opts: TurnOptions{},
		},
		{
			name:   "negative max tool calls",
			parent: context.Background(),
			opts:   TurnOptions{MaxToolCalls: -1},
		},
		{
			name:   "negative timeout",
			parent: context.Background(),
			opts:   TurnOptions{Timeout: -time.Second},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel, err := WithTurnContext(tt.parent, tt.opts)
			if err == nil {
				if cancel != nil {
					cancel()
				}
				t.Fatal("WithTurnContext() error = nil, want error")
			}
			if ctx != nil || cancel != nil {
				t.Fatalf("WithTurnContext() = (%v, %v, %v), want nil context and cancel", ctx, cancel, err)
			}
		})
	}
}

func TestAcquireToolCallTracksLimitAcrossDerivedContexts(t *testing.T) {
	ctx, cancel, err := WithTurnContext(context.Background(), TurnOptions{MaxToolCalls: 2})
	if err != nil {
		t.Fatalf("WithTurnContext() error = %v", err)
	}
	t.Cleanup(cancel)

	child := context.WithValue(ctx, struct{}{}, "child")
	for call := 0; call < 2; call++ {
		if err := AcquireToolCall(child); err != nil {
			t.Fatalf("AcquireToolCall() call %d error = %v", call+1, err)
		}
	}
	if err := AcquireToolCall(ctx); !errors.Is(err, ErrToolCallBudgetExceeded) {
		t.Fatalf("AcquireToolCall() error = %v, want ErrToolCallBudgetExceeded", err)
	}

	budget, ok := FromContext(ctx)
	if !ok {
		t.Fatal("FromContext() did not return a budget")
	}
	if got, want := budget.ToolCalls(), 2; got != want {
		t.Errorf("ToolCalls() = %d, want %d", got, want)
	}
	if got, want := budget.RemainingToolCalls(), 0; got != want {
		t.Errorf("RemainingToolCalls() = %d, want %d", got, want)
	}
}

func TestAcquireToolCallIsAtomic(t *testing.T) {
	const (
		limit    = 3
		attempts = 24
	)
	ctx, cancel, err := WithTurnContext(context.Background(), TurnOptions{MaxToolCalls: limit})
	if err != nil {
		t.Fatalf("WithTurnContext() error = %v", err)
	}
	t.Cleanup(cancel)

	start := make(chan struct{})
	results := make(chan error, attempts)
	var workers sync.WaitGroup
	for worker := 0; worker < attempts; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			results <- AcquireToolCall(ctx)
		}()
	}
	close(start)
	workers.Wait()
	close(results)

	var succeeded, denied int
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrToolCallBudgetExceeded):
			denied++
		default:
			t.Fatalf("AcquireToolCall() error = %v, want budget denial", err)
		}
	}
	if got, want := succeeded, limit; got != want {
		t.Errorf("successful tool calls = %d, want %d", got, want)
	}
	if got, want := denied, attempts-limit; got != want {
		t.Errorf("denied tool calls = %d, want %d", got, want)
	}
}

func TestAcquireToolCallReportsTurnDeadline(t *testing.T) {
	past := time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)
	ctx, cancel, err := withTurnContextAt(context.Background(), TurnOptions{MaxToolCalls: 1, Timeout: time.Second}, func() time.Time { return past })
	if err != nil {
		t.Fatalf("withTurnContextAt() error = %v", err)
	}
	t.Cleanup(cancel)

	select {
	case <-ctx.Done():
	default:
		t.Fatal("turn context did not expire for a past deadline")
	}
	if cause := context.Cause(ctx); !errors.Is(cause, ErrTurnDeadlineExceeded) {
		t.Fatalf("context.Cause() = %v, want ErrTurnDeadlineExceeded", cause)
	}
	if err := AcquireToolCall(ctx); !errors.Is(err, ErrTurnDeadlineExceeded) {
		t.Fatalf("AcquireToolCall() error = %v, want ErrTurnDeadlineExceeded", err)
	}

	budget, ok := FromContext(ctx)
	if !ok || !budget.DeadlineExceeded() {
		t.Fatalf("DeadlineExceeded() = %v, want true", ok && budget.DeadlineExceeded())
	}
	if !IsTurnDeadlineExceeded(ctx) {
		t.Fatal("IsTurnDeadlineExceeded() = false, want true")
	}
}

func TestAcquireToolCallPreservesParentCancellation(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	ctx, cancel, err := WithTurnContext(parent, TurnOptions{MaxToolCalls: 1})
	if err != nil {
		t.Fatalf("WithTurnContext() error = %v", err)
	}
	t.Cleanup(cancel)
	cancelParent()
	<-ctx.Done()

	if err := AcquireToolCall(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("AcquireToolCall() error = %v, want context.Canceled", err)
	}
	budget, ok := FromContext(ctx)
	if !ok {
		t.Fatal("FromContext() did not return a budget")
	}
	if budget.DeadlineExceeded() {
		t.Fatal("DeadlineExceeded() = true after parent cancellation")
	}
	if IsTurnDeadlineExceeded(ctx) {
		t.Fatal("IsTurnDeadlineExceeded() = true after parent cancellation")
	}
}

func TestAcquireToolCallWithoutTurnBudgetIsNoOp(t *testing.T) {
	if err := AcquireToolCall(context.Background()); err != nil {
		t.Fatalf("AcquireToolCall() error = %v, want nil", err)
	}
}

func TestAdmitToolBatchReservesWholeBatchOrNone(t *testing.T) {
	ctx, cancel, err := WithTurnContext(context.Background(), TurnOptions{
		MaxToolCalls:                      3,
		MaxConsecutiveEquivalentToolCalls: 3,
	})
	if err != nil {
		t.Fatalf("WithTurnContext() error = %v", err)
	}
	t.Cleanup(cancel)
	if err := AcquireToolCall(ctx); err != nil {
		t.Fatalf("first prior tool call: %v", err)
	}
	if err := AcquireToolCall(ctx); err != nil {
		t.Fatalf("second prior tool call: %v", err)
	}

	calls := []ToolCall{
		{ID: "source-1", Name: "shell", Arguments: `{"command":"pwd"}`},
		{ID: "source-2", Name: "shell", Arguments: `{"command":"git status"}`},
	}
	admissions, err := AdmitToolBatch(ctx, calls)
	if err != nil {
		t.Fatalf("AdmitToolBatch() error = %v", err)
	}
	for index, admission := range admissions {
		if admission.Allowed || admission.Reason != DenialReason(ErrToolCallBudgetExceeded) {
			t.Fatalf("admission[%d] = %#v, want whole-batch budget denial", index, admission)
		}
		if err := StartToolCall(ctx, calls[index]); !errors.Is(err, ErrToolCallBudgetExceeded) {
			t.Fatalf("StartToolCall(%q) error = %v, want budget denial", calls[index].ID, err)
		}
	}

	budget, ok := FromContext(ctx)
	if !ok {
		t.Fatal("FromContext() did not return a budget")
	}
	if got, want := budget.ToolCalls(), 2; got != want {
		t.Errorf("ToolCalls() = %d, want %d because denied batch must not consume slots", got, want)
	}
}

func TestAdmitToolBatchCanonicalizesArgumentsInSourceOrder(t *testing.T) {
	ctx, cancel, err := WithTurnContext(context.Background(), TurnOptions{
		MaxToolCalls:                      8,
		MaxConsecutiveEquivalentToolCalls: 2,
	})
	if err != nil {
		t.Fatalf("WithTurnContext() error = %v", err)
	}
	t.Cleanup(cancel)

	calls := []ToolCall{
		{ID: "first", Name: "shell", Arguments: `{"command":"git status","working_dir":"."}`},
		{ID: "second", Name: "shell", Arguments: ` { "working_dir" : ".", "command" : "git status" } `},
		{ID: "third", Name: "shell", Arguments: `{"command":"git status","working_dir":"."}`},
		{ID: "other-tool", Name: "read_artifact", Arguments: `{"artifact_id":"one"}`},
	}
	admissions, err := AdmitToolBatch(ctx, calls)
	if err != nil {
		t.Fatalf("AdmitToolBatch() error = %v", err)
	}
	want := []struct {
		allowed bool
		reason  string
	}{
		{allowed: true},
		{allowed: true},
		{allowed: false, reason: DenialReason(ErrEquivalentToolCallLimitExceeded)},
		{allowed: true},
	}
	for index, expected := range want {
		if admissions[index].Allowed != expected.allowed || admissions[index].Reason != expected.reason {
			t.Fatalf("admission[%d] = %#v, want allowed=%t reason=%q", index, admissions[index], expected.allowed, expected.reason)
		}
	}

	for index, call := range calls {
		err := StartToolCall(ctx, call)
		if index == 2 {
			if !errors.Is(err, ErrEquivalentToolCallLimitExceeded) {
				t.Fatalf("StartToolCall(%q) error = %v, want equivalent-call denial", call.ID, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("StartToolCall(%q) error = %v", call.ID, err)
		}
	}

	budget, ok := FromContext(ctx)
	if !ok {
		t.Fatal("FromContext() did not return a budget")
	}
	if got, want := budget.ToolCalls(), 3; got != want {
		t.Errorf("ToolCalls() = %d, want %d", got, want)
	}
}

func TestAdmittedBatchClaimsDoNotDoubleAcquireDuringParallelExecution(t *testing.T) {
	ctx, cancel, err := WithTurnContext(context.Background(), TurnOptions{
		MaxToolCalls:                      4,
		MaxConsecutiveEquivalentToolCalls: 4,
	})
	if err != nil {
		t.Fatalf("WithTurnContext() error = %v", err)
	}
	t.Cleanup(cancel)
	calls := []ToolCall{
		{ID: "one", Name: "shell", Arguments: `{"command":"one"}`},
		{ID: "two", Name: "shell", Arguments: `{"command":"two"}`},
		{ID: "three", Name: "shell", Arguments: `{"command":"three"}`},
		{ID: "four", Name: "shell", Arguments: `{"command":"four"}`},
	}
	admissions, err := AdmitToolBatch(ctx, calls)
	if err != nil {
		t.Fatalf("AdmitToolBatch() error = %v", err)
	}
	for _, admission := range admissions {
		if !admission.Allowed {
			t.Fatalf("unexpected admission denial: %#v", admission)
		}
	}

	start := make(chan struct{})
	errs := make(chan error, len(calls))
	var workers sync.WaitGroup
	for _, call := range calls {
		call := call
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			errs <- StartToolCall(ctx, call)
		}()
	}
	close(start)
	workers.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("StartToolCall() error = %v", err)
		}
	}

	budget, ok := FromContext(ctx)
	if !ok {
		t.Fatal("FromContext() did not return a budget")
	}
	if got, want := budget.ToolCalls(), len(calls); got != want {
		t.Errorf("ToolCalls() = %d, want pre-reserved %d slots without double acquire", got, want)
	}
	if err := StartToolCall(ctx, calls[0]); !errors.Is(err, ErrToolCallAlreadyStarted) {
		t.Fatalf("second StartToolCall() error = %v, want ErrToolCallAlreadyStarted", err)
	}
}

func TestMarkStateChangedResetsEquivalentCallCountsWithoutDiscardingAdmission(t *testing.T) {
	ctx, cancel, err := WithTurnContext(context.Background(), TurnOptions{
		MaxToolCalls:                      4,
		MaxConsecutiveEquivalentToolCalls: 1,
	})
	if err != nil {
		t.Fatalf("WithTurnContext() error = %v", err)
	}
	t.Cleanup(cancel)

	first := ToolCall{ID: "first", Name: "apply_patch", Arguments: `{"operations":[{"type":"create_file","path":"one"}]}`}
	if admissions, err := AdmitToolBatch(ctx, []ToolCall{first}); err != nil || !admissions[0].Allowed {
		t.Fatalf("first admission = %#v, %v", admissions, err)
	}
	MarkStateChanged(ctx)
	if err := StartToolCall(ctx, first); err != nil {
		t.Fatalf("StartToolCall() after reset error = %v", err)
	}

	second := ToolCall{ID: "second", Name: "apply_patch", Arguments: ` { "operations" : [ { "path" : "one", "type" : "create_file" } ] } `}
	admissions, err := AdmitToolBatch(ctx, []ToolCall{second})
	if err != nil {
		t.Fatalf("second AdmitToolBatch() error = %v", err)
	}
	if !admissions[0].Allowed {
		t.Fatalf("second admission = %#v, want reset equivalent count", admissions[0])
	}
}

func TestAdmitToolBatchRejectsAmbiguousIDsWithoutMutatingBudget(t *testing.T) {
	ctx, cancel, err := WithTurnContext(context.Background(), TurnOptions{MaxToolCalls: 2})
	if err != nil {
		t.Fatalf("WithTurnContext() error = %v", err)
	}
	t.Cleanup(cancel)

	_, err = AdmitToolBatch(ctx, []ToolCall{
		{ID: "same", Name: "shell", Arguments: `{}`},
		{ID: "same", Name: "shell", Arguments: `{}`},
	})
	if !errors.Is(err, ErrToolCallAlreadyAdmitted) {
		t.Fatalf("AdmitToolBatch() error = %v, want ErrToolCallAlreadyAdmitted", err)
	}
	budget, ok := FromContext(ctx)
	if !ok {
		t.Fatal("FromContext() did not return a budget")
	}
	if got := budget.ToolCalls(); got != 0 {
		t.Errorf("ToolCalls() = %d, want no partial reservation", got)
	}
}

func TestStartToolCallRejectsEmptyIDAfterBatchAdmission(t *testing.T) {
	ctx, cancel, err := WithTurnContext(context.Background(), TurnOptions{MaxToolCalls: 2})
	if err != nil {
		t.Fatalf("WithTurnContext() error = %v", err)
	}
	t.Cleanup(cancel)
	admitted := ToolCall{ID: "admitted", Name: "shell", Arguments: `{"command":"pwd"}`}
	admissions, err := AdmitToolBatch(ctx, []ToolCall{admitted})
	if err != nil || len(admissions) != 1 || !admissions[0].Allowed {
		t.Fatalf("AdmitToolBatch() = %#v, %v, want one allowed admission", admissions, err)
	}
	if err := StartToolCall(ctx, ToolCall{Name: "shell", Arguments: `{"command":"pwd"}`}); !errors.Is(err, ErrToolCallNotAdmitted) {
		t.Fatalf("StartToolCall(empty id) error = %v, want ErrToolCallNotAdmitted", err)
	}
	if err := StartToolCall(ctx, admitted); err != nil {
		t.Fatalf("StartToolCall(admitted) error = %v", err)
	}
	budget, ok := FromContext(ctx)
	if !ok {
		t.Fatal("FromContext() did not return a budget")
	}
	if got, want := budget.ToolCalls(), 1; got != want {
		t.Fatalf("ToolCalls() = %d, want %d: empty-id call must not consume a second slot", got, want)
	}
}
