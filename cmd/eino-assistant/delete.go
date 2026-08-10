package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"eino-local-assistant/internal/config"
	"eino-local-assistant/internal/store"

	"github.com/spf13/cobra"
)

func newDeleteCommand(opts *rootOptions) *cobra.Command {
	var confirmed bool
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <SESSION_ID_OR_NAME>",
		Short: "Permanently delete a saved session",
		Long: "Permanently delete one saved session without starting the TUI or a model. " +
			"Requires --yes or --force and refuses a session with an active turn or pending compaction.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !confirmed && !force {
				return errors.New("delete requires --yes or --force to confirm permanent session removal")
			}
			id := strings.TrimSpace(args[0])
			if id == "" {
				return errors.New("session ID or name is required")
			}
			if err := deleteSession(cmd.Context(), opts.configPath, id); err != nil {
				return err
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "deleted session %s\n", id)
			return err
		},
	}
	cmd.Flags().BoolVar(&confirmed, "yes", false, "confirm permanent deletion")
	cmd.Flags().BoolVar(&force, "force", false, "delete without an interactive confirmation prompt")
	return cmd
}

func deleteSession(ctx context.Context, configPath, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return errors.New("session ID or name is required")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	dataDir, err := cfg.Storage.ResolveDataDir()
	if err != nil {
		return err
	}
	threadStore, err := store.NewThreadStore(dataDir)
	if err != nil {
		return fmt.Errorf("open session store: %w", err)
	}
	defer threadStore.Close()
	sessionID, err = resolveSessionSelector(ctx, threadStore, sessionID, sessionScopeAll)
	if err != nil {
		return err
	}
	if err := threadStore.DeleteThread(ctx, sessionID); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}
