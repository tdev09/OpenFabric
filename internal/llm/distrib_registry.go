package llm

import (
	"sync"
	"time"
)

// WorkerCapability describes what a remote peer can serve for distributed inference.
// Populated from the worker_capability gossip message and refreshed every 30 seconds.
type WorkerCapability struct {
	NodeID          string             `json:"node_id"`
	NodeName        string             `json:"node_name"`
	Models          []string           `json:"models"`       // Ollama model tags present locally
	FreeRAM         int64              `json:"free_ram"`     // bytes
	OllamaReady     bool               `json:"ollama_ready"` // Ollama reachable on peer
	LinkLatencies   map[string]float64 `json:"link_latencies,omitempty"`
	LinkBandwidths  map[string]float64 `json:"link_bandwidths,omitempty"`
	InferenceSpeeds map[string]float64 `json:"inference_speeds,omitempty"`
	LastSeen        time.Time          `json:"last_seen"`

	WhisperReady  bool `json:"whisper_ready,omitempty"`
	ImageGenReady bool `json:"image_gen_ready,omitempty"`
}

// capabilityTTL is how long a capability entry is considered fresh.
const capabilityTTL = 2 * time.Minute

// WorkerRegistry is a thread-safe store of per-peer worker capabilities,
// updated by the gossip layer and consulted by the coordinator before routing.
type WorkerRegistry struct {
	mu    sync.RWMutex
	peers map[string]*WorkerCapability // nodeID → capability
}

// NewWorkerRegistry creates an empty registry.
func NewWorkerRegistry() *WorkerRegistry {
	return &WorkerRegistry{peers: make(map[string]*WorkerCapability)}
}

// Update inserts or replaces the capability record for a peer.
func (r *WorkerRegistry) Update(cap WorkerCapability) {
	cap.LastSeen = time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.peers[cap.NodeID] = &cap
}

// Remove deletes a peer's capability record (e.g. on disconnect).
func (r *WorkerRegistry) Remove(nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.peers, nodeID)
}

// CanServe returns true if the given nodeID has the given model downloaded,
// enough free RAM to run it, and was seen recently.
func (r *WorkerRegistry) CanServe(nodeID, model string, requiredRAM int64) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cap, ok := r.peers[nodeID]
	if !ok {
		return false
	}
	if time.Since(cap.LastSeen) > capabilityTTL {
		return false // stale - treat as unavailable
	}
	if !cap.OllamaReady {
		return false
	}
	if cap.FreeRAM < requiredRAM {
		return false
	}
	for _, m := range cap.Models {
		if m == model {
			return true
		}
		// Also accept prefix-matched variants (e.g. "llama3:8b" matches "llama3:8b-q4_0")
		if len(m) > len(model) && m[:len(model)] == model && m[len(model)] == '-' {
			return true
		}
	}
	return false
}

// BestWorker returns the nodeID of the peer best suited to serve the model -
// i.e. has the model downloaded, most free RAM, and a fresh capability record.
// Returns ("", false) if no eligible peer is found.
func (r *WorkerRegistry) BestWorker(model string, requiredRAM int64, excludeNodeID string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var bestID string
	var bestRAM int64 = -1

	for id, cap := range r.peers {
		if id == excludeNodeID {
			continue // skip self - coordinator handles local fallback
		}
		if time.Since(cap.LastSeen) > capabilityTTL {
			continue
		}
		if !cap.OllamaReady || cap.FreeRAM < requiredRAM {
			continue
		}
		hasModel := false
		for _, m := range cap.Models {
			if m == model || (len(m) > len(model) && m[:len(model)] == model && m[len(model)] == '-') {
				hasModel = true
				break
			}
		}
		if !hasModel {
			continue
		}
		if cap.FreeRAM > bestRAM {
			bestRAM = cap.FreeRAM
			bestID = id
		}
	}

	return bestID, bestID != ""
}

// All returns a snapshot of all capability records (for the /api/llm/inference/capabilities endpoint).
func (r *WorkerRegistry) All() []WorkerCapability {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]WorkerCapability, 0, len(r.peers))
	for _, cap := range r.peers {
		out = append(out, *cap)
	}
	return out
}
