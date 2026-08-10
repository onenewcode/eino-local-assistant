package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"eino-local-assistant/internal/config"
	"eino-local-assistant/internal/store"
)

const maxInteractiveSessionChoices = 30

var errSessionSelectionCancelled = errors.New("session selection cancelled")

// interactiveSessionPicker selects one active durable session before the TUI
// starts. Keeping this terminal interaction outside the TUI avoids opening a
// new model/session merely to render a source-session choice.
type interactiveSessionPicker func(context.Context, string, io.Reader, io.Writer) (string, error)

type latestInteractiveSessionSelector func(context.Context, string) (string, error)

func pickActiveSession(ctx context.Context, configPath string, input io.Reader, output io.Writer) (string, error) {
	if !isInteractive() {
		return "", errors.New("interactive terminal required for session picker")
	}
	metas, err := loadActiveSessionPickerEntries(ctx, configPath)
	if err != nil {
		return "", err
	}
	return promptForSession(input, output, metas)
}

func selectLatestActiveSession(ctx context.Context, configPath string) (string, error) {
	metas, err := loadActiveSessionPickerEntries(ctx, configPath)
	if err != nil {
		return "", err
	}
	if len(metas) == 0 {
		return "", errors.New("no saved sessions available")
	}
	return metas[0].ID, nil
}

// loadActiveSessionPickerEntries reads journals only. A canceled picker must
// not repair a catalog or otherwise mutate a session the user did not select.
func loadActiveSessionPickerEntries(ctx context.Context, configPath string) ([]store.ThreadMeta, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	dataDir, err := cfg.Storage.ResolveDataDir()
	if err != nil {
		return nil, err
	}
	threadStore, err := store.OpenThreadStore(dataDir, store.ThreadStoreOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("open session store: %w", err)
	}
	defer threadStore.Close()
	metas, err := threadStore.ListThreadsReadOnly(ctx)
	if err != nil {
		return nil, fmt.Errorf("list saved sessions: %w", err)
	}
	return metas, nil
}

func promptForSession(input io.Reader, output io.Writer, metas []store.ThreadMeta) (string, error) {
	if len(metas) == 0 {
		return "", errors.New("no saved sessions available")
	}
	if input == nil {
		return "", errors.New("session picker input is unavailable")
	}
	if output == nil {
		output = io.Discard
	}

	count := min(len(metas), maxInteractiveSessionChoices)
	fmt.Fprintln(output, "Saved sessions (most recent first):")
	for index := range count {
		meta := metas[index]
		title := sessionPickerTitle(meta.Title)
		if title == "" {
			title = "(untitled)"
		}
		fmt.Fprintf(output, "  %2d. %s  %s  msgs=%d  updated=%s\n", index+1, meta.ID, title, meta.MessageCount, sessionPickerTime(meta.UpdatedAt))
	}
	if len(metas) > count {
		fmt.Fprintf(output, "  … and %d more; use an explicit session ID to select one not shown\n", len(metas)-count)
	}

	reader := bufio.NewReader(input)
	for {
		fmt.Fprintf(output, "Select a session [1-%d] (q to cancel): ", count)
		line, readErr := reader.ReadString('\n')
		choice := strings.TrimSpace(line)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return "", fmt.Errorf("read session selection: %w", readErr)
		}
		if choice == "" || strings.EqualFold(choice, "q") || strings.EqualFold(choice, "quit") || choice == "\x1b" {
			return "", errSessionSelectionCancelled
		}
		selected, parseErr := strconv.Atoi(choice)
		if parseErr == nil && selected >= 1 && selected <= count {
			return metas[selected-1].ID, nil
		}
		if errors.Is(readErr, io.EOF) {
			return "", errSessionSelectionCancelled
		}
		fmt.Fprintf(output, "Invalid selection %q. Enter a number from 1 to %d, or q to cancel.\n", choice, count)
	}
}

func sessionPickerTitle(title string) string {
	return strings.Join(strings.Fields(title), " ")
}

func sessionPickerTime(updatedAt time.Time) string {
	if updatedAt.IsZero() {
		return "unknown"
	}
	return updatedAt.Local().Format("2006-01-02 15:04")
}
