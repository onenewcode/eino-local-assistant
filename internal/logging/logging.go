// Package logging provides process-scoped structured logging with optional
// durable file sinks. It wraps the standard library log/slog package and is
// intentionally free of product-domain imports so chat, agent, and tools can
// depend on it without cycle risk.
//
// Design goals (aligned with local coding-agent practice):
//   - High-signal Info events for lifecycle (startup, turn, model step, tool)
//   - Debug for noisier local detail
//   - File persistence under the session data root by default
//   - No user prompt / tool argument bodies at Info (privacy)
//   - TUI-safe: stderr is off by default so logs do not pollute the UI
package logging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// DefaultLevel is used when configuration omits or blanks level.
	DefaultLevel = "info"
	// DefaultFormat is the durable file encoding.
	DefaultFormat = "json"
	// DefaultRetentionDays keeps roughly a week of daily log files.
	DefaultRetentionDays = 7
	// EnvLevel overrides configured level for one process (diagnostics).
	EnvLevel = "EINO_LOG_LEVEL"
	// fileNamePrefix is the daily log basename prefix.
	fileNamePrefix = "eino"
)

// Options configures Open. Zero values use product defaults.
type Options struct {
	// Enabled controls whether file (and optional stderr) logging is active.
	// Nil means enabled.
	Enabled *bool
	// Level is debug | info | warn | error. Empty uses DefaultLevel, then
	// EINO_LOG_LEVEL when set.
	Level string
	// Dir is the absolute or relative log directory. Empty uses
	// <DataDir>/logs.
	Dir string
	// Format is json | text. Empty uses DefaultFormat for files.
	Format string
	// Stderr also writes the same structured stream to os.Stderr.
	// Keep false for interactive TUI so Bubble Tea is not corrupted.
	Stderr bool
	// RetentionDays is how many daily files to keep. Zero uses
	// DefaultRetentionDays; negative disables pruning.
	RetentionDays int
	// DataDir is the resolved session storage root used when Dir is empty.
	DataDir string
	// AddSource attaches caller file:line. Off by default (noisy in files).
	AddSource bool
	// Clock overrides time for tests (daily file name + retention).
	Clock func() time.Time
	// OpenFile overrides os.OpenFile for tests.
	OpenFile func(name string, flag int, perm os.FileMode) (*os.File, error)
	// ReadDir overrides os.ReadDir for retention tests.
	ReadDir func(name string) ([]os.DirEntry, error)
	// Remove overrides os.Remove for retention tests.
	Remove func(name string) error
	// MkdirAll overrides os.MkdirAll for tests.
	MkdirAll func(path string, perm os.FileMode) error
}

// Logger is a slog.Logger plus the resources that must be closed on shutdown.
type Logger struct {
	*slog.Logger
	path   string
	closer io.Closer
}

// Path returns the durable log file path, or empty when only stderr is used.
func (l *Logger) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Close flushes and closes the durable sink. Safe on nil.
func (l *Logger) Close() error {
	if l == nil || l.closer == nil {
		return nil
	}
	return l.closer.Close()
}

// Open builds a process logger. When logging is disabled it returns a discard
// logger and a no-op closer so callers can always call Close.
func Open(opts Options) (*Logger, error) {
	if opts.Enabled != nil && !*opts.Enabled {
		return discardLogger(), nil
	}
	level, err := parseLevel(effectiveLevel(opts.Level))
	if err != nil {
		return nil, err
	}
	format, err := parseFormat(opts.Format)
	if err != nil {
		return nil, err
	}
	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}
	mkdirAll := opts.MkdirAll
	if mkdirAll == nil {
		mkdirAll = os.MkdirAll
	}
	openFile := opts.OpenFile
	if openFile == nil {
		openFile = os.OpenFile
	}

	var writers []io.Writer
	var path string
	var file *os.File
	logDir, err := resolveLogDir(opts.Dir, opts.DataDir)
	if err != nil {
		return nil, err
	}
	if logDir != "" {
		if err := mkdirAll(logDir, 0o755); err != nil {
			return nil, fmt.Errorf("create log directory: %w", err)
		}
		path = filepath.Join(logDir, dailyFileName(clock()))
		file, err = openFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("open log file: %w", err)
		}
		writers = append(writers, file)
		pruneOldLogs(logDir, clock(), retentionDays(opts.RetentionDays), opts.ReadDir, opts.Remove)
	}
	if opts.Stderr {
		writers = append(writers, os.Stderr)
	}
	if len(writers) == 0 {
		// No sink configured: still return a working logger that discards.
		return discardLogger(), nil
	}

	handlerOpts := &slog.HandlerOptions{
		Level:     level,
		AddSource: opts.AddSource,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			// Keep time RFC3339Nano and stable for file grepping.
			if a.Key == slog.TimeKey && a.Value.Kind() == slog.KindTime {
				return slog.String(slog.TimeKey, a.Value.Time().UTC().Format(time.RFC3339Nano))
			}
			return a
		},
	}
	var handler slog.Handler
	out := io.MultiWriter(writers...)
	switch format {
	case "text":
		handler = slog.NewTextHandler(out, handlerOpts)
	default:
		handler = slog.NewJSONHandler(out, handlerOpts)
	}
	logger := &Logger{
		Logger: slog.New(handler).With(
			"service", "eino-assistant",
		),
		path:   path,
		closer: file,
	}
	return logger, nil
}

