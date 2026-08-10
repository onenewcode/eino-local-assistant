package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"eino-local-assistant/internal/store"
)

func TestArchiveCommandLifecycleAndArchivedListing(t *testing.T) {
	dataDir := t.TempDir()
	threadStore, err := store.NewThreadStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer threadStore.Close()
	ctx := context.Background()
	if _, err := threadStore.CreateThread(ctx, store.ThreadMeta{ID: "archive-session", Title: "keep this"}, "system"); err != nil {
		t.Fatal(err)
	}
	configPath := writeSessionsConfig(t, dataDir)

	var stdout bytes.Buffer
	command := newArchiveCommand(&rootOptions{configPath: configPath}, true)
	command.SetOut(&stdout)
	command.SetArgs([]string{"archive-session"})
	if err := command.Execute(); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if stdout.String() != "archived session archive-session\n" {
		t.Fatalf("archive output = %q", stdout.String())
	}

	active, err := threadStore.ListThreads(ctx)
	if err != nil || len(active) != 0 {
		t.Fatalf("active sessions after archive = %#v, %v", active, err)
	}
	var archivedJSON bytes.Buffer
	sessions := newSessionsCommand(&rootOptions{configPath: configPath})
	sessions.SetOut(&archivedJSON)
	sessions.SetArgs([]string{"--archived", "--output-format", "json"})
	if err := sessions.Execute(); err != nil {
		t.Fatalf("sessions --archived --output-format json: %v", err)
	}
	var archived []store.ThreadMeta
	if err := json.Unmarshal(archivedJSON.Bytes(), &archived); err != nil {
		t.Fatalf("decode archived JSON %q: %v", archivedJSON.String(), err)
	}
	if len(archived) != 1 || archived[0].ID != "archive-session" || archived[0].ArchivedAt == nil {
		t.Fatalf("archived sessions = %#v", archived)
	}

	stdout.Reset()
	command = newArchiveCommand(&rootOptions{configPath: configPath}, false)
	command.SetOut(&stdout)
	command.SetArgs([]string{"archive-session"})
	if err := command.Execute(); err != nil {
		t.Fatalf("unarchive: %v", err)
	}
	if stdout.String() != "unarchived session archive-session\n" {
		t.Fatalf("unarchive output = %q", stdout.String())
	}
	active, err = threadStore.ListThreads(ctx)
	if err != nil || len(active) != 1 || active[0].ID != "archive-session" || active[0].ArchivedAt != nil {
		t.Fatalf("active sessions after unarchive = %#v, %v", active, err)
	}
}

func TestArchiveCommandPreservesLifecycleSafety(t *testing.T) {
	dataDir := t.TempDir()
	threadStore, err := store.NewThreadStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer threadStore.Close()
	ctx := context.Background()
	state, err := threadStore.CreateThread(ctx, store.ThreadMeta{ID: "archive-active"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.StartTurn(ctx, state.ID, state.Revision, store.TurnStart{TurnID: "active", Input: "work"})
	if err != nil {
		t.Fatal(err)
	}
	command := newArchiveCommand(&rootOptions{configPath: writeSessionsConfig(t, dataDir)}, true)
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	command.SetArgs([]string{state.ID})
	if err := command.Execute(); !errors.Is(err, store.ErrThreadArchiveActiveTurn) {
		t.Fatalf("archive active session error = %v", err)
	}
	if _, err := threadStore.LoadThread(ctx, state.ID); err != nil {
		t.Fatalf("archive active session removed journal: %v", err)
	}

	if err := setSessionArchived(ctx, writeSessionsConfig(t, dataDir), " ", true); err == nil || !strings.Contains(err.Error(), "session id is required") {
		t.Fatalf("empty archive id error = %v", err)
	}
}
