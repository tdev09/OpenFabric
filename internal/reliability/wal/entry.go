package wal

import (
	"encoding/json"
	"hash/crc32"
	"time"
)

// EntryType identifies the kind of operation being logged.
type EntryType string

const (
	// EntryTaskSubmit is written when a task enters the scheduler queue.
	EntryTaskSubmit EntryType = "task_submit"
	// EntryTaskStart is written when a task begins executing on a node.
	EntryTaskStart EntryType = "task_start"
	// EntryTaskComplete is written when a task finishes successfully.
	EntryTaskComplete EntryType = "task_complete"
	// EntryTaskFail is written when a task fails or is cancelled.
	EntryTaskFail EntryType = "task_fail"
	// EntryStorageWrite is written before any file write to shared storage.
	EntryStorageWrite EntryType = "storage_write"
	// EntryStorageDelete is written before any file deletion.
	EntryStorageDelete EntryType = "storage_delete"
	// EntryFlowRunStart is written when a flow execution begins.
	EntryFlowRunStart EntryType = "flow_run_start"
	// EntryFlowStepComplete is written after each flow step completes.
	EntryFlowStepComplete EntryType = "flow_step_complete"
	// EntryFlowRunComplete is written when a flow run finishes.
	EntryFlowRunComplete EntryType = "flow_run_complete"
	// EntryAgentStart is written when an autonomous agent begins.
	EntryAgentStart EntryType = "agent_start"
	// EntryAgentStepComplete is written after each agent ReAct step.
	EntryAgentStepComplete EntryType = "agent_step_complete"
	// EntryCheckpoint marks a WAL compaction point.
	EntryCheckpoint EntryType = "checkpoint"
)

// EntryStatus tracks whether an operation has committed.
type EntryStatus string

const (
	// StatusPending means the operation was logged but not yet completed.
	StatusPending EntryStatus = "pending"
	// StatusCommitted means the operation completed successfully.
	StatusCommitted EntryStatus = "committed"
	// StatusAborted means the operation failed or was rolled back.
	StatusAborted EntryStatus = "aborted"
)

// Entry is a single WAL record. Serialized as newline-delimited JSON.
type Entry struct {
	// LSN is the Log Sequence Number - monotonically increasing, never reused.
	LSN       uint64      `json:"lsn"`
	Type      EntryType   `json:"type"`
	Status    EntryStatus `json:"status"`
	Timestamp time.Time   `json:"ts"`
	// EntityID is the ID of the entity this entry relates to
	// (task ID, file path, flow run ID, agent ID).
	EntityID string `json:"entity_id"`
	// Payload is operation-specific data.
	Payload json.RawMessage `json:"payload,omitempty"`
	// Checksum is CRC32 of all other fields for integrity verification.
	Checksum uint32 `json:"checksum"`
}

// TaskPayload is the payload for task-related WAL entries.
type TaskPayload struct {
	Command  string `json:"command"`
	NodeID   string `json:"node_id,omitempty"`
	Priority int    `json:"priority"`
	Error    string `json:"error,omitempty"`
}

// StoragePayload is the payload for storage write/delete entries.
type StoragePayload struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
	Checksum  string `json:"checksum,omitempty"` // SHA256 of file content
}

// FlowPayload is the payload for flow execution entries.
type FlowPayload struct {
	FlowID   string `json:"flow_id"`
	RunID    string `json:"run_id"`
	StepID   string `json:"step_id,omitempty"`
	StepType string `json:"step_type,omitempty"`
	Error    string `json:"error,omitempty"`
}

// computeChecksum computes CRC32 of all entry fields except Checksum.
func computeChecksum(e *Entry) uint32 {
	// Zero the checksum field before computing
	saved := e.Checksum
	e.Checksum = 0
	data, _ := json.Marshal(e)
	e.Checksum = saved
	return crc32.ChecksumIEEE(data)
}
