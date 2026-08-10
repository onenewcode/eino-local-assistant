package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"eino-local-assistant/internal/config"

	"github.com/spf13/cobra"
)

type mcpServerEntry struct {
	Name      string           `json:"name"`
	Enabled   bool             `json:"enabled"`
	Transport mcpTransportView `json:"transport"`
}

type mcpTransportView struct {
	Type              string   `json:"type"`
	Command           string   `json:"command,omitempty"`
	Args              []string `json:"args"`
	WorkingDir        string   `json:"working_dir,omitempty"`
	EnvVars           []string `json:"env_vars,omitempty"`
	URL               string   `json:"url,omitempty"`
	BearerTokenEnvVar string   `json:"bearer_token_env_var,omitempty"`
	OAuth             bool     `json:"oauth,omitempty"`
}

func (v mcpTransportView) MarshalJSON() ([]byte, error) {
	if v.Type == config.MCPTransportStreamableHTTP {
		return json.Marshal(struct {
			Type              string `json:"type"`
			URL               string `json:"url"`
			BearerTokenEnvVar string `json:"bearer_token_env_var,omitempty"`
			OAuth             bool   `json:"oauth,omitempty"`
		}{Type: v.Type, URL: v.URL, BearerTokenEnvVar: v.BearerTokenEnvVar, OAuth: v.OAuth})
	}
	return json.Marshal(struct {
		Type       string   `json:"type"`
		Command    string   `json:"command"`
		Args       []string `json:"args"`
		WorkingDir string   `json:"working_dir,omitempty"`
		EnvVars    []string `json:"env_vars,omitempty"`
	}{
		Type:       v.Type,
		Command:    v.Command,
		Args:       v.Args,
		WorkingDir: v.WorkingDir,
		EnvVars:    v.EnvVars,
	})
}

func newMCPCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Manage configured MCP servers",
		Long:  "Manage configured MCP servers in the user-level TOML configuration.",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(
		newMCPListCommand(opts),
		newMCPGetCommand(opts),
		newMCPAddCommand(opts),
		newMCPLoginCommand(opts),
		newMCPLogoutCommand(opts),
		newMCPEnableCommand(opts),
		newMCPDisableCommand(opts),
		newMCPRemoveCommand(opts),
	)
	return cmd
}

func newMCPListCommand(opts *rootOptions) *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List configured MCP servers",
		Long:  "List configured MCP servers without starting them or performing health checks. Environment values are never printed.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return listMCPServers(opts.configPath, jsonOutput, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output the configured servers as JSON")
	return cmd
}

