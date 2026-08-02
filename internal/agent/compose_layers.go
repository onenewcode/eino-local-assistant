package agent

import "strings"

// LayerOptions builds project-instructions + memory blocks for ComposeFullSystemPrompt.
// Callers load AGENTS.md and memory summary themselves; this package does not
// depend on internal/memory.
type LayerOptions struct {
	WorkspaceRoot              string
	ProjectInstructionsEnabled bool
	ProjectInstructionsTokens  int
	// MemoryBlock is an already-formatted memory section (may be empty).
	// Use FormatMemoryBlock(summaryText) when injection is enabled.
	MemoryBlock string
}

// BuildPromptLayers loads AGENTS.md and attaches a pre-rendered memory block.
func BuildPromptLayers(opts LayerOptions) (PromptLayers, error) {
	var layers PromptLayers
	if opts.ProjectInstructionsEnabled {
		bundle, err := LoadProjectInstructions(opts.WorkspaceRoot, opts.ProjectInstructionsTokens)
		if err != nil {
			return layers, err
		}
		layers.RulesBlock = FormatProjectInstructionsBlock(bundle)
	}
	layers.MemoryBlock = strings.TrimSpace(opts.MemoryBlock)
	return layers, nil
}

// ComposeWithLayers builds the full system prompt from persona + durable layers.
func ComposeWithLayers(persona string, opts LayerOptions) (string, error) {
	layers, err := BuildPromptLayers(opts)
	if err != nil {
		return "", err
	}
	return ComposeFullSystemPrompt(persona, layers), nil
}
