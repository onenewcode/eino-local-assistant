package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"eino-local-assistant/internal/config"
	"eino-local-assistant/internal/store"

	"github.com/cloudwego/eino/schema"
	"github.com/spf13/cobra"
)

type sessionExport struct {
	Meta     store.ThreadMeta  `json:"meta"`
	Messages []*schema.Message `json:"messages"`
}

func newExportCommand(opts *rootOptions) *cobra.Command {
	var outputFormat string
	cmd := &cobra.Command{
		Use:   "export <session-id>",
		Short: "Export a saved session",
		Long:  "Export the complete visible transcript of a saved session as Markdown or JSON without opening the TUI or starting a model.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := normalizeExportFormat(outputFormat)
			if err != nil {
				return err
			}
			return exportSession(opts.configPath, strings.TrimSpace(args[0]), format, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&outputFormat, "output-format", "markdown", "export format: markdown or json")
	return cmd
}

func normalizeExportFormat(raw string) (string, error) {
	format := strings.ToLower(strings.TrimSpace(raw))
	if format == "" {
		return "markdown", nil
	}
	if format != "markdown" && format != "json" {
		return "", fmt.Errorf("unsupported export format %q (choose markdown or json)", raw)
	}
	return format, nil
}

func exportSession(configPath, sessionID, format string, stdout io.Writer) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("session id is required")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	dataDir, err := cfg.Storage.ResolveDataDir()
	if err != nil {
		return err
	}
	sessionStore, err := store.OpenThreadStore(dataDir, store.ThreadStoreOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("open session store: %w", err)
	}
	defer sessionStore.Close()
	ctx := context.Background()
	state, messages, err := sessionStore.LoadThreadTranscriptReadOnly(ctx, sessionID, 0)
	if err != nil {
		return fmt.Errorf("load session transcript: %w", err)
	}
	export := sessionExport{Meta: state.Meta, Messages: messages}
	if format == "json" {
		if err := json.NewEncoder(stdout).Encode(export); err != nil {
			return fmt.Errorf("write session JSON: %w", err)
		}
		return nil
	}
	return writeMarkdownExport(stdout, export)
}

func writeMarkdownExport(writer io.Writer, export sessionExport) error {
	if _, err := fmt.Fprintf(writer, "# Session %s\n\n", export.Meta.ID); err != nil {
		return err
	}
	if export.Meta.Title != "" {
		if _, err := fmt.Fprintf(writer, "Title: %s\n\n", export.Meta.Title); err != nil {
			return err
		}
	}
	for _, message := range export.Messages {
		if message == nil {
			continue
		}
		if _, err := fmt.Fprintf(writer, "## %s\n\n%s\n\n", markdownMessageRole(message.Role), message.Content); err != nil {
			return err
		}
	}
	return nil
}

func markdownMessageRole(role schema.RoleType) string {
	switch role {
	case schema.System:
		return "System"
	case schema.User:
		return "User"
	case schema.Assistant:
		return "Assistant"
	case schema.Tool:
		return "Tool"
	default:
		return string(role)
	}
}
