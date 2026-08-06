// Package runtimeguard provides context-scoped limits for a single agent turn.
package runtimeguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultMaxToolCalls bounds tool invocations in one agent turn when no
	// explicit limit is supplied.
	DefaultMaxToolCalls = 16
	// DefaultMaxModelSteps bounds tool-enabled model decisions in one turn.
	// The agent layer reserves a final tools-disabled response separately.
	DefaultMaxModelSteps = 8
	// DefaultMaxConsecutiveEquivalentToolCalls bounds calls with the same
	// canonical tool name and JSON arguments until the caller records a state
	// change. It intentionally cannot be disabled with a zero value.
	DefaultMaxConsecutiveEquivalentToolCalls = 3
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
	// ErrModelStepBudgetExceeded is returned before a tool-enabled model
	// decision when its per-turn budget has been exhausted.
	ErrModelStepBudgetExceeded = errors.New("model-step budget exceeded")
	// ErrFinalResponseBudgetExceeded is returned before a forced tools-disabled
	// final response when that durable turn has already spent its one reserved
	// final-response request. It prevents a task continuation from repeatedly
	// retrying the same terminal provider call after tool planning is exhausted.
	ErrFinalResponseBudgetExceeded = errors.New("final-response budget exceeded")
	// ErrEquivalentToolCallLimitExceeded is returned before an equivalent tool
	// call starts when it would exceed the configured repeated-call limit.
	ErrEquivalentToolCallLimitExceeded = errors.New("equivalent tool-call limit exceeded")
	// ErrToolCallNotAdmitted prevents a tool call from bypassing an already
	// admitted batch. It normally indicates a caller failed to admit a new
	// model response before allowing ToolsNode to dispatch it.
	ErrToolCallNotAdmitted = errors.New("tool call not admitted")
	// ErrToolCallAdmissionMismatch prevents a call ID admitted for one tool
	// and arguments from being reused for another invocation.
	ErrToolCallAdmissionMismatch = errors.New("tool call does not match its admission")
	// ErrToolCallAlreadyStarted prevents a framework retry from executing the
	// same reserved call ID twice in one turn.
	ErrToolCallAlreadyStarted = errors.New("tool call already started")
	// ErrToolCallAlreadyAdmitted is returned when a caller tries to admit a
	// reused tool-call ID. Re-admitting would otherwise reserve slots twice.
	ErrToolCallAlreadyAdmitted = errors.New("tool call already admitted")
	// ErrToolCallIDRequired is returned because concurrent identical calls
	// cannot be associated with deterministic source-order admissions without
	// their protocol call IDs.
	ErrToolCallIDRequired = errors.New("tool call id is required for batch admission")
	// ErrTurnDeadlineExceeded is the cancellation cause installed when a turn's
	// own deadline expires. A caller's earlier cancellation remains unchanged.
	ErrTurnDeadlineExceeded = errors.New("turn deadline exceeded")
)

// TurnOptions configures limits for one agent turn. Zero values use the
// package defaults; negative values are invalid.
type TurnOptions struct {
	MaxModelSteps                     int
	MaxToolCalls                      int
	MaxConsecutiveEquivalentToolCalls int
	Timeout                           time.Duration
}

// DefaultTurnOptions returns the strict default limits for a turn.
func DefaultTurnOptions() TurnOptions {
	return TurnOptions{
		MaxModelSteps:                     DefaultMaxModelSteps,
		MaxToolCalls:                      DefaultMaxToolCalls,
		MaxConsecutiveEquivalentToolCalls: DefaultMaxConsecutiveEquivalentToolCalls,
		Timeout:                           DefaultTurnTimeout,
	}
}

// TurnBudget holds the state shared by all contexts derived from one turn.
// Its methods are safe to call concurrently from parallel tool invocations.
type TurnBudget struct {
	mu                                sync.Mutex
	maxModelSteps                     int
	modelSteps                        int
	finalResponseAcquired             bool
	maxToolCalls                      int
	toolCalls                         int
	maxConsecutiveEquivalentToolCalls int
	equivalentToolCalls               map[string]int
	admittedToolCalls                 map[string]*admittedToolCall
	hasBatchAdmissions                bool
	deadline                          time.Time
	turnContext                       context.Context
}

