package tools

import (
	"fmt"
	"strings"
)

type sandboxPermissions string

const (
	sandboxPermissionsDefault   sandboxPermissions = "use_default"
	sandboxPermissionsEscalated sandboxPermissions = "require_escalated"
)

func normalizeSandboxPermissions(raw, justification string) (sandboxPermissions, error) {
	value := sandboxPermissions(strings.ToLower(strings.TrimSpace(raw)))
	switch value {
	case "", sandboxPermissionsDefault:
		return sandboxPermissionsDefault, nil
	case sandboxPermissionsEscalated:
		if strings.TrimSpace(justification) == "" {
			return "", fmt.Errorf("justification is required when sandbox_permissions is %q", sandboxPermissionsEscalated)
		}
		return sandboxPermissionsEscalated, nil
	default:
		return "", fmt.Errorf("sandbox_permissions must be %q or %q", sandboxPermissionsDefault, sandboxPermissionsEscalated)
	}
}
