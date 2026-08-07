package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenWritesJSONLifecycleLine(t *testing.T) {
	dir := t.TempDir()
	logger, err := Open(Options{
		DataDir: dir,
		Level:   "info",
		Clock:   func() time.Time { return time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer logger.Close()

	logger.Info("runtime started", "component", "test", "session_id", "sess-1")
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	path := filepath.Join(dir, "logs", "eino-2026-08-07.log")
	if logger.Path() != path {
		t.Fatalf("Path() = %q, want %q", logger.Path(), path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	line := strings.TrimSpace(string(raw))
	var payload map[string]any
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		t.Fatalf("json.Unmarshal(%q): %v", line, err)
	}
	if payload["msg"] != "runtime started" {
		t.Fatalf("msg = %#v", payload["msg"])
	}
	if payload["service"] != "eino-assistant" {
		t.Fatalf("service = %#v", payload["service"])
	}
	if payload["session_id"] != "sess-1" {
		t.Fatalf("session_id = %#v", payload["session_id"])
	}
	if payload["level"] != "INFO" {
		t.Fatalf("level = %#v", payload["level"])
	}
}

func TestOpenDisabledDiscards(t *testing.T) {
	disabled := false
	dir := t.TempDir()
	logger, err := Open(Options{Enabled: &disabled, DataDir: dir})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	logger.Info("should not land on disk")
	_ = logger.Close()
	if _, err := os.Stat(filepath.Join(dir, "logs")); !os.IsNotExist(err) {
		t.Fatalf("expected no logs directory when disabled, err=%v", err)
	}
}

func TestOpenRejectsInvalidLevel(t *testing.T) {
	_, err := Open(Options{DataDir: t.TempDir(), Level: "verbose"})
	if err == nil || !strings.Contains(err.Error(), "logging.level") {
		t.Fatalf("error = %v, want logging.level validation", err)
	}
}

func TestEnvLevelOverridesConfig(t *testing.T) {
	t.Setenv(EnvLevel, "error")
	dir := t.TempDir()
	logger, err := Open(Options{
		DataDir: dir,
		Level:   "debug",
		Clock:   func() time.Time { return time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer logger.Close()
	logger.Info("filtered")
	logger.Error("kept")
	_ = logger.Close()
	raw, err := os.ReadFile(logger.Path())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(raw), "filtered") {
		t.Fatalf("info line should be filtered at error level: %s", raw)
	}
	if !strings.Contains(string(raw), "kept") {
		t.Fatalf("error line missing: %s", raw)
	}
}

func TestRetentionPrunesOldDailyFiles(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(logDir, "eino-2026-07-01.log")
	keep := filepath.Join(logDir, "eino-2026-08-06.log")
	if err := os.WriteFile(old, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keep, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	logger, err := Open(Options{
		DataDir:       dir,
		RetentionDays: 3,
		Clock:         func() time.Time { return time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = logger.Close()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old log should be pruned, err=%v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("recent log should remain: %v", err)
	}
}

func TestContextLoggerAccumulatesAttrs(t *testing.T) {
	var buf bytes.Buffer
	base := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	SetDefault(base)
	t.Cleanup(func() { SetDefault(slog.Default()) })

	ctx := With(context.Background(), "session_id", "s1")
	ctx = With(ctx, "turn_id", "t1")
	InfoContext(ctx, "turn started", "component", "chat")

	var payload map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &payload); err != nil {
		t.Fatalf("json: %v raw=%q", err, buf.String())
	}
	if payload["session_id"] != "s1" || payload["turn_id"] != "t1" {
		t.Fatalf("attrs = %#v", payload)
	}
}

func TestTextFormat(t *testing.T) {
	dir := t.TempDir()
	logger, err := Open(Options{
		DataDir: dir,
		Format:  "text",
		Clock:   func() time.Time { return time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	logger.Info("hello", "k", "v")
	_ = logger.Close()
	raw, err := os.ReadFile(logger.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "msg=hello") || !strings.Contains(string(raw), "k=v") {
		t.Fatalf("text log unexpected: %s", raw)
	}
}

func TestDurationMillis(t *testing.T) {
	if DurationMillis(time.Time{}) != 0 {
		t.Fatal("zero start should yield 0")
	}
	start := time.Now().Add(-50 * time.Millisecond)
	if got := DurationMillis(start); got < 40 {
		t.Fatalf("DurationMillis = %d, want >= 40", got)
	}
}

func TestOpenRequiresDataDirWhenDirEmpty(t *testing.T) {
	_, err := Open(Options{})
	if err == nil || !strings.Contains(err.Error(), "data_dir") {
		t.Fatalf("error = %v, want data_dir requirement", err)
	}
}

// Ensure discard logger accepts writes without panicking.
func TestDiscardLogger(t *testing.T) {
	l := discardLogger()
	l.Info("noop")
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if l.Path() != "" {
		t.Fatalf("path = %q", l.Path())
	}
}

// compile-time guard that Options open path can use a custom writer via
// OpenFile returning a file backed by the temp dir only.
func TestOpenFileHook(t *testing.T) {
	dir := t.TempDir()
	var opened string
	logger, err := Open(Options{
		Dir: dir,
		Clock: func() time.Time {
			return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
		},
		OpenFile: func(name string, flag int, perm os.FileMode) (*os.File, error) {
			opened = name
			return os.OpenFile(name, flag, perm)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = logger.Close()
	if !strings.HasSuffix(opened, "eino-2026-01-02.log") {
		t.Fatalf("opened = %q", opened)
	}
}

func TestFromContextFallsBackToDefault(t *testing.T) {
	var buf bytes.Buffer
	SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { SetDefault(slog.Default()) })
	FromContext(context.Background()).Info("fallback")
	if !strings.Contains(buf.String(), "fallback") {
		t.Fatalf("buf=%q", buf.String())
	}
	// nil context
	FromContext(nil).Info("nil-ctx")
	if !strings.Contains(buf.String(), "nil-ctx") {
		t.Fatalf("buf=%q", buf.String())
	}
}
