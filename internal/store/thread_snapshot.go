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

// SnapshotThread copies a locked session bundle into another store. The JSONL
// ledger is authoritative and the destination summary is rebuilt from it.
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
	destinationDir := filepath.Dir(destinationPath)
	parent := filepath.Dir(destinationDir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create snapshot sessions directory: %w", err)
	}
	if _, err := os.Lstat(destinationDir); err == nil {
		return fmt.Errorf("snapshot thread %q already exists", id)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect snapshot thread: %w", err)
	}
	tmpDir, err := os.MkdirTemp(parent, ".snapshot-")
	if err != nil {
		return fmt.Errorf("create snapshot session directory: %w", err)
	}
	if err := os.Chmod(tmpDir, 0o700); err != nil {
		_ = os.RemoveAll(tmpDir)
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(tmpDir)
		}
	}()
	tmpPath := journalPath(tmpDir, id)
	if err := copySnapshotFile(sourcePath, tmpPath, info); err != nil {
		return fmt.Errorf("copy snapshot journal: %w", err)
	}
	if err := os.Rename(tmpDir, destinationDir); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("snapshot thread %q already exists", id)
		}
		return fmt.Errorf("publish snapshot session directory: %w", err)
	}
	cleanup = false
	_ = syncDirectory(parent)
	state, events, _, err := replayJournalReadOnly(destinationPath, id)
	if err != nil {
		return fmt.Errorf("replay snapshot projection: %w", err)
	}
	return s.projectThread(destinationDir, state, events)
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
