package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditFileReplacesExactTextAtomically(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("before\nvalue\nafter\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	edit, err := NewEditFile(EditFileOptions{WorkingDir: root})
	if err != nil {
		t.Fatalf("NewEditFile() error = %v", err)
	}
	raw, err := edit.InvokableRun(context.Background(), `{"path":"main.go","old_string":"value","new_string":"updated"}`)
	if err != nil {
		t.Fatalf("InvokableRun() error = %v", err)
	}
	var output EditFileOutput
	if err := json.Unmarshal([]byte(raw), &output); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if output.Path != "main.go" || output.Replacements != 1 {
		t.Fatalf("output = %+v", output)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "before\nupdated\nafter\n" {
		t.Fatalf("file = %q", data)
	}
}

func TestEditFileRejectsStaleOrUnsafeEdits(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("same same"), 0o600); err != nil {
		t.Fatal(err)
	}
	edit, err := NewEditFile(EditFileOptions{WorkingDir: root})
	if err != nil {
		t.Fatal(err)
	}
	for name, input := range map[string]string{
		"ambiguous": `{"path":"main.go","old_string":"same","new_string":"new"}`,
		"escape":    `{"path":"../outside.go","old_string":"x","new_string":"y"}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := edit.InvokableRun(context.Background(), input)
			if err == nil {
				t.Fatal("expected error")
			}
			if name == "ambiguous" && !strings.Contains(err.Error(), "matched 2") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
