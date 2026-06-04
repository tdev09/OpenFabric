package renameio

import (
	"fmt"
	"os"
	"path/filepath"
)

// PendingFile represents a pending file that will be atomically renamed to its destination on Commit.
type PendingFile struct {
	*os.File
	tempPath string
	destPath string
}

// TempFile creates a temporary file in the same directory as path.
func TempFile(dir, path string) (*PendingFile, error) {
	if dir == "" {
		dir = filepath.Dir(path)
	}
	// Create temp file in same directory as target to ensure atomic rename on same filesystem.
	tmp, err := os.CreateTemp(dir, ".tmp-renameio-*")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	return &PendingFile{
		File:     tmp,
		tempPath: tmp.Name(),
		destPath: path,
	}, nil
}

// Cleanup closes the file and removes the temporary file.
func (t *PendingFile) Cleanup() error {
	_ = t.Close()
	return os.Remove(t.tempPath)
}

// Commit syncs the file, closes it, and atomically renames/replaces the destination.
func (t *PendingFile) Commit() error {
	if err := t.Sync(); err != nil {
		_ = t.Cleanup()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := t.Close(); err != nil {
		_ = t.Cleanup()
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := renameFile(t.tempPath, t.destPath); err != nil {
		_ = t.Cleanup()
		return fmt.Errorf("rename to destination: %w", err)
	}
	return nil
}

// CloseAtomicallyReplace syncs, closes, and renames the file to the destination path.
func (t *PendingFile) CloseAtomicallyReplace() error {
	return t.Commit()
}
