package chat

import (
	"context"
	"fmt"
	"sync"
)

// turnCallIDAllocator owns locally generated identifiers for one durable
// turn. A task-completion continuation can invoke the model more than once
// while retaining that same durable turn, so counters must outlive one model
// invocation.
type turnCallIDAllocator struct {
	mu            sync.Mutex
	nextModelCall uint64
	nextToolCall  uint64
}

func (a *turnCallIDAllocator) nextModelUsageID() string {
	if a == nil {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nextModelCall++
	return fmt.Sprintf("model-%d", a.nextModelCall)
}

func (a *turnCallIDAllocator) nextLocalToolCallID() string {
	if a == nil {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nextToolCall++
	return fmt.Sprintf("local-tool-call-%d", a.nextToolCall)
}

type turnCallIDAllocatorContextKey struct{}

func withTurnCallIDAllocator(ctx context.Context, allocator *turnCallIDAllocator) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if allocator == nil {
		return ctx
	}
	return context.WithValue(ctx, turnCallIDAllocatorContextKey{}, allocator)
}

func turnCallIDAllocatorFromContext(ctx context.Context) (*turnCallIDAllocator, bool) {
	if ctx == nil {
		return nil, false
	}
	allocator, ok := ctx.Value(turnCallIDAllocatorContextKey{}).(*turnCallIDAllocator)
	return allocator, ok && allocator != nil
}

// NextLocalToolCallID returns a unique local tool-call ID for the current
// durable turn. It returns ok=false outside a Session-managed turn so callers
// can retain their standalone fallback behavior.
func NextLocalToolCallID(ctx context.Context) (id string, ok bool) {
	allocator, ok := turnCallIDAllocatorFromContext(ctx)
	if !ok {
		return "", false
	}
	return allocator.nextLocalToolCallID(), true
}
