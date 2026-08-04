package agent

import "strings"

// LayerOptions builds user/project-instructions + memory blocks for ComposeFullSystemPrompt.
// Callers provide the memory summary; this package does not depend on
// internal/memory.
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
	layers, _, err := BuildPromptLayersSnapshot(opts)
	return layers, err
}

// BuildPromptLayersSnapshot loads prompt layers and returns the metadata that
// was captured while loading them. The snapshot contains no instruction body.
func BuildPromptLayersSnapshot(opts LayerOptions) (PromptLayers, PromptLayerSnapshot, error) {
	var layers PromptLayers
	snapshot := PromptLayerSnapshot{Available: true}
	if opts.ProjectInstructionsEnabled {
		if strings.TrimSpace(opts.UserInstructionsRoot) != "" {
			bundle, err := LoadUserInstructions(opts.UserInstructionsRoot, opts.UserInstructionsTokens)
			if err != nil {
				return layers, PromptLayerSnapshot{}, err
			}
			layers.UserInstructionsBlock = FormatUserInstructionsBlock(bundle)
			snapshot.User = PromptLayerBundleSnapshot{
				Available: true,
				Found:     bundle.Found,
				Path:      bundle.Path,
				Tokens:    bundle.Tokens,
				Truncated: bundle.Truncated,
			}
		}
		bundle, err := LoadProjectInstructionsAt(opts.WorkspaceRoot, opts.ProjectInstructionsStartDir, opts.ProjectInstructionsTokens)
		if err != nil {
			return layers, PromptLayerSnapshot{}, err
		}
		layers.RulesBlock = FormatProjectInstructionsBlock(bundle)
		snapshot.Project = PromptProjectSnapshot{
			Available:                true,
			Found:                    bundle.Found,
			Tokens:                   bundle.Tokens,
			Truncated:                bundle.Truncated,
			StartDirOutsideWorkspace: bundle.StartDirOutsideWorkspace,
			Sources:                  make([]PromptProjectSourceSnapshot, 0, len(bundle.Sources)),
		}
		for _, source := range bundle.Sources {
			snapshot.Project.Sources = append(snapshot.Project.Sources, PromptProjectSourceSnapshot{
				Path:      source.Path,
				Title:     source.Title,
				Tokens:    source.Tokens,
				Truncated: source.Truncated,
			})
		}
	}
	layers.MemoryBlock = strings.TrimSpace(opts.MemoryBlock)
	return layers, snapshot, nil
}

// ComposeWithLayers builds the full system prompt from persona + durable layers.
func ComposeWithLayers(persona string, opts LayerOptions) (string, error) {
	prompt, _, err := ComposeWithLayersSnapshot(persona, opts)
	return prompt, err
}

// ComposeWithLayersSnapshot builds the full system prompt and returns the
// source metadata captured during that same composition.
func ComposeWithLayersSnapshot(persona string, opts LayerOptions) (string, PromptLayerSnapshot, error) {
	layers, snapshot, err := BuildPromptLayersSnapshot(opts)
	if err != nil {
		return "", PromptLayerSnapshot{}, err
	}
	return ComposeFullSystemPrompt(persona, layers), snapshot, nil
}
