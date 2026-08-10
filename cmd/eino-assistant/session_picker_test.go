package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"eino-local-assistant/internal/store"
)

func TestPromptForSessionSelectsDisplayedActiveEntry(t *testing.T) {
	metas := []store.ThreadMeta{
		{ID: "newest", Title: "newest\n session", MessageCount: 3, UpdatedAt: time.Date(2026, 8, 11, 9, 30, 0, 0, time.UTC)},
		{ID: "older", Title: "", MessageCount: 1, UpdatedAt: time.Date(2026, 8, 10, 9, 30, 0, 0, time.UTC)},
	}
	var output bytes.Buffer
	id, err := promptForSession(strings.NewReader("2\n"), &output, metas)
	if err != nil || id != "older" {
		t.Fatalf("promptForSession() = %q, %v; want older", id, err)
	}
	for _, want := range []string{"Saved sessions", "newest session", "(untitled)", "Select a session [1-2]"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("picker output missing %q:\n%s", want, output.String())
		}
	}
}

func TestPromptForSessionRetriesAndCancelsWithoutImplicitLatest(t *testing.T) {
	metas := []store.ThreadMeta{{ID: "newest", UpdatedAt: time.Now()}}
	var output bytes.Buffer
	_, err := promptForSession(strings.NewReader("9\nq\n"), &output, metas)
	if !errors.Is(err, errSessionSelectionCancelled) {
		t.Fatalf("cancel error = %v", err)
	}
	if !strings.Contains(output.String(), "Invalid selection") {
		t.Fatalf("picker did not explain retry:\n%s", output.String())
	}

	_, err = promptForSession(strings.NewReader(""), io.Discard, metas)
	if !errors.Is(err, errSessionSelectionCancelled) {
		t.Fatalf("EOF error = %v, want cancellation", err)
	}
}

func TestPromptForSessionBoundsRecentChoices(t *testing.T) {
	metas := make([]store.ThreadMeta, maxInteractiveSessionChoices+1)
	for index := range metas {
		metas[index] = store.ThreadMeta{ID: fmt.Sprintf("session-%02d", index), UpdatedAt: time.Now()}
	}
	var output bytes.Buffer
	_, err := promptForSession(strings.NewReader("q\n"), &output, metas)
	if !errors.Is(err, errSessionSelectionCancelled) {
		t.Fatalf("picker cancel error = %v", err)
	}
	if !strings.Contains(output.String(), "and 1 more") || strings.Contains(output.String(), "session-30") {
		t.Fatalf("bounded picker output =\n%s", output.String())
	}
}

func TestActiveSessionPickerEntriesExcludeArchivedAndKeepNewestFirst(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	threadStore, err := store.NewThreadStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer threadStore.Close()
	older, err := threadStore.CreateThread(ctx, store.ThreadMeta{ID: "older", UpdatedAt: time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)}, "system")
	if err != nil {
		t.Fatal(err)
	}
	newer, err := threadStore.CreateThread(ctx, store.ThreadMeta{ID: "newer", UpdatedAt: time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)}, "system")
	if err != nil {
		t.Fatal(err)
	}
	archived, err := threadStore.CreateThread(ctx, store.ThreadMeta{ID: "archived", UpdatedAt: time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)}, "system")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := threadStore.ArchiveThread(ctx, archived.ID, archived.Revision); err != nil {
		t.Fatal(err)
	}

	metas, err := loadActiveSessionPickerEntries(ctx, writeSessionsConfig(t, dataDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 2 || metas[0].ID != newer.ID || metas[1].ID != older.ID {
		t.Fatalf("picker entries = %#v", metas)
	}
	selected, err := selectLatestActiveSession(ctx, writeSessionsConfig(t, dataDir))
	if err != nil || selected != newer.ID {
		t.Fatalf("latest = %q, %v; want %q", selected, err, newer.ID)
	}
}

func TestResumeAndForkOpenPickerOnlyWhenSelectorIsOmitted(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want func(sessionStart) bool
	}{
		{name: "resume picker", args: []string{"resume"}, want: func(start sessionStart) bool { return start.resumeID == "picked" }},
		{name: "fork picker", args: []string{"fork"}, want: func(start sessionStart) bool { return start.forkID == "picked" && !start.forkLast }},
		{name: "resume direct", args: []string{"resume", "direct"}, want: func(start sessionStart) bool { return start.resumeID == "direct" }},
		{name: "fork direct", args: []string{"fork", "direct"}, want: func(start sessionStart) bool { return start.forkID == "direct" }},
		{name: "resume last", args: []string{"resume", "--last"}, want: func(start sessionStart) bool { return start.resumeID == "latest" }},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			pickerCalls := 0
			var got sessionStart
			root := newRootCommandWithDeps(commandDeps{
				interactive: func(_ string, start sessionStart, _ io.Writer) error {
					got = start
					return nil
				},
				sessionPicker: func(context.Context, string, io.Reader, io.Writer) (string, error) {
					pickerCalls++
					return "picked", nil
				},
				selectLatestSaved: func(context.Context, string) (string, error) {
					return "latest", nil
				},
			})
			root.SetArgs(tc.args)
			if err := root.Execute(); err != nil {
				t.Fatalf("execute(%v): %v", tc.args, err)
			}
			if !tc.want(got) {
				t.Fatalf("session start = %#v", got)
			}
			wantPickerCalls := 0
			if tc.name == "resume picker" || tc.name == "fork picker" {
				wantPickerCalls = 1
			}
			if pickerCalls != wantPickerCalls {
				t.Fatalf("picker calls = %d, want %d", pickerCalls, wantPickerCalls)
			}
		})
	}
}

func TestResumeRejectsDirectSelectorWithLast(t *testing.T) {
	root := newRootCommandWithDeps(commandDeps{interactive: func(string, sessionStart, io.Writer) error {
		t.Fatal("interactive runner should not run")
		return nil
	}})
	root.SetArgs([]string{"resume", "direct", "--last"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("resume conflicting selector error = %v", err)
	}
}

func TestSessionPickerCancellationDoesNotStartInteractiveSession(t *testing.T) {
	called := false
	root := newRootCommandWithDeps(commandDeps{
		interactive: func(string, sessionStart, io.Writer) error {
			called = true
			return nil
		},
		sessionPicker: func(context.Context, string, io.Reader, io.Writer) (string, error) {
			return "", errSessionSelectionCancelled
		},
	})
	root.SetArgs([]string{"resume"})
	err := root.Execute()
	if !errors.Is(err, errSessionSelectionCancelled) || called {
		t.Fatalf("resume cancellation error=%v interactive called=%t", err, called)
	}
}

func TestSessionPickerHelpExplainsDefaultAndLatestPaths(t *testing.T) {
	for _, args := range [][]string{{"resume", "--help"}, {"fork", "--help"}} {
		stdout, _, err := executeForTest(args...)
		if err != nil {
			t.Fatalf("help %v: %v", args, err)
		}
		for _, want := range []string{"picker", "--last"} {
			if !strings.Contains(stdout, want) {
				t.Fatalf("help %v missing %q:\n%s", args, want, stdout)
			}
		}
	}
}
