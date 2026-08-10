package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFilePagesUTF8Content(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	read, err := NewReadFile(EditFileOptions{WorkingDir: root})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := read.InvokableRun(context.Background(), `{"path":"main.go","offset":3,"max_bytes":4}`)
	if err != nil {
		t.Fatal(err)
	}
	var output ReadFileOutput
	if err := json.Unmarshal([]byte(raw), &output); err != nil {
		t.Fatal(err)
	}
	if output.Path != "main.go" || output.Offset != 3 || output.Content != "3456" || output.Bytes != 4 || !output.HasMore || !output.Truncated || output.Encoding != "utf-8" {
		t.Fatalf("output = %+v", output)
	}
}

func TestReadFileEncodesBinaryAndRejectsUnsafePaths(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "data.bin"), []byte{0xff, 0x00, 0x01}, 0o600); err != nil {
		t.Fatal(err)
	}
	read, err := NewReadFile(EditFileOptions{WorkingDir: root})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := read.InvokableRun(context.Background(), `{"path":"data.bin"}`)
	if err != nil {
		t.Fatal(err)
	}
	var output ReadFileOutput
	if err := json.Unmarshal([]byte(raw), &output); err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(output.Content)
	if err != nil || string(decoded) != string([]byte{0xff, 0x00, 0x01}) || output.Encoding != "base64" {
		t.Fatalf("binary output = %+v, decoded=%v, err=%v", output, decoded, err)
	}
	if _, err := read.InvokableRun(context.Background(), `{"path":"../outside.txt"}`); err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Fatalf("unsafe path error = %v", err)
	}
}