func discardLogger() *Logger {
	return &Logger{
		Logger: slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 4})),
	}
}

func effectiveLevel(configured string) string {
	if env := strings.TrimSpace(os.Getenv(EnvLevel)); env != "" {
		return env
	}
	if strings.TrimSpace(configured) == "" {
		return DefaultLevel
	}
	return configured
}

func parseLevel(raw string) (slog.Leveler, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return nil, fmt.Errorf("logging.level %q is invalid; use debug, info, warn, or error", raw)
	}
}

func parseFormat(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "json":
		return "json", nil
	case "text":
		return "text", nil
	default:
		return "", fmt.Errorf("logging.format %q is invalid; use json or text", raw)
	}
}

func resolveLogDir(dir, dataDir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		dataDir = strings.TrimSpace(dataDir)
		if dataDir == "" {
			return "", errors.New("logging: data_dir is required when logging.dir is empty")
		}
		if !filepath.IsAbs(dataDir) {
			abs, err := filepath.Abs(dataDir)
			if err != nil {
				return "", fmt.Errorf("resolve logging data_dir: %w", err)
			}
			dataDir = abs
		}
		return filepath.Join(dataDir, "logs"), nil
	}
	if !filepath.IsAbs(dir) {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return "", fmt.Errorf("resolve logging.dir: %w", err)
		}
		dir = abs
	}
	return dir, nil
}

func dailyFileName(now time.Time) string {
	return fmt.Sprintf("%s-%s.log", fileNamePrefix, now.UTC().Format("2006-01-02"))
}

func retentionDays(days int) int {
	if days == 0 {
		return DefaultRetentionDays
	}
	return days
}

func pruneOldLogs(dir string, now time.Time, keepDays int, readDir func(string) ([]os.DirEntry, error), remove func(string) error) {
	if keepDays < 0 {
		return
	}
	if readDir == nil {
		readDir = os.ReadDir
	}
	if remove == nil {
		remove = os.Remove
	}
	entries, err := readDir(dir)
	if err != nil {
		return
	}
	cutoff := now.UTC().AddDate(0, 0, -keepDays)
	prefix := fileNamePrefix + "-"
	suffix := ".log"
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
			continue
		}
		datePart := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
		day, err := time.Parse("2006-01-02", datePart)
		if err != nil {
			continue
		}
		if day.Before(cutoff) {
			_ = remove(filepath.Join(dir, name))
		}
	}
}

// --- process default -------------------------------------------------------

var defaultLogger atomic.Pointer[slog.Logger]

func init() {
	defaultLogger.Store(slog.Default())
}

// SetDefault installs the process-wide default used by L() and package helpers.
func SetDefault(l *slog.Logger) {
	if l == nil {
		l = slog.Default()
	}
	defaultLogger.Store(l)
	slog.SetDefault(l)
}

// L returns the process default logger.
func L() *slog.Logger {
	if l := defaultLogger.Load(); l != nil {
		return l
	}
	return slog.Default()
}

// --- context ---------------------------------------------------------------

type ctxKey struct{}

// With returns a child context that carries logger attributes. Nested With
// calls accumulate attributes on the derived logger.
func With(ctx context.Context, args ...any) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxKey{}, FromContext(ctx).With(args...))
}

// FromContext returns the logger bound to ctx, or the process default.
func FromContext(ctx context.Context) *slog.Logger {
	if ctx != nil {
		if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok && l != nil {
			return l
		}
	}
	return L()
}

// InfoContext logs at Info with the context logger.
func InfoContext(ctx context.Context, msg string, args ...any) {
	FromContext(ctx).Info(msg, args...)
}

// DebugContext logs at Debug with the context logger.
func DebugContext(ctx context.Context, msg string, args ...any) {
	FromContext(ctx).Debug(msg, args...)
}

// WarnContext logs at Warn with the context logger.
func WarnContext(ctx context.Context, msg string, args ...any) {
	FromContext(ctx).Warn(msg, args...)
}

// ErrorContext logs at Error with the context logger.
func ErrorContext(ctx context.Context, msg string, args ...any) {
	FromContext(ctx).Error(msg, args...)
}

// DurationMillis returns elapsed wall time in whole milliseconds for logs.
func DurationMillis(started time.Time) int64 {
	if started.IsZero() {
		return 0
	}
	return time.Since(started).Milliseconds()
}
