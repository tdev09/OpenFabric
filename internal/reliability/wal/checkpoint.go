package wal

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/openfabric/openfabric/internal/reliability/observe"
)

// checkpoint compacts the WAL by removing committed and aborted entries.
// Keeps only pending entries and writes a fresh WAL file.
// Called asynchronously - never blocks the caller.
func (w *WAL) checkpoint() {
	w.mu.Lock()
	defer w.mu.Unlock()

	dir := filepath.Dir(w.path)
	tmpPath := w.path + ".tmp"

	// Read current WAL
	pending, err := RecoverPending(w.path)
	if err != nil {
		return // checkpoint failed - WAL still valid, try again next time
	}

	// Write pending-only entries to a new file
	tmp, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return
	}

	writer := bufio.NewWriter(tmp)
	for _, p := range pending {
		data, err := json.Marshal(p.Entry)
		if err != nil {
			tmp.Close()
			os.Remove(tmpPath)
			return
		}
		_, _ = writer.Write(data)
		_ = writer.WriteByte('\n')
	}

	if err := writer.Flush(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return
	}
	tmp.Close()

	// Atomic replace: rename tmp → wal
	// On POSIX systems, rename is atomic
	if err := os.Rename(tmpPath, w.path); err != nil {
		os.Remove(tmpPath)
		return
	}

	// Re-open the compacted WAL for appending
	newFile, err := os.OpenFile(w.path, os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return
	}

	if w.file != nil {
		w.file.Close()
	}
	w.file = newFile
	w.writer = bufio.NewWriterSize(newFile, 64*1024)

	observe.Metrics.WALCheckpoints.Add(1)

	_ = dir // suppress unused warning
}
