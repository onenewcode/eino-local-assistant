package agent

import "strings"

// LayerOptions builds user/project-instructions + memory blocks for ComposeFullSystemPrompt.
// Callers load AGENTS.md and memory summary themselves; this package does not
// depend on internal/memory.
type LayerOptions struct {
	WorkspaceRoot string
	// UserInstructionsRoot is the fixed user home instructions directory. An
	// empty root intentionally preserves project-only callers.
	UserInstructionsRoot   string
	UserInstructionsTokens int
	// ProjectInstructionsStartDir is the process startup directory used for
	// root-to-cwd project instruction discovery. Empty keeps root-only behavior.
	ProjectInstructionsStartDir string
	ProjectInstructionsEnabled  bool
	ProjectInstructionsTokens   int
	// MemoryBlock is an already-formatted memory section (may be empty).
	// Use FormatMemoryBlock(summaryText) when injection is enabled.
	MemoryBlock string
}

// BuildPromptLayers loads AGENTS.md and attaches a pre-rendered memory block.
func BuildPromptLayers(opts LayerOptions) (PromptLayers, error) {
	var layers PromptLayers
	if opts.ProjectInstructionsEnabled {
		if strings.TrimSpace(opts.UserInstructionsRoot) != "" {
			bundle, err := LoadUserInstructions(opts.UserInstructionsRoot, opts.UserInstructionsTokens)
			if err != nil {
				return layers, err
			}
			layers.UserInstructionsBlock = FormatUserInstructionsBlock(bundle)
		}
		bundle, err := LoadProjectInstructionsAt(opts.WorkspaceRoot, opts.ProjectInstructionsStartDir, opts.ProjectInstructionsTokens)
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
