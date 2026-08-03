package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/gofrs/flock"
)

const (
	readLockRetryDelay   = 10 * time.Millisecond
	stableReadAttempts   = 3
	stableReadRetryDelay = 5 * time.Millisecond
)

var errThreadSourceChanged = errors.New("thread source changed during read")

type sourceFingerprint [sha256.Size]byte

// withReadOnlyThread takes an existing shared file lock when one is present.
// Older threads and snapshots may not have a lock file, so those are read
// through a bounded fingerprint/retry path without creating storage entries.
func (s *ThreadStore) withReadOnlyThread(ctx context.Context, id string, fn func(string, bool) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	dir, err := s.threadDir(id)
	if err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("thread directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("thread directory must be a real directory")
	}
	unlockLocal, err := s.holdLocalThreadLock(ctx, id)
	if err != nil {
		return err
	}
	defer unlockLocal()

	lockPath := filepath.Join(dir, locksDir, writeLockName)
	lockInfo, err := os.Lstat(lockPath)
	switch {
	case err == nil:
		if lockInfo.Mode()&os.ModeSymlink != 0 || !lockInfo.Mode().IsRegular() {
			return errors.New("thread read lock must be a regular file")
		}
		fileLock := flock.New(lockPath, flock.SetFlag(os.O_RDONLY))
		locked, lockErr := fileLock.TryRLockContext(ctx, readLockRetryDelay)
		if lockErr != nil {
			_ = fileLock.Close()
			if ctx.Err() != nil {
				return fmt.Errorf("%w: %v", ErrThreadLocked, ctx.Err())
			}
			return fmt.Errorf("read-lock thread: %w", lockErr)
		}
		if !locked {
			_ = fileLock.Close()
			return ErrThreadLocked
		}
		defer func() {
			_ = fileLock.Unlock()
			_ = fileLock.Close()
		}()
		return fn(dir, true)
	case errors.Is(err, os.ErrNotExist):
		return fn(dir, false)
	default:
		return fmt.Errorf("inspect thread read lock: %w", err)
	}
}

func stableReadThread(ctx context.Context, dir, id string) (ThreadState, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var lastErr error
	for attempt := 0; attempt < stableReadAttempts; attempt++ {
		if err := stableReadContextError(ctx); err != nil {
			return ThreadState{}, err
		}
		before, lockPresent, err := fingerprintJournalSource(dir)
		if err != nil {
			if !errors.Is(err, errThreadSourceChanged) {
				return ThreadState{}, err
			}
			lastErr = err
			if err := waitForStableReadRetry(ctx, attempt); err != nil {
				return ThreadState{}, err
			}
			continue
		}
		if lockPresent {
			return ThreadState{}, fmt.Errorf("%w: source lock appeared", errThreadSourceChanged)
		}

		state, _, _, readErr := replayJournalReadOnly(filepath.Join(dir, journalFileName), id)
		after, afterLockPresent, fingerprintErr := fingerprintJournalSource(dir)
		if fingerprintErr != nil {
			if !errors.Is(fingerprintErr, errThreadSourceChanged) {
				return ThreadState{}, fingerprintErr
			}
			lastErr = fingerprintErr
		} else if readErr == nil && !afterLockPresent && before == after {
			return state, nil
		} else if readErr != nil && !afterLockPresent && before == after {
			return ThreadState{}, readErr
		} else {
			lastErr = errThreadSourceChanged
		}
		if err := waitForStableReadRetry(ctx, attempt); err != nil {
			return ThreadState{}, err
		}
	}
	if lastErr == nil {
		lastErr = errThreadSourceChanged
	}
	return ThreadState{}, fmt.Errorf("%w: retry limit reached", lastErr)
}

func stableReadContextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrThreadLocked, err)
	}
	return nil
}

