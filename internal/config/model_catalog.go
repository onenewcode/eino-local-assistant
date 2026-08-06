package config

import (
	"fmt"
	"strings"
	"unicode"
)

// ModelCatalogEntry is one explicitly configured model choice. The catalog is
// a local declaration, not a provider discovery result; its availability still
// depends on the configured endpoint and account.
type ModelCatalogEntry struct {
	// Name is the canonical model or deployment identifier sent to the provider.
	Name string `toml:"name"`
	// DisplayName is the human-facing picker label. It defaults to Name.
	DisplayName string `toml:"display_name"`
	// Aliases are accepted by startup overrides and /model, but are not durable
	// model identities after they resolve to Name.
	Aliases     []string `toml:"aliases"`
	Description string   `toml:"description"`
	// Lifecycle is active, deprecated, or retired. Retired entries stay visible
	// for context but are not offered as picker selections.
	Lifecycle    string                   `toml:"lifecycle"`
	Capabilities ModelCatalogCapabilities `toml:"capabilities"`
}

// ModelCatalogCapabilities contains declared, model-specific picker metadata.
// Zero values mean that the capability is unknown; they do not claim support
// or rejection.
type ModelCatalogCapabilities struct {
	ContextWindowTokens int      `toml:"context_window_tokens"`
	MaxOutputTokens     int      `toml:"max_output_tokens"`
	SupportsReasoning   *bool    `toml:"supports_reasoning"`
	ReasoningEfforts    []string `toml:"reasoning_efforts"`
	// DefaultReasoningEffort is the declared default requested effort for this
	// catalog entry. Empty means unknown or provider default; it is not an
	// observed provider-effective effort.
	DefaultReasoningEffort string   `toml:"default_reasoning_effort"`
	InputModalities        []string `toml:"input_modalities"`
	SupportsTools          *bool    `toml:"supports_tools"`
	SupportsStreaming      *bool    `toml:"supports_streaming"`
}

// CatalogEntries returns a defensive copy suitable for a picker or report.
func (c ModelConfig) CatalogEntries() []ModelCatalogEntry {
	if len(c.Catalog) == 0 {
		return nil
	}
	entries := make([]ModelCatalogEntry, len(c.Catalog))
	copy(entries, c.Catalog)
	for i := range entries {
		normalizeCatalogEntryForDisplay(&entries[i])
		entries[i].Aliases = append([]string(nil), entries[i].Aliases...)
		entries[i].Capabilities.ReasoningEfforts = append([]string(nil), entries[i].Capabilities.ReasoningEfforts...)
		entries[i].Capabilities.InputModalities = append([]string(nil), entries[i].Capabilities.InputModalities...)
		entries[i].Capabilities.SupportsReasoning = cloneCatalogBool(entries[i].Capabilities.SupportsReasoning)
		entries[i].Capabilities.SupportsTools = cloneCatalogBool(entries[i].Capabilities.SupportsTools)
		entries[i].Capabilities.SupportsStreaming = cloneCatalogBool(entries[i].Capabilities.SupportsStreaming)
	}
	return entries
}

// ResolveCatalogName maps a canonical name or alias to its canonical provider
// identifier. Unknown names deliberately remain free-form for custom endpoints.
func (c ModelConfig) ResolveCatalogName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false
	}
	for _, entry := range c.Catalog {
		if strings.EqualFold(strings.TrimSpace(entry.Name), name) {
			return strings.TrimSpace(entry.Name), true
		}
		for _, alias := range entry.Aliases {
			if strings.EqualFold(strings.TrimSpace(alias), name) {
				return strings.TrimSpace(entry.Name), true
			}
		}
	}
	return name, false
}

// CatalogDisplayName returns the configured label for a canonical model name.
// An empty result means the model is a free-form value or has no distinct label.
func (c ModelConfig) CatalogDisplayName(name string) string {
	name = strings.TrimSpace(name)
	for _, entry := range c.Catalog {
		if !strings.EqualFold(strings.TrimSpace(entry.Name), name) {
			continue
		}
		display := strings.TrimSpace(entry.DisplayName)
		if display == "" || display == strings.TrimSpace(entry.Name) {
			return ""
		}
		return display
	}
	return ""
}

