package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"eino-local-assistant/internal/store"
)

type sessionSelectorScope int

const (
	sessionScopeActive sessionSelectorScope = iota
	sessionScopeArchived
	sessionScopeAll
)

// resolveSessionSelector accepts a durable ID or an exact display name. An ID
// wins even when a title is identical, so scripts never change target after a
// title edit. Names are deliberately exact and ambiguity is a hard failure.
func resolveSessionSelector(ctx context.Context, threadStore *store.ThreadStore, selector string, scope sessionSelectorScope) (string, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return "", errors.New("session ID or name is required")
	}
	if threadStore == nil {
		return "", errors.New("session store is required")
	}
	if meta, err := threadStore.LoadThreadMeta(ctx, selector); err == nil {
		return meta.ID, nil
	}

	var candidates []store.ThreadMeta
	var err error
	switch scope {
	case sessionScopeActive:
		candidates, err = threadStore.ListThreadsReadOnly(ctx)
	case sessionScopeArchived:
		candidates, err = threadStore.ListArchivedThreadsReadOnly(ctx)
	case sessionScopeAll:
		candidates, err = threadStore.ListThreadsReadOnly(ctx)
		if err == nil {
			var archived []store.ThreadMeta
			archived, err = threadStore.ListArchivedThreadsReadOnly(ctx)
			candidates = append(candidates, archived...)
		}
	default:
		return "", errors.New("invalid session selector scope")
	}
	if err != nil {
		return "", fmt.Errorf("list sessions: %w", err)
	}

	matches := make([]string, 0, 1)
	for _, meta := range candidates {
		if meta.Title == selector {
			matches = append(matches, meta.ID)
		}
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no session with ID or name %q", selector)
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("session name %q is ambiguous; matching IDs: %s", selector, strings.Join(matches, ", "))
	}
	return matches[0], nil
}

func resolveSessionTitle(title, name string, titleChanged, nameChanged bool) (string, error) {
	if titleChanged && nameChanged {
		return "", errors.New("--title and --name cannot be used together")
	}
	if nameChanged {
		return strings.TrimSpace(name), nil
	}
	return strings.TrimSpace(title), nil
}
