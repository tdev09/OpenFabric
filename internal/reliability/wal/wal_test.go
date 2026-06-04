package wal

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWAL_AppendAndRecoverPending(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir)
	require.NoError(t, err)

	// Append a task start entry
	lsn, err := w.Append(EntryTaskStart, "task-1",
		TaskPayload{Command: "echo hello"})
	require.NoError(t, err)
	assert.Greater(t, lsn, uint64(0))

	// Close without committing - simulates crash
	w.Close()

	// Recover - should find the pending entry
	pending, err := RecoverPending(filepath.Join(dir, walFileName))
	require.NoError(t, err)
	assert.Len(t, pending, 1)
	assert.Equal(t, "task-1", pending[0].Entry.EntityID)
	assert.Equal(t, EntryTaskStart, pending[0].Entry.Type)
}

func TestWAL_CommittedNotRecovered(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir)
	require.NoError(t, err)

	lsn, err := w.Append(EntryTaskStart, "task-2",
		TaskPayload{Command: "date"})
	require.NoError(t, err)

	err = w.Commit(lsn, "task-2")
	require.NoError(t, err)

	w.Close()

	pending, err := RecoverPending(filepath.Join(dir, walFileName))
	require.NoError(t, err)
	assert.Empty(t, pending, "committed entry should not appear in recovery")
}

func TestWAL_ChecksumVerification(t *testing.T) {
	dir := t.TempDir()
	walPath := filepath.Join(dir, walFileName)

	// Write a valid WAL entry
	w, err := Open(dir)
	require.NoError(t, err)
	_, err = w.Append(EntryTaskStart, "task-3", TaskPayload{Command: "ls"})
	require.NoError(t, err)
	w.Close()

	// Corrupt the WAL file (flip a byte in the middle of payload or struct serialization)
	data, err := os.ReadFile(walPath)
	require.NoError(t, err)
	if len(data) > 10 {
		data[len(data)/2] ^= 0xFF
	}
	err = os.WriteFile(walPath, data, 0600)
	require.NoError(t, err)

	// Recovery should skip the corrupted entry
	pending, err := RecoverPending(walPath)
	require.NoError(t, err)
	assert.Empty(t, pending, "corrupted entry should be skipped")
}

func TestWAL_MonotonicLSN(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir)
	require.NoError(t, err)
	defer w.Close()

	lsn1, err := w.Append(EntryTaskStart, "t1", nil)
	require.NoError(t, err)
	lsn2, err := w.Append(EntryTaskStart, "t2", nil)
	require.NoError(t, err)
	lsn3, err := w.Append(EntryTaskStart, "t3", nil)
	require.NoError(t, err)

	assert.Less(t, lsn1, lsn2)
	assert.Less(t, lsn2, lsn3)
}

func TestWAL_ReopenContinuesLSN(t *testing.T) {
	dir := t.TempDir()

	w1, err := Open(dir)
	require.NoError(t, err)
	lsn1, err := w1.Append(EntryTaskStart, "t1", nil)
	require.NoError(t, err)
	w1.Close()

	// Reopen - LSN should continue from where it left off
	w2, err := Open(dir)
	require.NoError(t, err)
	lsn2, err := w2.Append(EntryTaskStart, "t2", nil)
	require.NoError(t, err)
	w2.Close()

	assert.Greater(t, lsn2, lsn1,
		"LSN after reopen must be greater than last LSN before close")
}
