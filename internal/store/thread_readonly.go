package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

const readLockRetryDelay = 10 * time.Millisecond

var errThreadSourceChanged = errors.New("thread source changed during read")

type sourceFingerprint [sha256.Size]byte

// withReadOnlyThread locks the journal file itself. There is no separate lock
// tree, so the session bundle has no lock-file lifecycle to clean up.
func (s *ThreadStore) withReadOnlyThread(ctx context.Context, id string, fn func(string, bool) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	path, err := s.threadJournalPath(id)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("session journal: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("session journal must be a regular file")
	}
	unlockLocal, err := s.holdLocalThreadLock(ctx, id)
	if err != nil {
		return err
	}
	defer unlockLocal()

	fileLock := flock.New(path, flock.SetFlag(os.O_RDONLY))
	locked, err := fileLock.TryRLockContext(ctx, readLockRetryDelay)
	if err != nil {
		_ = fileLock.Close()
		if ctx.Err() != nil {
			return fmt.Errorf("%w: %v", ErrThreadLocked, ctx.Err())
		}
		return fmt.Errorf("read-lock session: %w", err)
	}
	if !locked {
		_ = fileLock.Close()
		return ErrThreadLocked
	}
	defer func() {
		_ = fileLock.Unlock()
		_ = fileLock.Close()
	}()
	return fn(dirForJournal(path), true)
}

func dirForJournal(path string) string { return filepath.Dir(path) }

func stableReadContextError(ctx context.Context) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%w: %v", ErrThreadLocked, err)
		}
	}
	return nil
}

func fingerprintJournal(path string) (sourceFingerprint, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return sourceFingerprint{}, fmt.Errorf("inspect source journal: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return sourceFingerprint{}, errors.New("source journal must be a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return sourceFingerprint{}, fmt.Errorf("open source journal: %w", err)
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return sourceFingerprint{}, fmt.Errorf("read source journal: %w", err)
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, after) || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		return sourceFingerprint{}, errThreadSourceChanged
	}
	var fingerprint sourceFingerprint
	copy(fingerprint[:], hash.Sum(nil))
	return fingerprint, nil
}
