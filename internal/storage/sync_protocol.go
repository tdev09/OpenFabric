package storage

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/openfabric/openfabric/internal/policy"
)

// SyncSettings is the serializable sync payload for Settings,
// excluding node-specific properties like DeviceName and APIPort.
type SyncSettings struct {
	ClusterName        string          `json:"cluster_name"`
	AllowedCommands    []string        `json:"allowed_commands"`
	SandboxMode        bool            `json:"sandbox_mode"`
	MaxTaskMemoryMB    int             `json:"max_task_memory_mb"`
	MaxTaskProcs       int             `json:"max_task_procs"`
	MaxTaskFileSizeMB  int             `json:"max_task_file_size_mb"`
	Policies           []policy.Policy `json:"policies,omitempty"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

// SyncRequest is sent to a peer to initiate state synchronization.
type SyncRequest struct {
	NodeID    string        `json:"node_id"`
	Entries   []MetaEntry   `json:"entries"`
	Settings  *SyncSettings `json:"settings,omitempty"`
	Timestamp time.Time     `json:"timestamp"`
}

// SyncResponse is returned by the peer with resolved differences.
type SyncResponse struct {
	NodeID    string        `json:"node_id"`
	Entries   []MetaEntry   `json:"entries"` // newer/missing entries for the requester
	Settings  *SyncSettings `json:"settings,omitempty"`
	Timestamp time.Time     `json:"timestamp"`
}

// SettingsSyncInterface decouples storage package from api package to avoid circular imports.
type SettingsSyncInterface interface {
	GetSyncableSettings() SyncSettings
	ApplySyncableSettings(incoming SyncSettings) bool
}

// MergeRemoteRegistry merges peer CRDT entries with local ones using LWW logic.
// Returns:
//   - updatedLocally: entries from peer that were applied locally because they were newer.
//   - peerNeeds: local entries that are newer or missing from the peer's list.
func (s *Store) MergeRemoteRegistry(remoteEntries []MetaEntry) (updatedLocally []MetaEntry, peerNeeds []MetaEntry) {
	localMap := s.crdt.GetEntriesMap()
	remoteMap := make(map[string]MetaEntry)

	for _, remote := range remoteEntries {
		// Security: clean and validate remote paths before processing
		if strings.Contains(remote.Path, "..") || filepath.IsAbs(remote.Path) {
			continue // skip traversal attempts
		}
		remoteMap[remote.Path] = remote

		local, exists := localMap[remote.Path]
		if !exists {
			// Local is missing this file metadata. Apply it.
			s.crdt.Upsert(remote)
			updatedLocally = append(updatedLocally, remote)

			// If it's a remote tombstone and we have a local file, remove it
			if remote.Tombstone {
				_ = s.DeleteLocal(remote.Path)
			} else {
				s.RegisterFileAvailability(remote.Path, remote.NodeID, remote.Size, "")
			}
		} else if remote.UpdatedAt.After(local.UpdatedAt) {
			// Remote is newer. Apply it.
			s.crdt.Upsert(remote)
			updatedLocally = append(updatedLocally, remote)

			// If peer has a newer tombstone, remove local file
			if remote.Tombstone {
				_ = s.DeleteLocal(remote.Path)
			} else {
				s.RegisterFileAvailability(remote.Path, remote.NodeID, remote.Size, "")
			}
		} else if local.UpdatedAt.After(remote.UpdatedAt) {
			// Local is newer. Peer needs our version.
			peerNeeds = append(peerNeeds, local)
		}
	}

	// Find any files that local has but remote does not know about at all
	for path, local := range localMap {
		if _, exists := remoteMap[path]; !exists {
			peerNeeds = append(peerNeeds, local)
		}
	}

	return updatedLocally, peerNeeds
}