func normalizeModelCatalog(entries []ModelCatalogEntry) ([]ModelCatalogEntry, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	if len(entries) > 128 {
		return nil, fmt.Errorf("model.catalog must contain at most 128 entries")
	}
	normalized := make([]ModelCatalogEntry, len(entries))
	seen := make(map[string]string, len(entries)*2)
	for i, raw := range entries {
		entry := raw
		entry.Name = strings.TrimSpace(entry.Name)
		if err := validateCatalogToken(fmt.Sprintf("model.catalog[%d].name", i), entry.Name); err != nil {
			return nil, err
		}
		entry.DisplayName = strings.TrimSpace(entry.DisplayName)
		if err := validateCatalogText(fmt.Sprintf("model.catalog[%d].display_name", i), entry.DisplayName); err != nil {
			return nil, err
		}
		if entry.DisplayName == "" {
			entry.DisplayName = entry.Name
		}
		entry.Description = strings.TrimSpace(entry.Description)
		if err := validateCatalogText(fmt.Sprintf("model.catalog[%d].description", i), entry.Description); err != nil {
			return nil, err
		}
		entry.Lifecycle = strings.ToLower(strings.TrimSpace(entry.Lifecycle))
		if entry.Lifecycle == "" {
			entry.Lifecycle = "active"
		}
		switch entry.Lifecycle {
		case "active", "deprecated", "retired":
		default:
			return nil, fmt.Errorf("model.catalog[%d].lifecycle must be active, deprecated, or retired", i)
		}

		canonicalKey := strings.ToLower(entry.Name)
		if previous, ok := seen[canonicalKey]; ok {
			return nil, fmt.Errorf("model.catalog[%d].name duplicates %s", i, previous)
		}
		seen[canonicalKey] = fmt.Sprintf("model.catalog[%d].name", i)

		aliases := make([]string, 0, len(entry.Aliases))
		for aliasIndex, rawAlias := range entry.Aliases {
			alias := strings.TrimSpace(rawAlias)
			field := fmt.Sprintf("model.catalog[%d].aliases[%d]", i, aliasIndex)
			if err := validateCatalogToken(field, alias); err != nil {
				return nil, err
			}
			key := strings.ToLower(alias)
			if previous, ok := seen[key]; ok {
				return nil, fmt.Errorf("%s duplicates %s", field, previous)
			}
			seen[key] = field
			aliases = append(aliases, alias)
		}
		entry.Aliases = aliases
		if err := normalizeCatalogCapabilities(&entry.Capabilities, i); err != nil {
			return nil, err
		}
		normalized[i] = entry
	}
	return normalized, nil
}

func normalizeCatalogEntryForDisplay(entry *ModelCatalogEntry) {
	if entry == nil {
		return
	}
	entry.Name = strings.TrimSpace(entry.Name)
	entry.DisplayName = strings.TrimSpace(entry.DisplayName)
	if entry.DisplayName == "" {
		entry.DisplayName = entry.Name
	}
	entry.Description = strings.TrimSpace(entry.Description)
	entry.Lifecycle = strings.ToLower(strings.TrimSpace(entry.Lifecycle))
	if entry.Lifecycle == "" {
		entry.Lifecycle = "active"
	}
}

func normalizeCatalogCapabilities(capabilities *ModelCatalogCapabilities, entryIndex int) error {
	if capabilities == nil {
		return nil
	}
	if capabilities.ContextWindowTokens < 0 {
		return fmt.Errorf("model.catalog[%d].capabilities.context_window_tokens must be >= 0", entryIndex)
	}
	if capabilities.MaxOutputTokens < 0 {
		return fmt.Errorf("model.catalog[%d].capabilities.max_output_tokens must be >= 0", entryIndex)
	}
	if capabilities.ContextWindowTokens > 0 && capabilities.MaxOutputTokens >= capabilities.ContextWindowTokens && capabilities.MaxOutputTokens > 0 {
		return fmt.Errorf("model.catalog[%d].capabilities.max_output_tokens must be smaller than context_window_tokens", entryIndex)
	}
	capabilities.DefaultReasoningEffort = strings.TrimSpace(capabilities.DefaultReasoningEffort)
	if capabilities.DefaultReasoningEffort != "" {
		if err := validateCatalogToken(
			fmt.Sprintf("model.catalog[%d].capabilities.default_reasoning_effort", entryIndex),
			capabilities.DefaultReasoningEffort,
		); err != nil {
			return err
		}
	}
	var err error
	capabilities.ReasoningEfforts, err = normalizeCatalogValueList(
		fmt.Sprintf("model.catalog[%d].capabilities.reasoning_efforts", entryIndex),
		capabilities.ReasoningEfforts,
	)
	if err != nil {
		return err
	}
	capabilities.InputModalities, err = normalizeCatalogValueList(
		fmt.Sprintf("model.catalog[%d].capabilities.input_modalities", entryIndex),
		capabilities.InputModalities,
	)
	if err != nil {
		return err
	}
	if capabilities.SupportsReasoning != nil && !*capabilities.SupportsReasoning && len(capabilities.ReasoningEfforts) > 0 {
		return fmt.Errorf("model.catalog[%d].capabilities.supports_reasoning=false conflicts with reasoning_efforts", entryIndex)
	}
	if capabilities.SupportsReasoning == nil && len(capabilities.ReasoningEfforts) > 0 {
		supported := true
		capabilities.SupportsReasoning = &supported
	}
	return nil
}

func normalizeCatalogValueList(field string, values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	normalized := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			return nil, fmt.Errorf("%s[%d] must not be empty", field, index)
		}
		if err := validateCatalogToken(fmt.Sprintf("%s[%d]", field, index), value); err != nil {
			return nil, err
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, value)
	}
	return normalized, nil
}

func validateCatalogText(field, value string) error {
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s must not contain control characters", field)
		}
	}
	return nil
}

func cloneCatalogBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func validateCatalogToken(field, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", field)
	}
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Errorf("%s must be a single token without whitespace or control characters", field)
		}
	}
	return nil
}
