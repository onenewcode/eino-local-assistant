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

func newArchiveCommand(opts *rootOptions, archive bool) *cobra.Command {
	name := "archive"
	pastTense := "archived"
	short := "Archive a saved session"
	long := "Archive one saved session without deleting its journal, transcript, checkpoints, or artifacts. Archived sessions are hidden from normal session lists and must be unarchived before they can resume."
	if !archive {
		name = "unarchive"
		pastTense = "unarchived"
		short = "Restore an archived session"
		long = "Restore one non-destructively archived session to normal session lists and resume selection. The session journal and transcript remain unchanged."
	}
	return &cobra.Command{
		Use:   name + " <SESSION_ID_OR_NAME>",
		Short: short,
		Long:  long,
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.ExactArgs(1)(cmd, args); err != nil {
				return err
			}
			if strings.TrimSpace(args[0]) == "" {
				return errors.New("session ID or name is required")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimSpace(args[0])
			if err := setSessionArchived(cmd.Context(), opts.configPath, id, archive); err != nil {
				return err
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s session %s\n", pastTense, id)
			return err
		},
	}
}

func setSessionArchived(ctx context.Context, configPath, sessionID string, archive bool) error {
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
	scope := sessionScopeActive
	if !archive {
		scope = sessionScopeArchived
	}
	sessionID, err = resolveSessionSelector(ctx, threadStore, sessionID, scope)
	if err != nil {
		return err
	}
	state, err := threadStore.LoadThread(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("load session: %w", err)
	}
	if archive {
		_, err = threadStore.ArchiveThread(ctx, sessionID, state.Revision)
	} else {
		_, err = threadStore.UnarchiveThread(ctx, sessionID, state.Revision)
	}
	if err != nil {
		return fmt.Errorf("change session archive state: %w", err)
	}
	return nil
}
