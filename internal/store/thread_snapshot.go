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

// SnapshotThread copies one locked thread into another store. The source
// journal is authoritative; immutable support files are copied for replay.
// Source locks are deliberately not copied.
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
	if ctx == nil {
		ctx = context.Background()
	}

	sourceDir, err := s.threadDir(id)
	if err != nil {
		return err
	}
	destinationDir, err := destination.threadDir(id)
	if err != nil {
		return err
	}
	if snapshotPathsOverlap(sourceDir, destinationDir) {
		return errors.New("snapshot destination overlaps source thread")
	}
	return s.withReadOnlyThread(ctx, id, func(sourceDir string, locked bool) error {
		if err := validateSnapshotJournal(sourceDir, id); err != nil {
			return err
		}
		if locked {
			return destination.snapshotThreadLocked(sourceDir, destinationDir)
		}
		return destination.snapshotThreadStable(ctx, sourceDir, destinationDir, id)
	})
}

func (s *ThreadStore) snapshotThreadStable(ctx context.Context, sourceDir, destinationDir, id string) error {
	var lastErr error
	for attempt := 0; attempt < stableReadAttempts; attempt++ {
		if err := stableReadContextError(ctx); err != nil {
			return err
		}
		before, lockPresent, err := fingerprintSnapshotSource(sourceDir)
		if err != nil {
			if !errors.Is(err, errThreadSourceChanged) {
				return err
			}
			lastErr = err
			if err := waitForStableReadRetry(ctx, attempt); err != nil {
				return err
			}
			continue
		}
		if lockPresent {
			return fmt.Errorf("%w: source lock appeared", errThreadSourceChanged)
		}
		if err := validateSnapshotJournal(sourceDir, id); err != nil {
			return err
		}

		err = s.snapshotThreadToDestination(sourceDir, destinationDir, func() error {
			after, afterLockPresent, fingerprintErr := fingerprintSnapshotSource(sourceDir)
			if fingerprintErr != nil {
				return fingerprintErr
			}
			if afterLockPresent || before != after {
				return errThreadSourceChanged
			}
			return nil
		})
		if err == nil {
			return nil
		}
		if !errors.Is(err, errThreadSourceChanged) {
			return err
		}
		lastErr = err
		if err := waitForStableReadRetry(ctx, attempt); err != nil {
			return err
		}
	}
	if lastErr == nil {
		lastErr = errThreadSourceChanged
	}
	return fmt.Errorf("snapshot source changed during stable read: %w", lastErr)
}

func validateSnapshotJournal(sourceDir, id string) error {
	journalPath := filepath.Join(sourceDir, journalFileName)
	journalInfo, err := os.Lstat(journalPath)
	if err != nil {
		return fmt.Errorf("inspect source journal: %w", err)
	}
	if err := validateSnapshotRegularFile(journalInfo, journalFileName); err != nil {
		return err
	}
	if _, _, _, err := replayJournalReadOnly(journalPath, id); err != nil {
		return fmt.Errorf("validate source thread: %w", err)
	}
	return nil
}

func (s *ThreadStore) snapshotThreadLocked(sourceDir, destinationDir string) error {
	return s.snapshotThreadToDestination(sourceDir, destinationDir, nil)
}

func (s *ThreadStore) snapshotThreadToDestination(sourceDir, destinationDir string, validateSource func() error) error {
	parent := filepath.Dir(destinationDir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create snapshot sessions directory: %w", err)
	}
	if _, err := os.Lstat(destinationDir); err == nil {
		return fmt.Errorf("snapshot thread %q already exists", filepath.Base(destinationDir))
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect snapshot thread: %w", err)
	}

	stagingDir, err := os.MkdirTemp(parent, ".snapshot-")
	if err != nil {
		return fmt.Errorf("create snapshot thread directory: %w", err)
	}
	if err := os.Chmod(stagingDir, 0o700); err != nil {
		_ = os.RemoveAll(stagingDir)
		return fmt.Errorf("set snapshot thread permissions: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(stagingDir)
		}
	}()

	for _, name := range []string{checkpointsDir, artifactsDir} {
		if err := os.Mkdir(filepath.Join(stagingDir, name), 0o700); err != nil {
			return fmt.Errorf("create snapshot %s directory: %w", name, err)
		}
	}
	if err := copySnapshotFileIfPresent(sourceDir, stagingDir, journalFileName, true); err != nil {
		return err
	}
	if err := copySnapshotFileIfPresent(sourceDir, stagingDir, systemPromptFileName, true); err != nil {
		return err
	}
	for _, name := range []string{checkpointsDir, artifactsDir} {
		if err := copySnapshotDirectory(filepath.Join(sourceDir, name), filepath.Join(stagingDir, name)); err != nil {
			return err
		}
	}
	if validateSource != nil {
		if err := validateSource(); err != nil {
			return err
		}
	}

	if err := os.Rename(stagingDir, destinationDir); err != nil {
		return fmt.Errorf("publish snapshot thread: %w", err)
	}
	cleanup = false
	if err := syncDirectory(parent); err != nil {
		return fmt.Errorf("sync snapshot sessions directory: %w", err)
	}
	id := filepath.Base(destinationDir)
	state, events, _, err := replayJournalReadOnly(filepath.Join(destinationDir, journalFileName), id)
	if err != nil {
		return fmt.Errorf("replay snapshot projection: %w", err)
	}
	if err := s.projectThread(destinationDir, state, events); err != nil {
		return fmt.Errorf("project snapshot: %w", err)
	}
	return nil
}

func copySnapshotFileIfPresent(sourceDir, destinationDir, name string, required bool) error {
	sourcePath := filepath.Join(sourceDir, name)
	info, err := os.Lstat(sourcePath)
	if errors.Is(err, os.ErrNotExist) && !required {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect snapshot file %q: %w", name, err)
	}
	if err := validateSnapshotRegularFile(info, name); err != nil {
		return err
	}
	if err := copySnapshotFile(sourcePath, filepath.Join(destinationDir, name), info); err != nil {
		return fmt.Errorf("copy snapshot file %q: %w", name, err)
	}
	return nil
}

func copySnapshotDirectory(sourceDir, destinationDir string) error {
	info, err := os.Lstat(sourceDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect snapshot directory %q: %w", filepath.Base(sourceDir), err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("snapshot directory %q must be a real directory", filepath.Base(sourceDir))
	}
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		return fmt.Errorf("read snapshot directory %q: %w", filepath.Base(sourceDir), err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !validSnapshotEntryName(name) {
			return fmt.Errorf("invalid snapshot entry name %q", name)
		}
		entryPath := filepath.Join(sourceDir, name)
		entryInfo, statErr := os.Lstat(entryPath)
		if statErr != nil {
			return fmt.Errorf("inspect snapshot entry %q: %w", name, statErr)
		}
		if err := validateSnapshotRegularFile(entryInfo, name); err != nil {
			return err
		}
		if err := copySnapshotFile(entryPath, filepath.Join(destinationDir, name), entryInfo); err != nil {
			return fmt.Errorf("copy snapshot entry %q: %w", name, err)
		}
	}
	return nil
}

func validateSnapshotRegularFile(info os.FileInfo, name string) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("snapshot file %q must be a regular file", name)
	}
	return nil
}

func validSnapshotEntryName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name && !strings.ContainsAny(name, `/\\`)
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

	destinationDir := filepath.Dir(destinationPath)
	temp, err := os.CreateTemp(destinationDir, ".snapshot-file-")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tempName)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := io.Copy(temp, source); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempName, destinationPath); err != nil {
		return err
	}
	cleanup = false
	return syncDirectory(destinationDir)
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