func newMCPGetCommand(opts *rootOptions) *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "get <name>",
		Short: "Show one configured MCP server",
		Long:  "Show one configured MCP server without starting it or performing health checks. Environment values are never printed.",
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.ExactArgs(1)(cmd, args); err != nil {
				return err
			}
			if strings.TrimSpace(args[0]) == "" {
				return fmt.Errorf("MCP server name is required")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return getMCPServer(opts.configPath, args[0], jsonOutput, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output the configured server as JSON")
	return cmd
}

func newMCPAddCommand(opts *rootOptions) *cobra.Command {
	var environment []string
	var endpoint string
	var bearerTokenEnvVar string
	cmd := &cobra.Command{
		Use:   "add <name> (--url <url> | -- <command> [args...])",
		Short: "Add one configured MCP server",
		Long:  "Add one stdio MCP server or one Streamable HTTP MCP server to the user-level TOML configuration. Use -- before a stdio command or --url for a remote endpoint. Environment values are only valid for stdio and are never printed.",
		Args: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("url") {
				if cmd.Flags().ArgsLenAtDash() >= 0 || len(args) != 1 || strings.TrimSpace(args[0]) == "" {
					return fmt.Errorf("mcp add with --url requires <name> and no command")
				}
				if len(environment) > 0 {
					return fmt.Errorf("mcp add --env is only valid with a stdio command")
				}
				if cmd.Flags().Changed("bearer-token-env-var") && strings.TrimSpace(bearerTokenEnvVar) == "" {
					return fmt.Errorf("mcp add --bearer-token-env-var requires an environment variable name")
				}
				return nil
			}
			if cmd.Flags().Changed("bearer-token-env-var") {
				return fmt.Errorf("mcp add --bearer-token-env-var is only valid with --url")
			}
			if cmd.Flags().ArgsLenAtDash() != 1 || len(args) < 2 {
				return fmt.Errorf("mcp add requires <name> --url <url> or <name> -- <command> [args...]")
			}
			if strings.TrimSpace(args[0]) == "" || strings.TrimSpace(args[1]) == "" {
				return fmt.Errorf("mcp server name and command are required")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			server := config.MCPServerConfig{Name: strings.TrimSpace(args[0])}
			if cmd.Flags().Changed("url") {
				server.Type = config.MCPTransportStreamableHTTP
				server.URL = strings.TrimSpace(endpoint)
				server.BearerTokenEnvVar = strings.TrimSpace(bearerTokenEnvVar)
			} else {
				env, err := parseMCPEnvironment(environment)
				if err != nil {
					return err
				}
				server.Command = strings.TrimSpace(args[1])
				server.Args = append([]string(nil), args[2:]...)
				server.Env = env
			}
			if err := config.AddMCPServer(opts.configPath, server); err != nil {
				return err
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "Added MCP server %q.\n", server.Name)
			return err
		},
	}
	cmd.Flags().StringArrayVarP(&environment, "env", "e", nil, "environment variable to set (KEY=VALUE; repeatable)")
	cmd.Flags().StringVar(&endpoint, "url", "", "Streamable HTTP MCP endpoint URL")
	cmd.Flags().StringVar(&bearerTokenEnvVar, "bearer-token-env-var", "", "environment variable containing a Streamable HTTP bearer token")
	return cmd
}

func newMCPEnableCommand(opts *rootOptions) *cobra.Command {
	return newMCPSetEnabledCommand(opts, "enable", "Enable", true)
}

func newMCPDisableCommand(opts *rootOptions) *cobra.Command {
	return newMCPSetEnabledCommand(opts, "disable", "Disable", false)
}

func newMCPSetEnabledCommand(opts *rootOptions, verb, action string, enabled bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   verb + " <name>",
		Short: action + " one configured MCP server",
		Long:  action + " one configured MCP server for future runtimes without starting or stopping an MCP process.",
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.ExactArgs(1)(cmd, args); err != nil {
				return err
			}
			if strings.TrimSpace(args[0]) == "" {
				return fmt.Errorf("MCP server name is required")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[0])
			if err := config.SetMCPServerEnabled(opts.configPath, name, enabled); err != nil {
				return err
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s MCP server %q.\n", action+"d", name)
			return err
		},
	}
	return cmd
}

func newMCPRemoveCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove one configured MCP server",
		Long:  "Remove one configured MCP server from the user-level TOML configuration without starting it.",
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.ExactArgs(1)(cmd, args); err != nil {
				return err
			}
			if strings.TrimSpace(args[0]) == "" {
				return fmt.Errorf("MCP server name is required")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[0])
			if err := config.RemoveMCPServer(opts.configPath, name); err != nil {
				return err
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "Removed MCP server %q.\n", name)
			return err
		},
	}
	return cmd
}

func listMCPServers(configPath string, jsonOutput bool, stdout io.Writer) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	entries := mcpServerEntries(cfg.MCP.Servers)
	if jsonOutput {
		if err := json.NewEncoder(stdout).Encode(entries); err != nil {
			return fmt.Errorf("write MCP server JSON: %w", err)
		}
		return nil
	}
	if len(entries) == 0 {
		_, err := fmt.Fprintln(stdout, "No configured MCP servers.")
		return err
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tTRANSPORT\tCOMMAND/URL\tARGS\tENV\tAUTH\tWORKING DIR")
	for _, entry := range entries {
		endpoint := mcpTransportEndpoint(entry.Transport)
		args := quotedArguments(entry.Transport.Args)
		env := strings.Join(entry.Transport.EnvVars, ",")
		auth := entry.Transport.BearerTokenEnvVar
		if entry.Transport.OAuth {
			auth = "oauth"
		}
		workingDir := entry.Transport.WorkingDir
		if args == "" {
			args = "-"
		}
		if env == "" {
			env = "-"
		}
		if auth == "" {
			auth = "-"
		}
		if workingDir == "" {
			workingDir = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", entry.Name, entry.Transport.Type, endpoint, args, env, auth, workingDir)
	}
	return tw.Flush()
}

func getMCPServer(configPath, name string, jsonOutput bool, stdout io.Writer) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	for _, entry := range mcpServerEntries(cfg.MCP.Servers) {
		if entry.Name != name {
			continue
		}
		if jsonOutput {
			if err := json.NewEncoder(stdout).Encode(entry); err != nil {
				return fmt.Errorf("write MCP server JSON: %w", err)
			}
			return nil
		}
		return writeMCPServerDetails(stdout, entry)
	}
	return fmt.Errorf("MCP server %q is not configured", name)
}

