package memory

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreAddListDeleteAccept(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	fixed := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	st, err := Open(Options{
		WorkspaceRoot:    ws,
		MaxSummaryTokens: 2500,
		UseEnabled:       true,
		GenerateEnabled:  true,
		Now:              func() time.Time { return fixed },
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := st.AddUser("lang", "Prefer Go"); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	cand, err := st.AddCandidate("build", "use make test", "thread-1", nil)
	if err != nil {
		t.Fatalf("AddCandidate: %v", err)
	}
	list, err := st.ListActive()
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListActive len = %d, want 2", len(list))
	}
	if list[0].Trust != TrustUser {
		t.Fatalf("first trust = %s, want user", list[0].Trust)
	}
	sum, err := st.Summary()
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if !strings.Contains(sum.Text, "Prefer Go") {
		t.Fatalf("summary missing user claim: %q", sum.Text)
	}
	if !strings.Contains(sum.Text, "unverified") {
		t.Fatalf("summary missing unverified marker: %q", sum.Text)
	}
	if _, err := st.Accept(cand.ID); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	got, err := st.Get(cand.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Trust != TrustUser {
		t.Fatalf("trust after accept = %s", got.Trust)
	}
	if _, err := st.Delete("lang"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	list, err = st.ListActive()
	if err != nil {
		t.Fatalf("ListActive after delete: %v", err)
	}
	for _, e := range list {
		if e.Key == "lang" {
			t.Fatalf("deleted key still active")
		}
	}
	gi := filepath.Join(ws, ".eino", ".gitignore")
	data, err := os.ReadFile(gi)
	if err != nil {
		t.Fatalf("gitignore: %v", err)
	}
	if !strings.Contains(string(data), "memory/") {
		t.Fatalf("gitignore missing memory/: %q", data)
	}
}

func TestStoreLWWSameKey(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	st, err := Open(Options{WorkspaceRoot: ws, UseEnabled: true, GenerateEnabled: false})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := st.AddUser("pref", "v1"); err != nil {
		t.Fatalf("add1: %v", err)
	}
	e2, err := st.AddUser("pref", "v2")
	if err != nil {
		t.Fatalf("add2: %v", err)
	}
	if e2.Version != 2 {
		t.Fatalf("version = %d, want 2", e2.Version)
	}
	list, err := st.ListActive()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Claim != "v2" {
		t.Fatalf("active = %+v", list)
	}
}

func TestStoreUpdateUserByIDAndKey(t *testing.T) {
	t.Parallel()
	st, err := Open(Options{WorkspaceRoot: t.TempDir(), UseEnabled: true, GenerateEnabled: true})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	old, err := st.AddCandidate("package-manager", "Use npm", "thread-1", []string{"event-1"})
	if err != nil {
		t.Fatalf("AddCandidate: %v", err)
	}

	corrected, err := st.UpdateUser(old.ID, "Use pnpm")
	if err != nil {
		t.Fatalf("UpdateUser by id: %v", err)
	}
	if corrected.Key != old.Key || corrected.Claim != "Use pnpm" || corrected.Trust != TrustUser {
		t.Fatalf("corrected = %+v", corrected)
	}
	if corrected.Version != 2 || corrected.Supersedes != old.ID {
		t.Fatalf("corrected lineage = %+v", corrected)
	}

	latest, err := st.UpdateUser(old.Key, "Use pnpm through corepack")
	if err != nil {
		t.Fatalf("UpdateUser by key: %v", err)
	}
	if latest.Version != 3 || latest.Supersedes != corrected.ID {
		t.Fatalf("latest lineage = %+v", latest)
	}
	active, err := st.ListActive()
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(active) != 1 || active[0].ID != latest.ID || active[0].Claim != latest.Claim {
		t.Fatalf("active = %+v", active)
	}
	if _, err := st.Get(old.ID); err == nil {
		t.Fatal("superseded id must not be readable as active")
	}
	oldMatches, err := st.Search("Use npm")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(oldMatches) != 0 {
		t.Fatalf("superseded claim returned by search: %+v", oldMatches)
	}
	summary, err := st.Summary()
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if !strings.Contains(summary.Text, latest.Claim) || strings.Contains(summary.Text, "Use npm\n") {
		t.Fatalf("summary = %q", summary.Text)
	}

	var all []Entry
	err = st.withFileLock(func() error {
		var loadErr error
		all, loadErr = st.loadEntriesUnlocked()
		return loadErr
	})
	if err != nil {
		t.Fatalf("load entries: %v", err)
	}
	if len(all) != 3 || all[0].Status != StatusSuperseded || all[1].Status != StatusSuperseded {
		t.Fatalf("history = %+v", all)
	}
}

func TestStoreUpdateUserRejectsInvalidInputWithoutWriting(t *testing.T) {
	t.Parallel()
	st, err := Open(Options{WorkspaceRoot: t.TempDir(), UseEnabled: true})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := st.AddUser("lang", "Prefer Go"); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	for _, tc := range []struct {
		name   string
		target string
		claim  string
	}{
		{name: "empty target", claim: "Prefer Rust"},
		{name: "empty claim", target: "lang"},
		{name: "unknown target", target: "missing", claim: "Prefer Rust"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := st.UpdateUser(tc.target, tc.claim); err == nil {
				t.Fatal("UpdateUser error = nil")
			}
		})
	}
	active, err := st.ListActive()
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(active) != 1 || active[0].Key != "lang" || active[0].Claim != "Prefer Go" {
		t.Fatalf("active after rejected updates = %+v", active)
	}
}

func TestStoreResetClearsMemoryAndConsolidationState(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	fixed := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	st, err := Open(Options{
		WorkspaceRoot:   ws,
		UseEnabled:      true,
		GenerateEnabled: false,
		Now:             func() time.Time { return fixed },
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := st.AddUser("lang", "Prefer Go"); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	if _, err := st.AddCandidate("build", "Use go test", "thread-candidate", nil); err != nil {
		t.Fatalf("AddCandidate: %v", err)
	}
	if err := st.MarkExtracted("thread-processed"); err != nil {
		t.Fatalf("MarkExtracted: %v", err)
	}
	if err := st.RecordExtractError(errTest); err != nil {
		t.Fatalf("RecordExtractError: %v", err)
	}

	var before Meta
	err = st.withFileLock(func() error {
		var readErr error
		before, readErr = st.readMetaUnlocked()
		if readErr != nil {
			return readErr
		}
		before.ClaimedThreads = []string{"thread-claimed"}
		return st.writeMetaUnlocked(before)
	})
	if err != nil {
		t.Fatalf("seed meta: %v", err)
	}

	if err := st.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	active, err := st.ListActive()
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active after reset = %+v", active)
	}
	entries, err := os.ReadFile(filepath.Join(st.Root(), entriesFile))
	if err != nil {
		t.Fatalf("read entries: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries after reset = %q", entries)
	}
	summary, err := st.Summary()
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if !strings.Contains(summary.Text, "No memories stored yet") ||
		strings.Contains(summary.Text, "Prefer Go") || strings.Contains(summary.Text, "Use go test") {
		t.Fatalf("summary after reset = %q", summary.Text)
	}

	reopened, err := Open(Options{
		WorkspaceRoot:   ws,
		UseEnabled:      true,
		GenerateEnabled: false,
		Now:             func() time.Time { return fixed },
	})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	var after Meta
	err = reopened.withFileLock(func() error {
		var readErr error
		after, readErr = reopened.readMetaUnlocked()
		return readErr
	})
	if err != nil {
		t.Fatalf("read reset meta: %v", err)
	}
	if after.ResetGeneration != before.ResetGeneration+1 {
		t.Fatalf("reset generation = %d, want %d", after.ResetGeneration, before.ResetGeneration+1)
	}
	if after.SchemaVersion != before.SchemaVersion || after.WorkspaceRoot != before.WorkspaceRoot ||
		after.UseEnabled != before.UseEnabled || after.GenerateEnabled != before.GenerateEnabled {
		t.Fatalf("preserved meta changed: before=%+v after=%+v", before, after)
	}
	if after.LastConsolidate != nil || after.LastError != "" ||
		len(after.ClaimedThreads) != 0 || len(after.ProcessedThreads) != 0 {
		t.Fatalf("consolidation meta after reset = %+v", after)
	}
}

func TestStoreResetGenerationFencesAnotherStoreInstance(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	first, err := Open(Options{WorkspaceRoot: ws, UseEnabled: true, GenerateEnabled: true})
	if err != nil {
		t.Fatalf("Open first: %v", err)
	}
	second, err := Open(Options{WorkspaceRoot: ws, UseEnabled: true, GenerateEnabled: true})
	if err != nil {
		t.Fatalf("Open second: %v", err)
	}
	generation, err := first.resetGeneration()
	if err != nil {
		t.Fatalf("resetGeneration: %v", err)
	}
	if err := second.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	if _, err := first.addCandidateAtGeneration(generation, "late", "late candidate", "thread-late", nil); !errors.Is(err, errResetGenerationChanged) {
		t.Fatalf("late candidate error = %v, want %v", err, errResetGenerationChanged)
	}
	if err := first.markExtractedAtGeneration(generation, "thread-late"); !errors.Is(err, errResetGenerationChanged) {
		t.Fatalf("late processed error = %v, want %v", err, errResetGenerationChanged)
	}
	if err := first.recordExtractErrorAtGeneration(generation, errTest); !errors.Is(err, errResetGenerationChanged) {
		t.Fatalf("late error record = %v, want %v", err, errResetGenerationChanged)
	}

	active, err := second.ListActive()
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("late candidate survived reset: %+v", active)
	}
	processed, err := second.IsProcessed("thread-late")
	if err != nil {
		t.Fatalf("IsProcessed: %v", err)
	}
	report, err := second.Report()
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if processed || report.LastConsolidate != nil || report.LastError != "" {
		t.Fatalf("late consolidation state: processed=%v report=%+v", processed, report)
	}
}

func TestCandidateDoesNotSupersedeUser(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	st, err := Open(Options{WorkspaceRoot: ws, UseEnabled: true, GenerateEnabled: true})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := st.AddUser("lang", "Prefer Go"); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	_, err = st.AddCandidate("lang", "Prefer Rust", "t1", nil)
	if err == nil {
		t.Fatal("expected candidate refused over user key")
	}
	if !strings.Contains(err.Error(), "candidate refused") {
		t.Fatalf("error = %v", err)
	}
	list, err := st.ListActive()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Claim != "Prefer Go" || list[0].Trust != TrustUser {
		t.Fatalf("active = %+v", list)
	}
}

func TestUserSupersedesCandidate(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	st, err := Open(Options{WorkspaceRoot: ws, UseEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddCandidate("lang", "maybe go", "t1", nil); err != nil {
		t.Fatal(err)
	}
	u, err := st.AddUser("lang", "Prefer Go")
	if err != nil {
		t.Fatal(err)
	}
	if u.Trust != TrustUser {
		t.Fatalf("trust = %s", u.Trust)
	}
	list, err := st.ListActive()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Claim != "Prefer Go" {
		t.Fatalf("active = %+v", list)
	}
}

func TestMarkExtractedOnlyOnSuccess(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	st, err := Open(Options{WorkspaceRoot: ws, UseEnabled: true, GenerateEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordExtractError(errTest); err != nil {
		t.Fatal(err)
	}
	done, err := st.IsProcessed("thread-a")
	if err != nil {
		t.Fatal(err)
	}
	if done {
		t.Fatal("error path must not mark processed")
	}
	if err := st.MarkExtracted("thread-a"); err != nil {
		t.Fatal(err)
	}
	done, err = st.IsProcessed("thread-a")
	if err != nil || !done {
		t.Fatalf("processed = %v err = %v", done, err)
	}
	rep, err := st.Report()
	if err != nil {
		t.Fatal(err)
	}
	if rep.LastError != "" {
		t.Fatalf("last error should clear on success: %q", rep.LastError)
	}
}

var errTest = errString("extract failed")

type errString string

func (e errString) Error() string { return string(e) }

func TestParseExtractJSON(t *testing.T) {
	t.Parallel()
	drafts, err := parseExtractJSON(`{"memories":[{"key":"Go","claim":"Use Go 1.22"}]}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(drafts) != 1 || drafts[0].Key != "go" {
		t.Fatalf("drafts = %+v", drafts)
	}
}
