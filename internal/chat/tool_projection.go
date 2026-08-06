package chat

import (
	"context"
	"strings"
)

// toolResultProjectionStore is owned by the session recorder. It bridges a
// durable large-tool-output preview back to the in-flight ReAct loop without
// giving the agent access to the ledger or artifact store.
type toolResultProjectionStore interface {
	modelToolResultProjection(toolCallID string) (string, bool)
}

// toolResultProjectionFailureStore is intentionally separate from the
// projection lookup. Callers that only replay an already-persisted result do
// not need to know whether the recorder later failed, while ReAct must stop
// before it can fall back to a raw result.
type toolResultProjectionFailureStore interface {
	toolResultProjectionFailure() error
}

type toolResultProjectionContextKey struct{}

func withToolResultProjectionStore(ctx context.Context, store toolResultProjectionStore) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if store == nil {
		return ctx
	}
	return context.WithValue(ctx, toolResultProjectionContextKey{}, store)
}

// ProjectToolResultForModel returns the durable, model-visible tool result for
// the current turn when the session recorder has one. The fallback is retained
// for embeddings that do not run through Session and for tool implementations
// that cannot emit a correlated lifecycle event.
func ProjectToolResultForModel(ctx context.Context, toolCallID, fallback string) string {
	if ctx == nil || strings.TrimSpace(toolCallID) == "" {
		return fallback
	}
	store, ok := ctx.Value(toolResultProjectionContextKey{}).(toolResultProjectionStore)
	if !ok || store == nil {
		return fallback
	}
	if projection, ok := store.modelToolResultProjection(toolCallID); ok {
		return projection
	}
	return fallback
}

// ToolResultProjectionFailure reports a durable recorder failure from the
// current turn. ReAct checks this immediately after a tool batch so a failed
// artifact write cannot cause the raw, unprojected output to reach another
// model call.
func ToolResultProjectionFailure(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	store, ok := ctx.Value(toolResultProjectionContextKey{}).(toolResultProjectionFailureStore)
	if !ok || store == nil {
		return nil
	}
	return store.toolResultProjectionFailure()
}
