package main

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"eino-local-assistant/internal/config"
	"eino-local-assistant/internal/tools"

	"github.com/spf13/cobra"
)

// runDoctor performs local startup diagnostics. It deliberately avoids model
// requests, MCP connections, credential reads, and mutable runtime setup.
func runDoctor(configPath string, stdout io.Writer) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("doctor config: %w", err)
	}
	workspace, err := tools.ResolveWorkspaceRoot(cfg.Workspace.Root)
	if err != nil {
		return fmt.Errorf("doctor workspace: %w", err)
	}
	dataDir, err := cfg.Storage.ResolveDataDir()
	if err != nil {
		return fmt.Errorf("doctor storage: %w", err)
	}

	fmt.Fprintln(stdout, "Doctor")
	fmt.Fprintf(stdout, "  config: ok (%s)\n", configPath)
	fmt.Fprintf(stdout, "  model: ok (%s, %s %s)\n", cfg.Model.Name, cfg.Model.Provider, redactDoctorEndpoint(cfg.Model.BaseURL))
	fmt.Fprintf(stdout, "  workspace: ok (%s)\n", workspace)
	if info, statErr := os.Stat(dataDir); statErr == nil {
		if !info.IsDir() {
			return fmt.Errorf("doctor storage: %q is not a directory", dataDir)
		}
		fmt.Fprintf(stdout, "  storage: ok (%s)\n", dataDir)
	} else if os.IsNotExist(statErr) {
		fmt.Fprintf(stdout, "  storage: pending (%s; created on first durable session)\n", dataDir)
	} else {
		return fmt.Errorf("doctor storage: %w", statErr)
	}

	if len(cfg.MCP.Servers) == 0 {
		fmt.Fprintln(stdout, "  mcp: none configured")
	}
	for _, server := range cfg.MCP.Servers {
		if !server.IsEnabled() {
			fmt.Fprintf(stdout, "  mcp: disabled %s\n", server.Name)
			continue
		}
		switch server.TransportType() {
		case config.MCPTransportStdio:
			resolved, lookErr := exec.LookPath(server.Command)
			if lookErr != nil {
				return fmt.Errorf("doctor MCP server %q: command %q not found: %w", server.Name, server.Command, lookErr)
			}
			fmt.Fprintf(stdout, "  mcp: ok %s (stdio %s)\n", server.Name, resolved)
		case config.MCPTransportStreamableHTTP:
			auth, authErr := doctorMCPAuth(server)
			if authErr != nil {
				return authErr
			}
			fmt.Fprintf(stdout, "  mcp: ok %s (streamable_http %s; %s)\n", server.Name, server.URL, auth)
		}
	}

	sandboxMode := cfg.Sandbox.ModeNormalized()
	if sandboxMode == "" {
		sandboxMode = "off"
	}
	fmt.Fprintf(stdout, "  tools: ok approval=%s sandbox=%s\n", cfg.ApprovalPolicyNormalized(), sandboxMode)
	fmt.Fprintln(stdout, "  result: ok")
	return nil
}

func newDoctorCommand(opts *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose local configuration and prerequisites",
		Long:  "Check local configuration, workspace, session storage, and MCP prerequisites without starting a model or MCP server.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor(opts.configPath, cmd.OutOrStdout())
		},
	}
}

func doctorMCPAuth(server config.MCPServerConfig) (string, error) {
	if server.OAuth {
		return "OAuth configured (credential not checked)", nil
	}
	if name := strings.TrimSpace(server.BearerTokenEnvVar); name != "" {
		value, ok := os.LookupEnv(name)
		if !ok || value == "" {
			return "", fmt.Errorf("doctor MCP server %q: bearer token environment variable %q is not set or is empty", server.Name, name)
		}
		return "bearer environment variable set", nil
	}
	return "no configured authentication", nil
}

func redactDoctorEndpoint(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "(invalid endpoint)"
	}
	return parsed.Scheme + "://" + parsed.Host
}
