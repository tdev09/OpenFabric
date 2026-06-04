package wal

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openfabric/openfabric/internal/reliability/observe"
)

const (
	// walFileName is the active WAL file.
	walFileName = "openfabric.wal"
	// checkpointInterval triggers compaction every N committed entries.
	checkpointInterval = 1000
)

// WAL is an append-only write-ahead log.
// All writes are fsync'd for durability. Thread-safe.
type WAL struct {
	mu         sync.Mutex
	file       *os.File
	writer     *bufio.Writer
	lsn        atomic.Uint64 // monotonically increasing
	committedN atomic.Uint64 // entries since last checkpoint
	path       string
}

// Open opens or creates the WAL at the given directory.
// If a WAL already exists, it is opened for append.
func Open(dir string) (*WAL, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("wal: create directory: %w", err)
	}

	path := filepath.Join(dir, walFileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return nil, fmt.Errorf("wal: open file: %w", err)
	}

	w := &WAL{
		file:   f,
		writer: bufio.NewWriterSize(f, 64*1024), // 64KB write buffer
		path:   path,
	}

	// Recover LSN from last entry in existing WAL
	if err := w.recoverLSN(); err != nil {
		f.Close()
		return nil, fmt.Errorf("wal: recover LSN: %w", err)
	}

	return w, nil
}

// Append writes a new entry to the WAL with StatusPending.
// Returns the LSN assigned to this entry.
// The write is fsync'd before returning - caller can proceed knowing
// the entry is durable on disk.
func (w *WAL) Append(entryType EntryType, entityID string, payload any) (uint64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	lsn := w.lsn.Add(1)

	var payloadBytes []byte
	if payload != nil {
		var err error
		payloadBytes, err = json.Marshal(payload)
		if err != nil {
			return 0, fmt.Errorf("wal: marshal payload: %w", err)
		}
	}

	entry := &Entry{
		LSN:       lsn,
		Type:      entryType,
		Status:    StatusPending,
		Timestamp: time.Now().UTC(),
		EntityID:  entityID,
		Payload:   payloadBytes,
	}
	entry.Checksum = computeChecksum(entry)

	if err := w.writeEntry(entry); err != nil {
		return 0, err
	}

	return lsn, nil
}

// Commit marks an entry as successfully completed.
// Must be called after the operation succeeds.
func (w *WAL) Commit(lsn uint64, entityID string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	entry := &Entry{
		LSN:       w.lsn.Add(1),
		Type:      EntryCheckpoint, // reuse as commit marker
		Status:    StatusCommitted,
		Timestamp: time.Now().UTC(),
		EntityID:  entityID,
		Payload:   json.RawMessage(fmt.Sprintf(`{"committed_lsn":%d}`, lsn)),
	}
	entry.Checksum = computeChecksum(entry)

	if err := w.writeEntry(entry); err != nil {
		return err
	}

	// Trigger checkpoint if needed
	if w.committedN.Add(1) >= checkpointInterval {
		w.committedN.Store(0)
		go w.checkpoint() // async - does not block the caller
	}

	return nil
}

// Abort marks an entry as failed/aborted.
func (w *WAL) Abort(lsn uint64, entityID string, reason string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	entry := &Entry{
		LSN:       w.lsn.Add(1),
		Type:      EntryTaskFail,
		Status:    StatusAborted,
		Timestamp: time.Now().UTC(),
		EntityID:  entityID,
		Payload:   json.RawMessage(fmt.Sprintf(`{"aborted_lsn":%d,"reason":%q}`, lsn, reason)),
	}
	entry.Checksum = computeChecksum(entry)

	return w.writeEntry(entry)
}

// writeEntry serializes an entry and writes + fsyncs to disk.
// Must be called with w.mu held.
func (w *WAL) writeEntry(entry *Entry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("wal: marshal entry: %w", err)
	}

	// Append newline delimiter
	data = append(data, '\n')

	if _, err := w.writer.Write(data); err != nil {
		return fmt.Errorf("wal: write: %w", err)
	}

	// Flush buffer to OS
	if err := w.writer.Flush(); err != nil {
		return fmt.Errorf("wal: flush: %w", err)
	}

	// fsync: guarantee durability on power loss
	// This is the critical reliability guarantee - without fsync,
	// the OS may buffer writes and lose them on crash
	if err := w.file.Sync(); err != nil {
		return fmt.Errorf("wal: fsync: %w", err)
	}

	observe.Metrics.WALEntries.Add(1)

	return nil
}

// recoverLSN reads the WAL to find the highest LSN.
// Called on Open() to resume from the correct sequence number.
func (w *WAL) recoverLSN() error {
	// Read from beginning
	if _, err := w.file.Seek(0, 0); err != nil {
		return err
	}

	var maxLSN uint64
	scanner := bufio.NewScanner(w.file)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB max line

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var entry Entry
		if err := json.Unmarshal(line, &entry); err != nil {
			// Skip corrupted entries - log them but continue
			continue
		}

		// Verify checksum
		expected := computeChecksum(&entry)
		if entry.Checksum != expected {
			// Corrupted entry - skip
			continue
		}

		if entry.LSN > maxLSN {
			maxLSN = entry.LSN
		}
	}

	w.lsn.Store(maxLSN)

	// Seek back to end for appending
	_, err := w.file.Seek(0, 2)
	return err
}

// Close flushes and closes the WAL.
func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.writer != nil {
		_ = w.writer.Flush()
	}
	if w.file != nil {
		_ = w.file.Sync()
		return w.file.Close()
	}
	return nil
}