func mcpServerEntries(servers []config.MCPServerConfig) []mcpServerEntry {
	entries := make([]mcpServerEntry, 0, len(servers))
	for _, server := range servers {
		transport := mcpTransportView{Type: server.TransportType()}
		if transport.Type == config.MCPTransportStreamableHTTP {
			transport.URL = strings.TrimSpace(server.URL)
			transport.BearerTokenEnvVar = strings.TrimSpace(server.BearerTokenEnvVar)
			transport.OAuth = server.OAuth
			entries = append(entries, mcpServerEntry{
				Name:      strings.TrimSpace(server.Name),
				Enabled:   server.IsEnabled(),
				Transport: transport,
			})
			continue
		}
		envVars := make([]string, 0, len(server.Env))
		for name := range server.Env {
			envVars = append(envVars, name)
		}
		sort.Strings(envVars)
		entries = append(entries, mcpServerEntry{
			Name:    strings.TrimSpace(server.Name),
			Enabled: server.IsEnabled(),
			Transport: mcpTransportView{
				Type:       transport.Type,
				Command:    strings.TrimSpace(server.Command),
				Args:       append([]string{}, server.Args...),
				WorkingDir: strings.TrimSpace(server.WorkingDir),
				EnvVars:    envVars,
			},
		})
	}
	return entries
}

func mcpTransportEndpoint(transport mcpTransportView) string {
	if transport.Type == config.MCPTransportStreamableHTTP {
		return transport.URL
	}
	return transport.Command
}

func writeMCPServerDetails(stdout io.Writer, entry mcpServerEntry) error {
	if entry.Transport.Type == config.MCPTransportStreamableHTTP {
		if _, err := fmt.Fprintf(stdout, "%s\n  enabled: %t\n  transport: %s\n  url: %s\n", entry.Name, entry.Enabled, entry.Transport.Type, entry.Transport.URL); err != nil {
			return err
		}
		if entry.Transport.BearerTokenEnvVar != "" {
			_, err := fmt.Fprintf(stdout, "  bearer_token_env_var: %s\n", entry.Transport.BearerTokenEnvVar)
			return err
		}
		if entry.Transport.OAuth {
			_, err := fmt.Fprintln(stdout, "  oauth: enabled")
			return err
		}
		return nil
	}
	args := quotedArguments(entry.Transport.Args)
	env := strings.Join(entry.Transport.EnvVars, ",")
	workingDir := entry.Transport.WorkingDir
	if args == "" {
		args = "-"
	}
	if env == "" {
		env = "-"
	}
	if workingDir == "" {
		workingDir = "-"
	}
	_, err := fmt.Fprintf(stdout, "%s\n  enabled: %t\n  transport: %s\n  command: %s\n  args: %s\n  env_vars: %s\n  working_dir: %s\n", entry.Name, entry.Enabled, entry.Transport.Type, entry.Transport.Command, args, env, workingDir)
	return err
}

func parseMCPEnvironment(values []string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	environment := make(map[string]string, len(values))
	for _, value := range values {
		name, envValue, ok := strings.Cut(value, "=")
		if !ok || !isMCPEnvironmentName(name) {
			return nil, fmt.Errorf("mcp environment must use NAME=VALUE with a conventional environment name")
		}
		if _, exists := environment[name]; exists {
			return nil, fmt.Errorf("mcp environment variable %q is repeated", name)
		}
		environment[name] = envValue
	}
	return environment, nil
}

func isMCPEnvironmentName(name string) bool {
	if name == "" {
		return false
	}
	for i := range name {
		char := name[i]
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || char == '_' {
			continue
		}
		if i > 0 && char >= '0' && char <= '9' {
			continue
		}
		return false
	}
	return true
}

func quotedArguments(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, strconv.Quote(arg))
	}
	return strings.Join(quoted, " ")
}
