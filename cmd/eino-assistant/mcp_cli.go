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
	Type       string   `json:"type"`
	Command    string   `json:"command"`
	Args       []string `json:"args"`
	WorkingDir string   `json:"working_dir,omitempty"`
	EnvVars    []string `json:"env_vars,omitempty"`
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
	cmd := &cobra.Command{
		Use:   "add <name> -- <command> [args...]",
		Short: "Add one configured stdio MCP server",
		Long:  "Add one stdio MCP server to the user-level TOML configuration. Use -- before the command. Environment values are written to the configuration and are never printed.",
		Args: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().ArgsLenAtDash() != 1 || len(args) < 2 {
				return fmt.Errorf("mcp add requires <name> -- <command> [args...]")
			}
			if strings.TrimSpace(args[0]) == "" || strings.TrimSpace(args[1]) == "" {
				return fmt.Errorf("mcp server name and command are required")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := parseMCPEnvironment(environment)
			if err != nil {
				return err
			}
			server := config.MCPServerConfig{
				Name:    strings.TrimSpace(args[0]),
				Command: strings.TrimSpace(args[1]),
				Args:    append([]string(nil), args[2:]...),
				Env:     env,
			}
			if err := config.AddMCPServer(opts.configPath, server); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Added MCP server %q.\n", server.Name)
			return err
		},
	}
	cmd.Flags().StringArrayVarP(&environment, "env", "e", nil, "environment variable to set (KEY=VALUE; repeatable)")
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
	fmt.Fprintln(tw, "NAME\tTRANSPORT\tCOMMAND\tARGS\tENV\tWORKING DIR")
	for _, entry := range entries {
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
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", entry.Name, entry.Transport.Type, entry.Transport.Command, args, env, workingDir)
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
		envVars := make([]string, 0, len(server.Env))
		for name := range server.Env {
			envVars = append(envVars, name)
		}
		sort.Strings(envVars)
		entries = append(entries, mcpServerEntry{
			Name:    strings.TrimSpace(server.Name),
			Enabled: server.IsEnabled(),
			Transport: mcpTransportView{
				Type:       "stdio",
				Command:    strings.TrimSpace(server.Command),
				Args:       append([]string{}, server.Args...),
				WorkingDir: strings.TrimSpace(server.WorkingDir),
				EnvVars:    envVars,
			},
		})
	}
	return entries
}

func writeMCPServerDetails(stdout io.Writer, entry mcpServerEntry) error {
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
