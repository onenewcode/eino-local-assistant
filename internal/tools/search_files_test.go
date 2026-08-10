package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchFilesReturnsBoundedMatches(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nTODO first\nTODO second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("TODO ignored by glob\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "hidden.go"), []byte("TODO ignored in git\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	search, err := NewSearchFiles(EditFileOptions{WorkingDir: root})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := search.InvokableRun(context.Background(), `{"query":"TODO","glob":"*.go","max_results":1,"context_lines":1}`)
	if err != nil {
		t.Fatal(err)
	}
	var output SearchFilesOutput
	if err := json.Unmarshal([]byte(raw), &output); err != nil {
		t.Fatal(err)
	}
	if len(output.Matches) != 1 || output.Matches[0].Path != "main.go" || output.Matches[0].Line != 2 || !output.Truncated || output.FilesScanned != 1 {
		t.Fatalf("output = %+v", output)
	}
	if len(output.Matches[0].Before) != 1 || output.Matches[0].Before[0] != "package main" || len(output.Matches[0].After) != 1 || output.Matches[0].After[0] != "TODO second" {
		t.Fatalf("context = %+v", output.Matches[0])
	}
}

func TestSearchFilesRejectsInvalidOrUnsafeInput(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	search, err := NewSearchFiles(EditFileOptions{WorkingDir: root})
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{
		`{"query":"["}`,
		`{"query":"main","path":"../outside"}`,
		`{"query":"main","max_results":1001}`,
		`{"query":"main","context_lines":6}`,
	} {
		if _, err := search.InvokableRun(context.Background(), input); err == nil {
			t.Errorf("input %s succeeded, want error", input)
		}
	}
	longLine := strings.Repeat("x", maxSearchLineRunes+1)
	if err := os.WriteFile(filepath.Join(root, "long.txt"), []byte(longLine+" TODO\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := search.InvokableRun(context.Background(), `{"query":"TODO"}`)
	if err != nil {
		t.Fatal(err)
	}
	var output SearchFilesOutput
	if err := json.Unmarshal([]byte(raw), &output); err != nil {
		t.Fatal(err)
	}
	if len(output.Matches) != 1 || !output.Matches[0].TextTruncated {
		t.Fatalf("long line output = %+v", output)
	}
}
