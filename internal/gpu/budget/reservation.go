package budget

import (
	"strings"
	"time"
)

// ReservationID is a unique identifier for a VRAM reservation.
type ReservationID string

// ReservationStatus tracks the lifecycle of a VRAM reservation.
type ReservationStatus string

const (
	StatusPending  ReservationStatus = "pending"  // reserved, task not yet started
	StatusActive   ReservationStatus = "active"   // task running, VRAM in use
	StatusReleased ReservationStatus = "released" // task done, VRAM freed
	StatusExpired  ReservationStatus = "expired"  // reservation timed out (never claimed)
	StatusEvicted  ReservationStatus = "evicted"  // forcibly freed by preemption
)

// Reservation holds a VRAM allocation for a single GPU task.
type Reservation struct {
	ID            ReservationID     `json:"id"`
	DeviceIndex   int               `json:"device_index"`
	BytesReserved int64             `json:"bytes_reserved"`
	TaskID        string            `json:"task_id"`
	TaskType      string            `json:"task_type"` // "llm", "image_gen", "embed"
	Priority      int               `json:"priority"`  // higher = less likely to be preempted
	Status        ReservationStatus `json:"status"`
	CreatedAt     time.Time         `json:"created_at"`
	ActivatedAt   time.Time         `json:"activated_at"`
	ReleasedAt    time.Time         `json:"released_at"`
	ExpiresAt     time.Time         `json:"expires_at"` // auto-release if never activated
}

// VRAMEstimator computes VRAM requirements for common task types.
type VRAMEstimator struct{}

// ForLLM estimates VRAM needed for an LLM inference task.
// Applies the 1.35x production headroom multiplier.
func (e *VRAMEstimator) ForLLM(modelSizeBytes int64) int64 {
	// Model weights + KV cache + activation memory + framework overhead
	// 1.35x is the safe production multiplier per 2026 benchmarks
	return int64(float64(modelSizeBytes) * 1.35)
}

// ForImageGen estimates VRAM for an image generation task.
// SDXL at 1024×1024: ~6GB. FLUX.1: ~12GB.
func (e *VRAMEstimator) ForImageGen(modelName string, width, height int) int64 {
	GB := int64(1024 * 1024 * 1024)
	pixels := int64(width * height)

	switch {
	case strings.Contains(strings.ToLower(modelName), "flux"):
		return 12 * GB
	case strings.Contains(strings.ToLower(modelName), "sdxl"):
		if pixels >= 1024*1024 {
			return 8 * GB
		}
		return 6 * GB
	case strings.Contains(strings.ToLower(modelName), "sd3"):
		return 8 * GB
	default:
		return 6 * GB // conservative default
	}
}

// ForEmbedding estimates VRAM for an embedding model task.
func (e *VRAMEstimator) ForEmbedding() int64 {
	return 512 * 1024 * 1024 // ~512MB for nomic-embed-text
}
