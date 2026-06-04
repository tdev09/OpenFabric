package wal

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

// PendingEntry represents an operation that started but never committed.
type PendingEntry struct {
	Entry   Entry
	Payload json.RawMessage
}

// RecoverPending reads the WAL and returns all entries that are
// StatusPending without a corresponding StatusCommitted or StatusAborted entry.
// These are operations interrupted by a crash that need to be
// retried or marked as failed on startup.
func RecoverPending(walPath string) ([]PendingEntry, error) {
	f, err := os.Open(walPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no WAL = no pending operations
		}
		return nil, fmt.Errorf("wal: open for recovery: %w", err)
	}
	defer f.Close()

	// Two-pass approach:
	// Pass 1: collect all LSNs that have been committed or aborted
	// Pass 2: return pending entries with no corresponding commit/abort

	committed := make(map[uint64]bool)
	var allEntries []Entry

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var entry Entry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue // skip corrupted entries
		}

		// Verify integrity
		expected := computeChecksum(&entry)
		if entry.Checksum != expected {
			continue // skip corrupted entries
		}

		allEntries = append(allEntries, entry)

		// Track commits and aborts
		if entry.Status == StatusCommitted || entry.Status == StatusAborted {
			// Extract the LSN that was committed/aborted from payload
			var commitPayload struct {
				CommittedLSN uint64 `json:"committed_lsn"`
				AbortedLSN   uint64 `json:"aborted_lsn"`
				Reason       string `json:"reason,omitempty"`
			}
			if err := json.Unmarshal(entry.Payload, &commitPayload); err == nil {
				if commitPayload.CommittedLSN != 0 {
					committed[commitPayload.CommittedLSN] = true
				}
				if commitPayload.AbortedLSN != 0 {
					committed[commitPayload.AbortedLSN] = true
				}
			}
		}
	}

	// Collect pending entries that were never committed or aborted
	var pending []PendingEntry
	for _, entry := range allEntries {
		if entry.Status == StatusPending && !committed[entry.LSN] {
			pending = append(pending, PendingEntry{
				Entry:   entry,
				Payload: entry.Payload,
			})
		}
	}

	return pending, scanner.Err()
}
