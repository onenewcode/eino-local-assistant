package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// SnapshotThread copies one locked session journal into another store. The
// JSONL ledger is authoritative and the destination SQLite projection is rebuilt from it.
func (s *ThreadStore) SnapshotThread(ctx context.Context, id string, destination *ThreadStore) error {
	if s == nil {
		return errors.New("source thread store is required")
	}
	if destination == nil {
		return errors.New("snapshot destination thread store is required")
	}
	id = strings.TrimSpace(id)
	if err := validateThreadID(id); err != nil {
		return err
	}
	if s == destination {
		return errors.New("snapshot destination must differ from source thread store")
	}
	sourcePath, err := s.threadJournalPath(id)
	if err != nil {
		return err
	}
	relativePath, err := filepath.Rel(s.sessionsRoot(), sourcePath)
	if err != nil || relativePath == "." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) || filepath.IsAbs(relativePath) {
		return errors.New("source thread is outside the active session hierarchy")
	}
	destinationPath := filepath.Join(destination.sessionsRoot(), relativePath)
	if snapshotPathsOverlap(sourcePath, destinationPath) {
		return errors.New("snapshot destination overlaps source thread")
	}
	return s.withReadOnlyThread(ctx, id, func(sourceDir string, _ bool) error {
		return destination.snapshotJournal(journalPath(sourceDir, id), destinationPath, id)
	})
}

func (s *ThreadStore) snapshotJournal(sourcePath, destinationPath, id string) error {
	info, err := os.Lstat(sourcePath)
	if err != nil {
		return fmt.Errorf("inspect source journal: %w", err)
	}
	if err := validateSnapshotRegularFile(info, filepath.Base(sourcePath)); err != nil {
		return err
	}
	if _, _, _, err := replayJournalReadOnly(sourcePath, id); err != nil {
		return fmt.Errorf("validate source thread: %w", err)
	}
	destinationDayDir := filepath.Dir(destinationPath)
	if err := os.MkdirAll(destinationDayDir, 0o700); err != nil {
		return fmt.Errorf("create snapshot sessions directory: %w", err)
	}
	if _, err := os.Lstat(destinationPath); err == nil {
		return fmt.Errorf("snapshot thread %q already exists", id)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect snapshot thread: %w", err)
	}
	tmp, err := os.CreateTemp(destinationDayDir, ".snapshot-")
	if err != nil {
		return fmt.Errorf("create temporary snapshot journal: %w", err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	defer func() { _ = os.Remove(tmpPath) }()
	if err := copySnapshotFile(sourcePath, tmpPath, info); err != nil {
		return fmt.Errorf("copy snapshot journal: %w", err)
	}
	if err := publishNewJournal(tmpPath, destinationPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("snapshot thread %q already exists", id)
		}
		return fmt.Errorf("publish snapshot session journal: %w", err)
	}
	state, events, _, err := replayJournalReadOnly(destinationPath, id)
	if err != nil {
		return fmt.Errorf("replay snapshot projection: %w", err)
	}
	return s.projectThread(destinationDayDir, state, events)
}

func validateSnapshotRegularFile(info os.FileInfo, name string) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("snapshot file %q must be a regular file", name)
	}
	return nil
}

func copySnapshotFile(sourcePath, destinationPath string, initialInfo os.FileInfo) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	openedInfo, err := source.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(initialInfo, openedInfo) {
		return errors.New("source changed after inspection")
	}
	if !openedInfo.Mode().IsRegular() {
		return errors.New("source changed from a regular file")
	}
	destination, err := os.OpenFile(destinationPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(destination, source); err != nil {
		_ = destination.Close()
		return err
	}
	if err := destination.Sync(); err != nil {
		_ = destination.Close()
		return err
	}
	if err := destination.Close(); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(destinationPath))
}

func snapshotPathsOverlap(first, second string) bool {
	first = filepath.Clean(first)
	second = filepath.Clean(second)
	return snapshotPathWithin(first, second) || snapshotPathWithin(second, first)
}

func snapshotPathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}
