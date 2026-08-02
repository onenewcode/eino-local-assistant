package tools

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// stageSandboxWorker copies a workspace-resident worker to a host-private
// directory before a model can modify it. Workspace hard-link aliases are
// rejected by policy normalization; an external worker is already outside the
// model's writable view and remains pinned by its resolved path. Missing or
// invalid test seams stay unstaged so command construction reports their normal
// fail-closed error at execution time.
func stageSandboxWorker(raw, workspaceRoot string) (string, func() error, error) {
	worker := strings.TrimSpace(raw)
	if worker == "" {
		return "", nil, errors.New("sandbox worker path is required")
	}
	abs, err := filepath.Abs(worker)
	if err != nil {
		return "", nil, fmt.Errorf("resolve sandbox worker path: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return filepath.Clean(abs), nil, nil
		}
		return "", nil, fmt.Errorf("resolve sandbox worker path: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", nil, fmt.Errorf("stat sandbox worker path: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return filepath.Clean(resolved), nil, nil
	}
	if !PathWithinWorkspace(workspaceRoot, resolved) {
		return filepath.Clean(resolved), nil, nil
	}

	stageDir, err := os.MkdirTemp("", "eino-assistant-sandbox-worker-*")
	if err != nil {
		return "", nil, fmt.Errorf("create sandbox worker staging directory: %w", err)
	}
	cleanup := func() error { return os.RemoveAll(stageDir) }
	resolvedStageDir, err := filepath.EvalSymlinks(stageDir)
	if err != nil {
		_ = cleanup()
		return "", nil, fmt.Errorf("resolve sandbox worker staging directory: %w", err)
	}
	if PathWithinWorkspace(workspaceRoot, resolvedStageDir) {
		_ = cleanup()
		return "", nil, errors.New("sandbox worker staging directory must be outside the workspace")
	}

	source, err := os.Open(resolved)
	if err != nil {
		_ = cleanup()
		return "", nil, fmt.Errorf("open sandbox worker for staging: %w", err)
	}
	defer source.Close()

	staged := filepath.Join(stageDir, "worker")
	destination, err := os.OpenFile(staged, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		_ = cleanup()
		return "", nil, fmt.Errorf("create staged sandbox worker: %w", err)
	}
	if _, err := io.Copy(destination, source); err != nil {
		_ = destination.Close()
		_ = cleanup()
		return "", nil, fmt.Errorf("copy staged sandbox worker: %w", err)
	}
	if err := destination.Sync(); err != nil {
		_ = destination.Close()
		_ = cleanup()
		return "", nil, fmt.Errorf("sync staged sandbox worker: %w", err)
	}
	if err := destination.Close(); err != nil {
		_ = cleanup()
		return "", nil, fmt.Errorf("close staged sandbox worker: %w", err)
	}
	if err := os.Chmod(staged, 0o700); err != nil {
		_ = cleanup()
		return "", nil, fmt.Errorf("set staged sandbox worker permissions: %w", err)
	}
	return staged, cleanup, nil
}
