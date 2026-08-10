package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"eino-local-assistant/internal/store"
)

func TestDeleteCommandRequiresConfirmationAndDeletesInactiveSession(t *testing.T) {
	dataDir := t.TempDir()
	threadStore, err := store.NewThreadStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer threadStore.Close()
	ctx := context.Background()
	if _, err := threadStore.CreateThread(ctx, store.ThreadMeta{ID: "delete-confirm"}, "system"); err != nil {
		t.Fatal(err)
	}
	configPath := writeSessionsConfig(t, dataDir)

	command := newDeleteCommand(&rootOptions{configPath: configPath})
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	command.SetArgs([]string{"delete-confirm"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "--yes or --force") {
		t.Fatalf("delete without confirmation error = %v", err)
	}
	if _, err := threadStore.LoadThread(ctx, "delete-confirm"); err != nil {
		t.Fatalf("unconfirmed delete removed session: %v", err)
	}

	var stdout bytes.Buffer
	command = newDeleteCommand(&rootOptions{configPath: configPath})
	command.SetOut(&stdout)
	command.SetArgs([]string{"delete-confirm", "--yes"})
	if err := command.Execute(); err != nil {
		t.Fatalf("confirmed delete: %v", err)
	}
	if stdout.String() != "deleted session delete-confirm\n" {
		t.Fatalf("delete output = %q", stdout.String())
	}
	if _, err := threadStore.LoadThread(ctx, "delete-confirm"); err == nil {
		t.Fatal("confirmed delete left session loadable")
	}
}

func TestDeleteCommandForceAndLifecycleSafety(t *testing.T) {
	t.Run("force", func(t *testing.T) {
		dataDir := t.TempDir()
		threadStore, err := store.NewThreadStore(dataDir)
		if err != nil {
			t.Fatal(err)
		}
		defer threadStore.Close()
		if _, err := threadStore.CreateThread(context.Background(), store.ThreadMeta{ID: "delete-force"}, "system"); err != nil {
			t.Fatal(err)
		}
		command := newDeleteCommand(&rootOptions{configPath: writeSessionsConfig(t, dataDir)})
		command.SetOut(&bytes.Buffer{})
		command.SetErr(&bytes.Buffer{})
		command.SetArgs([]string{"delete-force", "--force"})
		if err := command.Execute(); err != nil {
			t.Fatalf("force delete: %v", err)
		}
		if _, err := threadStore.LoadThread(context.Background(), "delete-force"); err == nil {
			t.Fatal("force delete left session loadable")
		}
	})

	for _, lifecycle := range []struct {
		name  string
		start func(context.Context, *store.ThreadStore, store.ThreadState) (store.ThreadState, error)
		want  error
	}{
		{
			name: "active turn",
			start: func(ctx context.Context, threadStore *store.ThreadStore, state store.ThreadState) (store.ThreadState, error) {
				return threadStore.StartTurn(ctx, state.ID, state.Revision, store.TurnStart{TurnID: "active-turn", Input: "work"})
			},
			want: store.ErrThreadDeleteActiveTurn,
		},
		{
			name: "pending compaction",
			start: func(ctx context.Context, threadStore *store.ThreadStore, state store.ThreadState) (store.ThreadState, error) {
				state, err := threadStore.StartTurn(ctx, state.ID, state.Revision, store.TurnStart{TurnID: "turn", Input: "work"})
				if err != nil {
					return store.ThreadState{}, err
				}
				state, err = threadStore.FailTurn(ctx, state.ID, state.Revision, store.TurnFailure{TurnID: "turn", Error: "stopped"})
				if err != nil {
					return store.ThreadState{}, err
				}
				return threadStore.StartCompaction(ctx, state.ID, state.Revision, store.CompactionStart{OperationID: "pending"})
			},
			want: store.ErrThreadDeletePendingCompaction,
		},
	} {
		t.Run(lifecycle.name, func(t *testing.T) {
			dataDir := t.TempDir()
			threadStore, err := store.NewThreadStore(dataDir)
			if err != nil {
				t.Fatal(err)
			}
			defer threadStore.Close()
			ctx := context.Background()
			state, err := threadStore.CreateThread(ctx, store.ThreadMeta{ID: "delete-lifecycle"}, "system")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := lifecycle.start(ctx, threadStore, state); err != nil {
				t.Fatal(err)
			}
			if err := deleteSession(ctx, writeSessionsConfig(t, dataDir), state.ID); !errors.Is(err, lifecycle.want) {
				t.Fatalf("delete lifecycle error = %v, want %v", err, lifecycle.want)
			}
			if _, err := threadStore.LoadThread(ctx, state.ID); err != nil {
				t.Fatalf("lifecycle delete removed session: %v", err)
			}
		})
	}
}
