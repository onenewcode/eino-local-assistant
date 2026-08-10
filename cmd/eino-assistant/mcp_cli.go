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

type mcpListEntry struct {
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
		Long:  "Inspect configured MCP servers from the user-level TOML configuration.",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newMCPListCommand(opts))
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

func listMCPServers(configPath string, jsonOutput bool, stdout io.Writer) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	entries := make([]mcpListEntry, 0, len(cfg.MCP.Servers))
	for _, server := range cfg.MCP.Servers {
		envVars := make([]string, 0, len(server.Env))
		for name := range server.Env {
			envVars = append(envVars, name)
		}
		sort.Strings(envVars)
		entries = append(entries, mcpListEntry{
			Name:    strings.TrimSpace(server.Name),
			Enabled: true,
			Transport: mcpTransportView{
				Type:       "stdio",
				Command:    strings.TrimSpace(server.Command),
				Args:       append([]string{}, server.Args...),
				WorkingDir: strings.TrimSpace(server.WorkingDir),
				EnvVars:    envVars,
			},
		})
	}
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

func quotedArguments(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, strconv.Quote(arg))
	}
	return strings.Join(quoted, " ")
}