type admittedToolCall struct {
	key     string
	allowed bool
	err     error
	started bool
}

// ToolCall is a model-requested tool invocation. ID must be the provider or
// framework call ID when passed to AdmitToolBatch; it links a deterministic
// source-order decision to a later concurrent tool execution.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// ToolAdmission reports the deterministic decision made for one item in an
// admitted model batch. Denied calls do not reserve a tool-call slot.
type ToolAdmission struct {
	CallID  string
	Allowed bool
	Reason  string
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
		maxModelSteps:                     normalized.MaxModelSteps,
		maxToolCalls:                      normalized.MaxToolCalls,
		maxConsecutiveEquivalentToolCalls: normalized.MaxConsecutiveEquivalentToolCalls,
		equivalentToolCalls:               make(map[string]int),
		admittedToolCalls:                 make(map[string]*admittedToolCall),
		deadline:                          deadline,
		turnContext:                       turnContext,
	}

	return context.WithValue(turnContext, turnBudgetContextKey{}, budget), cancel, nil
}

func normalizeTurnOptions(opts TurnOptions) (TurnOptions, error) {
	defaults := DefaultTurnOptions()
	if opts.MaxModelSteps < 0 {
		return TurnOptions{}, errors.New("max model steps must be >= 0")
	}
	if opts.MaxModelSteps == 0 {
		opts.MaxModelSteps = defaults.MaxModelSteps
	}
	if opts.MaxToolCalls < 0 {
		return TurnOptions{}, errors.New("max tool calls must be >= 0")
	}
	if opts.MaxToolCalls == 0 {
		opts.MaxToolCalls = defaults.MaxToolCalls
	}
	if opts.MaxConsecutiveEquivalentToolCalls < 0 {
		return TurnOptions{}, errors.New("max consecutive equivalent tool calls must be >= 0")
	}
	if opts.MaxConsecutiveEquivalentToolCalls == 0 {
		opts.MaxConsecutiveEquivalentToolCalls = defaults.MaxConsecutiveEquivalentToolCalls
		if opts.MaxConsecutiveEquivalentToolCalls > opts.MaxToolCalls {
			opts.MaxConsecutiveEquivalentToolCalls = opts.MaxToolCalls
		}
	} else if opts.MaxConsecutiveEquivalentToolCalls > opts.MaxToolCalls {
		return TurnOptions{}, fmt.Errorf("max consecutive equivalent tool calls must be <= max tool calls (%d)", opts.MaxToolCalls)
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
	if !budget.acquireToolCall() {
		return ErrToolCallBudgetExceeded
	}
	return nil
}

// AcquireModelStep consumes one tool-enabled model-decision slot. The agent
// layer calls this before a model request that may invoke tools; its reserved
// final tools-disabled response does not consume this budget.
func AcquireModelStep(ctx context.Context) error {
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
	if !budget.acquireModelStep() {
		return ErrModelStepBudgetExceeded
	}
	return nil
}

// AcquireFinalResponse reserves the one forced tools-disabled model response
// permitted for a durable turn. A normal model response that ends a turn does
// not use this reservation; the agent calls it only after tool planning is
// exhausted. Contexts without a turn budget keep the legacy no-op behavior.
func AcquireFinalResponse(ctx context.Context) error {
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
	if !budget.acquireFinalResponse() {
		return ErrFinalResponseBudgetExceeded
	}
	return nil
}

// AdmitToolBatch decides a model response's complete tool batch in source
// order and atomically reserves every admissible tool-call slot. If the batch
// would exceed the remaining call budget, all otherwise admissible calls are
// denied together; this prevents parallel dispatch from choosing a random
// subset. Equivalent-call limits are evaluated per canonical tool name and
// canonical JSON arguments before that reservation.
//
// The returned entries always preserve calls' source order. Each non-empty
// batch requires unique call IDs so StartToolCall can later associate a
// concurrent execution with its precomputed decision.
func AdmitToolBatch(ctx context.Context, calls []ToolCall) ([]ToolAdmission, error) {
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	admissions := make([]ToolAdmission, len(calls))
	budget, ok := FromContext(ctx)
	if !ok {
		for index, call := range calls {
			admissions[index] = ToolAdmission{CallID: call.ID, Allowed: true}
		}
		return admissions, nil
	}
	if err := budget.contextError(ctx); err != nil {
		return nil, err
	}
	return budget.admitToolBatch(calls)
}

// StartToolCall obtains permission to run an individual tool invocation. A
// call admitted through AdmitToolBatch simply claims its pre-reserved slot and
// never acquires a second one. Without prior batch admission this preserves
// the legacy per-call fallback for direct invocations while still enforcing
// the tool-call and equivalent-call budgets.
func StartToolCall(ctx context.Context, call ToolCall) error {
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
	return budget.startToolCall(call)
}

// MarkStateChanged resets the equivalent-call counters after the caller has
// confirmed an actual workspace mutation or external side effect. It does not
// discard pending batch admissions, so calls from the already admitted batch
// keep their deterministic decisions even if they complete concurrently.
func MarkStateChanged(ctx context.Context) {
	budget, ok := FromContext(ctx)
	if !ok || budget == nil {
		return
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	budget.equivalentToolCalls = make(map[string]int)
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

func (b *TurnBudget) acquireToolCall() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.toolCalls >= b.maxToolCalls {
		return false
	}
	b.toolCalls++
	return true
}

func (b *TurnBudget) acquireModelStep() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.modelSteps >= b.maxModelSteps {
		return false
	}
	b.modelSteps++
	return true
}

func (b *TurnBudget) acquireFinalResponse() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.finalResponseAcquired {
		return false
	}
	b.finalResponseAcquired = true
	return true
}

