// Package runtimeguard provides context-scoped limits for a single agent turn.
package runtimeguard

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

const (
	// DefaultMaxToolCalls bounds tool invocations in one agent turn when no
	// explicit limit is supplied.
	DefaultMaxToolCalls = 16
	// DefaultTurnTimeout bounds the total wall-clock duration of one agent turn.
	DefaultTurnTimeout = 10 * time.Minute
	// TurnTimeoutReason is the stable durable/UI reason for a whole-turn
	// deadline rather than a provider-specific context error.
	TurnTimeoutReason = "runtime_guardrail: turn_timeout"
)

var (
	// ErrToolCallBudgetExceeded is returned before a tool starts when its turn
	// has consumed every allowed tool invocation. Callers can expose it as a
	// structured soft denial rather than treating it as an execution failure.
	ErrToolCallBudgetExceeded = errors.New("tool-call budget exceeded")
	// ErrTurnDeadlineExceeded is the cancellation cause installed when a turn's
	// own deadline expires. A caller's earlier cancellation remains unchanged.
	ErrTurnDeadlineExceeded = errors.New("turn deadline exceeded")
)

// TurnOptions configures limits for one agent turn. Zero values use the
// package defaults; negative values are invalid.
type TurnOptions struct {
	MaxToolCalls int
	Timeout      time.Duration
}

// DefaultTurnOptions returns the strict default limits for a turn.
func DefaultTurnOptions() TurnOptions {
	return TurnOptions{
		MaxToolCalls: DefaultMaxToolCalls,
		Timeout:      DefaultTurnTimeout,
	}
}

// TurnBudget holds the state shared by all contexts derived from one turn.
// Its methods are safe to call concurrently from parallel tool invocations.
type TurnBudget struct {
	maxToolCalls int64
	toolCalls    atomic.Int64
	deadline     time.Time
	turnContext  context.Context
}

type turnBudgetContextKey struct{}

// WithTurnContext derives a context carrying a per-turn tool-call budget and
// deadline. Call the returned cancel function when the turn finishes to
// release the timer promptly.
func WithTurnContext(parent context.Context, opts TurnOptions) (context.Context, context.CancelFunc, error) {
	return withTurnContextAt(parent, opts, time.Now)
}

func withTurnContextAt(parent context.Context, opts TurnOptions, now func() time.Time) (context.Context, context.CancelFunc, error) {
	if parent == nil {
		return nil, nil, errors.New("parent context is required")
	}
	if now == nil {
		return nil, nil, errors.New("clock is required")
	}

	normalized, err := normalizeTurnOptions(opts)
	if err != nil {
		return nil, nil, err
	}

	deadline := now().Add(normalized.Timeout)
	turnContext, cancel := context.WithDeadlineCause(parent, deadline, ErrTurnDeadlineExceeded)
	if effectiveDeadline, ok := turnContext.Deadline(); ok {
		deadline = effectiveDeadline
	}
	budget := &TurnBudget{
		maxToolCalls: int64(normalized.MaxToolCalls),
		deadline:     deadline,
		turnContext:  turnContext,
	}

	return context.WithValue(turnContext, turnBudgetContextKey{}, budget), cancel, nil
}

func normalizeTurnOptions(opts TurnOptions) (TurnOptions, error) {
	defaults := DefaultTurnOptions()
	if opts.MaxToolCalls < 0 {
		return TurnOptions{}, errors.New("max tool calls must be >= 0")
	}
	if opts.MaxToolCalls == 0 {
		opts.MaxToolCalls = defaults.MaxToolCalls
	}
	if opts.Timeout < 0 {
		return TurnOptions{}, errors.New("turn timeout must be >= 0")
	}
	if opts.Timeout == 0 {
		opts.Timeout = defaults.Timeout
	}
	return opts, nil
}

// FromContext returns the turn budget attached to ctx, if any.
func FromContext(ctx context.Context) (*TurnBudget, bool) {
	if ctx == nil {
		return nil, false
	}
	budget, ok := ctx.Value(turnBudgetContextKey{}).(*TurnBudget)
	return budget, ok
}

// IsTurnDeadlineExceeded reports whether ctx was ended by this package's own
// per-turn deadline, rather than an unrelated parent deadline.
func IsTurnDeadlineExceeded(ctx context.Context) bool {
	budget, ok := FromContext(ctx)
	return ok && budget.DeadlineExceeded()
}

// AcquireToolCall consumes one tool-call slot from ctx's turn budget. It is a
// no-op for contexts without a budget so existing non-agent callers remain
// usable while they are migrated to a turn context.
func AcquireToolCall(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	budget, ok := FromContext(ctx)
	if !ok {
		return nil
	}
	if err := budget.contextError(ctx); err != nil {
		return err
	}
	if !budget.acquire() {
		return ErrToolCallBudgetExceeded
	}
	return nil
}

func (b *TurnBudget) contextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		if b.DeadlineExceeded() {
			return ErrTurnDeadlineExceeded
		}
		return err
	}
	return nil
}

func (b *TurnBudget) acquire() bool {
	for {
		used := b.toolCalls.Load()
		if used >= b.maxToolCalls {
			return false
		}
		if b.toolCalls.CompareAndSwap(used, used+1) {
			return true
		}
	}
}

// MaxToolCalls returns the configured tool-call limit.
func (b *TurnBudget) MaxToolCalls() int {
	if b == nil {
		return 0
	}
	return int(b.maxToolCalls)
}

// ToolCalls returns the number of successfully acquired tool-call slots.
func (b *TurnBudget) ToolCalls() int {
	if b == nil {
		return 0
	}
	return int(b.toolCalls.Load())
}

// RemainingToolCalls returns the number of tool-call slots that remain.
func (b *TurnBudget) RemainingToolCalls() int {
	if b == nil {
		return 0
	}
	remaining := b.maxToolCalls - b.toolCalls.Load()
	if remaining < 0 {
		return 0
	}
	return int(remaining)
}

// Deadline returns the effective turn deadline after respecting any earlier
// deadline from the parent context.
func (b *TurnBudget) Deadline() time.Time {
	if b == nil {
		return time.Time{}
	}
	return b.deadline
}

// DeadlineExceeded reports whether this turn's own deadline, rather than a
// parent cancellation, ended the context.
func (b *TurnBudget) DeadlineExceeded() bool {
	if b == nil || b.turnContext == nil {
		return false
	}
	return errors.Is(context.Cause(b.turnContext), ErrTurnDeadlineExceeded)
}
