package memory

import (
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
