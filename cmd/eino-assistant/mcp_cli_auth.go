package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"eino-local-assistant/internal/config"
	"eino-local-assistant/internal/mcpoauth"

	"github.com/spf13/cobra"
)

// mcpOAuthStatusEntry is a redacted, local-only credential view. It is never
// a guarantee that a remote server will accept the token at a later time.
type mcpOAuthStatusEntry struct {
	Name      string     `json:"name"`
	Status    string     `json:"status"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

func newMCPAuthCommand(opts *rootOptions) *cobra.Command {
	return newMCPAuthCommandWithDeps(opts, defaultMCPOAuthCommandDeps())
}

func newMCPAuthCommandWithDeps(opts *rootOptions, deps mcpOAuthCommandDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Inspect local MCP OAuth credential status",
		Long:  "Inspect local MCP OAuth keyring state without contacting configured MCP servers or printing credentials.",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(
		newMCPAuthListCommand(opts, deps),
		newMCPAuthGetCommand(opts, deps),
	)
	return cmd
}

func newMCPAuthListCommand(opts *rootOptions, deps mcpOAuthCommandDeps) *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List local MCP OAuth credential status",
		Long:  "List OAuth-enabled MCP servers and their local keyring state without connecting to an MCP endpoint or printing credentials.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return listMCPAuthStatus(opts.configPath, jsonOutput, cmd.OutOrStdout(), deps)
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output OAuth status as JSON")
	return cmd
}

func newMCPAuthGetCommand(opts *rootOptions, deps mcpOAuthCommandDeps) *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "get <name>",
		Short: "Show local OAuth status for one MCP server",
		Long:  "Show local keyring state for one configured remote MCP server without contacting it or printing credentials.",
		Args:  validMCPServerNameArgument,
		RunE: func(cmd *cobra.Command, args []string) error {
			return getMCPAuthStatus(opts.configPath, args[0], jsonOutput, cmd.OutOrStdout(), deps)
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output OAuth status as JSON")
	return cmd
}

func listMCPAuthStatus(configPath string, jsonOutput bool, stdout io.Writer, deps mcpOAuthCommandDeps) error {
	entries, err := mcpOAuthStatusEntries(configPath, deps)
	if err != nil {
		return err
	}
	if jsonOutput {
		if err := json.NewEncoder(stdout).Encode(entries); err != nil {
			return fmt.Errorf("write MCP OAuth status JSON: %w", err)
		}
		return nil
	}
	if len(entries) == 0 {
		_, err := fmt.Fprintln(stdout, "No OAuth-enabled MCP servers.")
		return err
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSTATUS\tEXPIRES AT")
	for _, entry := range entries {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", entry.Name, entry.Status, mcpOAuthExpiryText(entry.ExpiresAt))
	}
	return tw.Flush()
}

func getMCPAuthStatus(configPath, name string, jsonOutput bool, stdout io.Writer, deps mcpOAuthCommandDeps) error {
	entry, err := mcpOAuthStatusForServer(configPath, name, deps)
	if err != nil {
		return err
	}
	if jsonOutput {
		if err := json.NewEncoder(stdout).Encode(entry); err != nil {
			return fmt.Errorf("write MCP OAuth status JSON: %w", err)
		}
		return nil
	}
	_, err = fmt.Fprintf(stdout, "%s\n  status: %s\n  expires_at: %s\n", entry.Name, entry.Status, mcpOAuthExpiryText(entry.ExpiresAt))
	return err
}

func mcpOAuthStatusEntries(configPath string, deps mcpOAuthCommandDeps) ([]mcpOAuthStatusEntry, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	entries := make([]mcpOAuthStatusEntry, 0, len(cfg.MCP.Servers))
	for _, server := range cfg.MCP.Servers {
		if server.TransportType() != config.MCPTransportStreamableHTTP || !server.OAuth {
			continue
		}
		entries = append(entries, mcpOAuthStatusForConfiguredServer(server, deps))
	}
	return entries, nil
}

func mcpOAuthStatusForServer(configPath, name string, deps mcpOAuthCommandDeps) (mcpOAuthStatusEntry, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return mcpOAuthStatusEntry{}, err
	}
	name = strings.TrimSpace(name)
	for _, server := range cfg.MCP.Servers {
		if strings.TrimSpace(server.Name) != name {
			continue
		}
		if server.TransportType() != config.MCPTransportStreamableHTTP {
			return mcpOAuthStatusEntry{}, fmt.Errorf("MCP server %q does not support OAuth", name)
		}
		if !server.OAuth {
			return mcpOAuthStatusEntry{Name: name, Status: "not_configured"}, nil
		}
		return mcpOAuthStatusForConfiguredServer(server, deps), nil
	}
	return mcpOAuthStatusEntry{}, fmt.Errorf("MCP server %q is not configured", name)
}

func mcpOAuthStatusForConfiguredServer(server config.MCPServerConfig, deps mcpOAuthCommandDeps) mcpOAuthStatusEntry {
	entry := mcpOAuthStatusEntry{Name: strings.TrimSpace(server.Name)}
	if deps.newStore == nil {
		entry.Status = "keyring_unavailable"
		return entry
	}
	store := deps.newStore()
	if store == nil {
		entry.Status = "keyring_unavailable"
		return entry
	}
	token, err := store.Load(entry.Name, strings.TrimSpace(server.URL))
	switch {
	case errors.Is(err, mcpoauth.ErrNotFound):
		entry.Status = "missing"
	case errors.Is(err, mcpoauth.ErrEndpointMismatch):
		entry.Status = "endpoint_mismatch"
	case errors.Is(err, mcpoauth.ErrInvalidCredential):
		entry.Status = "invalid"
	case err != nil:
		entry.Status = "keyring_unavailable"
	case token == nil || strings.TrimSpace(token.AccessToken) == "":
		entry.Status = "invalid"
	default:
		if !token.Expiry.IsZero() {
			expiresAt := token.Expiry.UTC()
			entry.ExpiresAt = &expiresAt
		}
		if token.Valid() {
			entry.Status = "available"
		} else {
			entry.Status = "expired"
		}
	}
	return entry
}

func mcpOAuthExpiryText(expiresAt *time.Time) string {
	if expiresAt == nil {
		return "-"
	}
	return expiresAt.UTC().Format(time.RFC3339)
}
