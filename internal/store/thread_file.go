package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func writeMaterializedState(dir string, state ThreadState) error {
	if err := writeJSONAtomic(filepath.Join(dir, stateFileName), state); err != nil {
		return fmt.Errorf("write thread state: %w", err)
	}
	if err := writeJSONAtomic(filepath.Join(dir, metaFileName), state.Meta); err != nil {
		return fmt.Errorf("write thread meta: %w", err)
	}
	return nil
}

func writeJSONAtomic(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode JSON: %w", err)
	}
	data = append(data, '\n')
	return writeBytesAtomic(path, data)
}

func writeBytesAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set temp file permissions: %w", err)
	}
	n, err := tmp.Write(data)
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if n != len(data) {
		_ = tmp.Close()
		return ioShortWrite("write temp file", len(data), n)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace file atomically: %w", err)
	}
	cleanup = false
	return syncDirectory(dir)
}

func readJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open directory for sync: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync directory: %w", err)
	}
	return nil
}