func (b *TurnBudget) admitToolBatch(calls []ToolCall) ([]ToolAdmission, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	admissions := make([]ToolAdmission, len(calls))
	if len(calls) == 0 {
		return admissions, nil
	}

	seenIDs := make(map[string]struct{}, len(calls))
	keys := make([]string, len(calls))
	for index, call := range calls {
		id := strings.TrimSpace(call.ID)
		if id == "" {
			return nil, fmt.Errorf("tool call at index %d: %w", index, ErrToolCallIDRequired)
		}
		if _, duplicate := seenIDs[id]; duplicate {
			return nil, fmt.Errorf("tool call id %q: %w", id, ErrToolCallAlreadyAdmitted)
		}
		if _, exists := b.admittedToolCalls[id]; exists {
			return nil, fmt.Errorf("tool call id %q: %w", id, ErrToolCallAlreadyAdmitted)
		}
		seenIDs[id] = struct{}{}
		keys[index] = canonicalToolCallKey(call)
	}

	// Work on a copy so a whole-batch budget denial leaves both counters and
	// slots unchanged. Source order makes duplicate decisions deterministic.
	tentativeCounts := make(map[string]int, len(b.equivalentToolCalls))
	for key, count := range b.equivalentToolCalls {
		tentativeCounts[key] = count
	}
	allowedIndexes := make([]int, 0, len(calls))
	for index, call := range calls {
		key := keys[index]
		if tentativeCounts[key] >= b.maxConsecutiveEquivalentToolCalls {
			admissions[index] = ToolAdmission{
				CallID:  call.ID,
				Allowed: false,
				Reason:  DenialReason(ErrEquivalentToolCallLimitExceeded),
			}
			continue
		}
		tentativeCounts[key]++
		admissions[index] = ToolAdmission{CallID: call.ID, Allowed: true}
		allowedIndexes = append(allowedIndexes, index)
	}

	if len(allowedIndexes) > b.maxToolCalls-b.toolCalls {
		for _, index := range allowedIndexes {
			admissions[index].Allowed = false
			admissions[index].Reason = DenialReason(ErrToolCallBudgetExceeded)
		}
	} else {
		b.toolCalls += len(allowedIndexes)
		b.equivalentToolCalls = tentativeCounts
	}

	for index, call := range calls {
		admission := admissions[index]
		var admissionErr error
		if !admission.Allowed {
			admissionErr = errorForDenialReason(admission.Reason)
		}
		b.admittedToolCalls[strings.TrimSpace(call.ID)] = &admittedToolCall{
			key:     keys[index],
			allowed: admission.Allowed,
			err:     admissionErr,
		}
	}
	b.hasBatchAdmissions = true
	return admissions, nil
}

