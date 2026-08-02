package tools

import (
	"context"
	"fmt"
	"strings"

	"eino-local-assistant/internal/memory"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

// MemoryListInput filters memory_list.
type MemoryListInput struct {
	// Trust is optional: user | candidate | empty for all active.
	Trust string `json:"trust,omitempty" jsonschema:"description=Optional trust filter: user, candidate, or empty for all active memories."`
}

// MemorySearchInput is the query for memory_search.
type MemorySearchInput struct {
	Query string `json:"query" jsonschema:"description=Case-insensitive substring to match against memory key or claim."`
}

// MemoryReadInput selects one memory by id or key.
type MemoryReadInput struct {
	IDOrKey string `json:"id_or_key" jsonschema:"description=Memory id (mem_…) or key slug."`
}

// MemoryEntryView is a compact tool-facing memory row.
type MemoryEntryView struct {
	ID      string `json:"id"`
	Key     string `json:"key"`
	Claim   string `json:"claim"`
	Trust   string `json:"trust"`
	Version int    `json:"version"`
}

// MemoryListOutput is the list tool result.
type MemoryListOutput struct {
	Enabled bool              `json:"enabled"`
	Count   int               `json:"count"`
	Entries []MemoryEntryView `json:"entries"`
	Note    string            `json:"note,omitempty"`
}

// MemorySearchOutput is the search tool result.
type MemorySearchOutput struct {
	Enabled bool              `json:"enabled"`
	Query   string            `json:"query"`
	Count   int               `json:"count"`
	Entries []MemoryEntryView `json:"entries"`
	Note    string            `json:"note,omitempty"`
}

// MemoryReadOutput is the read tool result.
type MemoryReadOutput struct {
	Enabled bool            `json:"enabled"`
	Found   bool            `json:"found"`
	Entry   MemoryEntryView `json:"entry,omitempty"`
	Note    string          `json:"note,omitempty"`
}

// NewMemoryTools registers read-only memory tools when store is non-nil.
func NewMemoryTools(store *memory.Store) ([]tool.BaseTool, error) {
	if store == nil {
		return nil, nil
	}
	listTool, err := utils.InferTool(
		"memory_list",
		"List active project-scoped persistent memories. This is long-term memory, not the current chat transcript. Optional trust filter: user or candidate.",
		func(_ context.Context, input MemoryListInput) (MemoryListOutput, error) {
			if !store.UseEnabled() {
				return MemoryListOutput{Enabled: false, Note: "memory use is disabled"}, nil
			}
			entries, err := store.ListActive()
			if err != nil {
				return MemoryListOutput{}, err
			}
			filter := strings.ToLower(strings.TrimSpace(input.Trust))
			views := make([]MemoryEntryView, 0, len(entries))
			for _, e := range entries {
				if filter != "" && string(e.Trust) != filter {
					continue
				}
				views = append(views, toView(e))
			}
			return MemoryListOutput{Enabled: true, Count: len(views), Entries: views}, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("create memory_list: %w", err)
	}
	searchTool, err := utils.InferTool(
		"memory_search",
		"Search active project-scoped persistent memories by substring on key or claim.",
		func(_ context.Context, input MemorySearchInput) (MemorySearchOutput, error) {
			if !store.UseEnabled() {
				return MemorySearchOutput{Enabled: false, Note: "memory use is disabled"}, nil
			}
			entries, err := store.Search(input.Query)
			if err != nil {
				return MemorySearchOutput{}, err
			}
			views := make([]MemoryEntryView, 0, len(entries))
			for _, e := range entries {
				views = append(views, toView(e))
			}
			return MemorySearchOutput{
				Enabled: true,
				Query:   input.Query,
				Count:   len(views),
				Entries: views,
			}, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("create memory_search: %w", err)
	}
	readTool, err := utils.InferTool(
		"memory_read",
		"Read one active project-scoped persistent memory by id or key.",
		func(_ context.Context, input MemoryReadInput) (MemoryReadOutput, error) {
			if !store.UseEnabled() {
				return MemoryReadOutput{Enabled: false, Note: "memory use is disabled"}, nil
			}
			e, err := store.Get(input.IDOrKey)
			if err != nil {
				return MemoryReadOutput{Enabled: true, Found: false, Note: err.Error()}, nil
			}
			return MemoryReadOutput{Enabled: true, Found: true, Entry: toView(e)}, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("create memory_read: %w", err)
	}
	return []tool.BaseTool{listTool, searchTool, readTool}, nil
}

func toView(e memory.Entry) MemoryEntryView {
	return MemoryEntryView{
		ID:      e.ID,
		Key:     e.Key,
		Claim:   e.Claim,
		Trust:   string(e.Trust),
		Version: e.Version,
	}
}
