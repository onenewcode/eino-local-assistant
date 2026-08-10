package main

import (
	"context"
	"io"
	"strings"
	"testing"

	"eino-local-assistant/internal/store"
)

func TestResolveSessionSelectorUsesIDThenExactNameAndScope(t *testing.T) {
	ctx := context.Background()
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer threadStore.Close()
	for _, meta := range []store.ThreadMeta{
		{ID: "session-alpha", Title: "alpha"},
		{ID: "alpha", Title: "ID wins"},
		{ID: "duplicate-one", Title: "duplicate"},
		{ID: "duplicate-two", Title: "duplicate"},
		{ID: "archived-session", Title: "archived name"},
	} {
		state, err := threadStore.CreateThread(ctx, meta, "system")
		if err != nil {
			t.Fatal(err)
		}
		if meta.ID == "archived-session" {
			if _, err := threadStore.ArchiveThread(ctx, state.ID, state.Revision); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, tc := range []struct {
		selector string
		scope    sessionSelectorScope
		want     string
	}{
		{selector: "alpha", scope: sessionScopeActive, want: "alpha"},
		{selector: "session-alpha", scope: sessionScopeActive, want: "session-alpha"},
		{selector: "archived name", scope: sessionScopeArchived, want: "archived-session"},
		{selector: "archived name", scope: sessionScopeAll, want: "archived-session"},
		{selector: "archived-session", scope: sessionScopeActive, want: "archived-session"},
	} {
		got, err := resolveSessionSelector(ctx, threadStore, tc.selector, tc.scope)
		if err != nil || got != tc.want {
			t.Fatalf("resolveSessionSelector(%q) = %q, %v; want %q", tc.selector, got, err, tc.want)
		}
	}
	if _, err := resolveSessionSelector(ctx, threadStore, "archived name", sessionScopeActive); err == nil || !strings.Contains(err.Error(), "no session") {
		t.Fatalf("active archived-name error = %v", err)
	}
	if _, err := resolveSessionSelector(ctx, threadStore, "duplicate", sessionScopeActive); err == nil || !strings.Contains(err.Error(), "ambiguous") || !strings.Contains(err.Error(), "duplicate-one") || !strings.Contains(err.Error(), "duplicate-two") {
		t.Fatalf("ambiguous name error = %v", err)
	}
}

func TestSessionNameFlagsSetTitleAndRejectConflicts(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"chat", "--help"}, {"fork", "--help"}} {
		stdout, _, err := executeForTest(args...)
		if err != nil || !strings.Contains(stdout, "--name") {
			t.Fatalf("execute(%v) name help missing, err=%v\n%s", args, err, stdout)
		}
	}
	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "bare", args: []string{"--name", "bare name"}, want: "bare name"},
		{name: "chat", args: []string{"chat", "--name", "chat name"}, want: "chat name"},
		{name: "fork", args: []string{"fork", "source", "--name", "fork name"}, want: "fork name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got sessionStart
			root := newRootCommandWithDeps(commandDeps{interactive: func(_ string, start sessionStart, _ io.Writer) error {
				got = start
				return nil
			}})
			root.SetArgs(tc.args)
			if err := root.Execute(); err != nil {
				t.Fatalf("execute(%v): %v", tc.args, err)
			}
			if got.title != tc.want {
				t.Fatalf("session title = %q, want %q", got.title, tc.want)
			}
		})
	}
	for _, args := range [][]string{
		{"--title", "one", "--name", "two"},
		{"chat", "--title", "one", "--name", "two"},
		{"fork", "source", "--title", "one", "--name", "two"},
	} {
		root := newRootCommandWithDeps(commandDeps{interactive: func(string, sessionStart, io.Writer) error {
			t.Fatal("interactive runner called with conflicting title flags")
			return nil
		}})
		root.SetArgs(args)
		if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "cannot be used together") {
			t.Fatalf("execute(%v) conflict error = %v", args, err)
		}
	}
}

func TestResolveStartupSessionSelectorsCanonicalizesNames(t *testing.T) {
	ctx := context.Background()
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer threadStore.Close()
	if _, err := threadStore.CreateThread(ctx, store.ThreadMeta{ID: "resume-id", Title: "resume by name"}, "system"); err != nil {
		t.Fatal(err)
	}
	if _, err := threadStore.CreateThread(ctx, store.ThreadMeta{ID: "fork-id", Title: "fork by name"}, "system"); err != nil {
		t.Fatal(err)
	}
	start := sessionStart{resumeID: "resume by name"}
	if err := resolveStartupSessionSelectors(ctx, threadStore, &start); err != nil || start.resumeID != "resume-id" {
		t.Fatalf("resolve resume startup = %#v, %v", start, err)
	}
	start = sessionStart{forkID: "fork by name"}
	if err := resolveStartupSessionSelectors(ctx, threadStore, &start); err != nil || start.forkID != "fork-id" {
		t.Fatalf("resolve fork startup = %#v, %v", start, err)
	}
}