func (b *TurnBudget) startToolCall(call ToolCall) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	id := strings.TrimSpace(call.ID)
	key := canonicalToolCallKey(call)
	if id != "" {
		if admitted, ok := b.admittedToolCalls[id]; ok {
			if admitted.key != key {
				return ErrToolCallAdmissionMismatch
			}
			if admitted.err != nil {
				return admitted.err
			}
			if admitted.started {
				return ErrToolCallAlreadyStarted
			}
			admitted.started = true
			return nil
		}
		if b.hasBatchAdmissions {
			return ErrToolCallNotAdmitted
		}
	} else if b.hasBatchAdmissions {
		// Once a model batch has been admitted, every execution must claim one
		// of its stable protocol IDs. Falling back to direct acquisition here
		// would let an uncorrelated tool invocation bypass that batch decision.
		return ErrToolCallNotAdmitted
	}

	if b.toolCalls >= b.maxToolCalls {
		return ErrToolCallBudgetExceeded
	}
	if b.equivalentToolCalls[key] >= b.maxConsecutiveEquivalentToolCalls {
		return ErrEquivalentToolCallLimitExceeded
	}
	b.toolCalls++
	b.equivalentToolCalls[key]++
	return nil
}

func canonicalToolCallKey(call ToolCall) string {
	return strings.TrimSpace(call.Name) + "\x00" + canonicalJSON(call.Arguments)
}

func canonicalJSON(arguments string) string {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return "{}"
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	if err := decoder.Decode(&value); err != nil {
		return "invalid:" + trimmed
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "invalid:" + trimmed
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "invalid:" + trimmed
	}
	return string(canonical)
}

// DenialReason converts a runtime admission denial into the stable reason
// carried in model-readable tool results. Empty means the error is not a
// runtime admission denial.
func DenialReason(err error) string {
	switch {
	case errors.Is(err, ErrToolCallBudgetExceeded):
		return "runtime_tool_budget_exceeded"
	case errors.Is(err, ErrEquivalentToolCallLimitExceeded):
		return "runtime_equivalent_tool_call_limit"
	case errors.Is(err, ErrToolCallNotAdmitted),
		errors.Is(err, ErrToolCallAdmissionMismatch),
		errors.Is(err, ErrToolCallAlreadyStarted):
		return "runtime_tool_call_not_admitted"
	default:
		return ""
	}
}

func errorForDenialReason(reason string) error {
	switch reason {
	case DenialReason(ErrToolCallBudgetExceeded):
		return ErrToolCallBudgetExceeded
	case DenialReason(ErrEquivalentToolCallLimitExceeded):
		return ErrEquivalentToolCallLimitExceeded
	default:
		return ErrToolCallNotAdmitted
	}
}

// MaxToolCalls returns the configured tool-call limit.
func (b *TurnBudget) MaxToolCalls() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.maxToolCalls
}

// ToolCalls returns the number of successfully acquired tool-call slots.
func (b *TurnBudget) ToolCalls() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.toolCalls
}

// RemainingToolCalls returns the number of tool-call slots that remain.
func (b *TurnBudget) RemainingToolCalls() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.maxToolCalls - b.toolCalls
	if remaining < 0 {
		return 0
	}
	return remaining
}

// MaxModelSteps returns the configured tool-enabled model-decision limit.
func (b *TurnBudget) MaxModelSteps() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.maxModelSteps
}

// ModelSteps returns the number of acquired tool-enabled model decisions.
func (b *TurnBudget) ModelSteps() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.modelSteps
}

// RemainingModelSteps returns the number of tool-enabled model decisions
// still available before the agent must request its final tools-disabled reply.
func (b *TurnBudget) RemainingModelSteps() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.maxModelSteps - b.modelSteps
	if remaining < 0 {
		return 0
	}
	return remaining
}

// MaxConsecutiveEquivalentToolCalls returns the repeated-call limit.
func (b *TurnBudget) MaxConsecutiveEquivalentToolCalls() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.maxConsecutiveEquivalentToolCalls
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