func waitForStableReadRetry(ctx context.Context, attempt int) error {
	if attempt+1 >= stableReadAttempts {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	timer := time.NewTimer(stableReadRetryDelay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%w: %v", ErrThreadLocked, ctx.Err())
	}
}

func fingerprintJournalSource(dir string) (sourceFingerprint, bool, error) {
	hashValue := sha256.New()
	writeFingerprintRecord(hashValue, "journal-source", "v1")
	if err := fingerprintSourceFile(hashValue, journalFileName, filepath.Join(dir, journalFileName), true); err != nil {
		return sourceFingerprint{}, false, err
	}
	lockPresent, err := fingerprintSourceLock(hashValue, filepath.Join(dir, locksDir, writeLockName))
	if err != nil {
		return sourceFingerprint{}, false, err
	}
	return fingerprintFromHash(hashValue), lockPresent, nil
}

func fingerprintSnapshotSource(dir string) (sourceFingerprint, bool, error) {
	hashValue := sha256.New()
	writeFingerprintRecord(hashValue, "snapshot-source", "v1")
	if err := fingerprintSourceFile(hashValue, journalFileName, filepath.Join(dir, journalFileName), true); err != nil {
		return sourceFingerprint{}, false, err
	}
	for _, name := range []string{stateFileName, metaFileName} {
		if err := fingerprintSourceFile(hashValue, name, filepath.Join(dir, name), false); err != nil {
			return sourceFingerprint{}, false, err
		}
	}
	for _, name := range []string{checkpointsDir, artifactsDir} {
		if err := fingerprintSnapshotDirectory(hashValue, filepath.Join(dir, name), name); err != nil {
			return sourceFingerprint{}, false, err
		}
	}
	lockPresent, err := fingerprintSourceLock(hashValue, filepath.Join(dir, locksDir, writeLockName))
	if err != nil {
		return sourceFingerprint{}, false, err
	}
	return fingerprintFromHash(hashValue), lockPresent, nil
}

func fingerprintSnapshotDirectory(hashValue hash.Hash, dir, name string) error {
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		writeFingerprintRecord(hashValue, name, "missing")
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect source snapshot directory %q: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("snapshot directory %q must be a real directory", name)
	}
	writeFileFingerprint(hashValue, name, info, "directory")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: source snapshot directory %q disappeared", errThreadSourceChanged, name)
		}
		return fmt.Errorf("read source snapshot directory %q: %w", name, err)
	}
	for _, entry := range entries {
		entryName := entry.Name()
		if !validSnapshotEntryName(entryName) {
			return fmt.Errorf("invalid snapshot entry name %q", entryName)
		}
		entryPath := filepath.Join(dir, entryName)
		if err := fingerprintSourceFile(hashValue, filepath.Join(name, entryName), entryPath, true); err != nil {
			return err
		}
	}
	return nil
}

func fingerprintSourceFile(hashValue hash.Hash, name, path string, required bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && !required {
		writeFingerprintRecord(hashValue, name, "missing")
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect source file %q: %w", name, err)
	}
	if err := validateSnapshotRegularFile(info, name); err != nil {
		return err
	}
	source, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open source file %q: %w", name, err)
	}
	openedInfo, err := source.Stat()
	if err != nil {
		_ = source.Close()
		return fmt.Errorf("stat source file %q: %w", name, err)
	}
	if !sameSourceFileInfo(info, openedInfo) {
		_ = source.Close()
		return errThreadSourceChanged
	}
	contentHash := sha256.New()
	if _, err := io.Copy(contentHash, source); err != nil {
		_ = source.Close()
		return fmt.Errorf("read source file %q: %w", name, err)
	}
	if err := source.Close(); err != nil {
		return fmt.Errorf("close source file %q: %w", name, err)
	}
	afterInfo, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errThreadSourceChanged
		}
		return fmt.Errorf("reinspect source file %q: %w", name, err)
	}
	if !sameSourceFileInfo(info, afterInfo) {
		return errThreadSourceChanged
	}
	writeFileFingerprint(hashValue, name, info, fmt.Sprintf("%x", contentHash.Sum(nil)))
	return nil
}

func fingerprintSourceLock(hashValue hash.Hash, path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		writeFingerprintRecord(hashValue, writeLockName, "missing")
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect source thread lock: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, errors.New("thread source lock must be a regular file")
	}
	writeFileFingerprint(hashValue, writeLockName, info, "lock")
	return true, nil
}

func sameSourceFileInfo(first, second os.FileInfo) bool {
	return os.SameFile(first, second) && first.Mode() == second.Mode() && first.Size() == second.Size() && first.ModTime().Equal(second.ModTime())
}

func fingerprintFromHash(hashValue hash.Hash) sourceFingerprint {
	sum := hashValue.Sum(nil)
	var fingerprint sourceFingerprint
	copy(fingerprint[:], sum)
	return fingerprint
}

func writeFileFingerprint(hashValue hash.Hash, name string, info os.FileInfo, content string) {
	writeFingerprintRecord(
		hashValue,
		name,
		"present",
		strconv.FormatUint(uint64(info.Mode()), 10),
		strconv.FormatInt(info.Size(), 10),
		strconv.FormatInt(info.ModTime().UnixNano(), 10),
		content,
	)
}

func writeFingerprintRecord(hashValue hash.Hash, values ...string) {
	for _, value := range values {
		_, _ = hashValue.Write([]byte(value))
		_, _ = hashValue.Write([]byte{0})
	}
}
