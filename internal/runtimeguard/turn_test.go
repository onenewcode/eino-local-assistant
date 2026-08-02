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
	if got.MaxToolCalls != DefaultMaxToolCalls {
		t.Errorf("MaxToolCalls = %d, want %d", got.MaxToolCalls, DefaultMaxToolCalls)
	}
	if got.Timeout != DefaultTurnTimeout {
		t.Errorf("Timeout = %s, want %s", got.Timeout, DefaultTurnTimeout)
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
