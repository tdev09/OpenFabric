package budget

import (
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/openfabric/openfabric/internal/gpu/backend"
)

const (
	// reservationTimeout is how long a Pending reservation is kept
	// before auto-releasing if never activated.
	reservationTimeout = 60 * time.Second

	// safetyMarginBytes is always kept free regardless of calculations.
	// Prevents edge-case OOM from measurement inaccuracy.
	safetyMarginBytes = 256 * 1024 * 1024 // 256 MB
)

// Manager tracks all VRAM reservations and enforces budget limits.
// Thread-safe. One Manager per GPU device.
type Manager struct {
	mu           sync.RWMutex
	deviceIndex  int
	backend      backend.Backend
	reservations map[ReservationID]*Reservation
	estimator    VRAMEstimator
	log          *zap.Logger
}

// NewManager creates a VRAM budget manager for a single GPU device.
func NewManager(deviceIndex int, b backend.Backend, log *zap.Logger) *Manager {
	m := &Manager{
		deviceIndex:  deviceIndex,
		backend:      b,
		reservations: make(map[ReservationID]*Reservation),
		log:          log,
	}
	go m.expireLoop()
	return m
}

// Reserve attempts to allocate bytesNeeded VRAM for a task.
// Returns a Reservation if successful, error if insufficient VRAM.
// The reservation is in StatusPending until Activate() is called.
func (m *Manager) Reserve(taskID, taskType string, bytesNeeded int64, priority int) (*Reservation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Get current VRAM state
	stats, err := m.backend.VRAMStats(m.deviceIndex)
	if err != nil {
		return nil, fmt.Errorf("budget: vram stats: %w", err)
	}

	// Calculate safe available VRAM
	// Effective free minus already-pending reservations minus safety margin
	pendingReserved := m.pendingBytesLocked()
	available := stats.EffectiveFree() - pendingReserved - safetyMarginBytes

	if available < bytesNeeded {
		return nil, &InsufficientVRAMError{
			DeviceIndex:   m.deviceIndex,
			Requested:     bytesNeeded,
			Available:     available,
			TotalVRAM:     stats.Total,
			CurrentlyUsed: stats.Used + pendingReserved,
		}
	}

	r := &Reservation{
		ID:            ReservationID(uuid.New().String()),
		DeviceIndex:   m.deviceIndex,
		BytesReserved: bytesNeeded,
		TaskID:        taskID,
		TaskType:      taskType,
		Priority:      priority,
		Status:        StatusPending,
		CreatedAt:     time.Now(),
		ExpiresAt:     time.Now().Add(reservationTimeout),
	}

	m.reservations[r.ID] = r

	m.log.Debug("vram reserved",
		zap.String("task_id", taskID),
		zap.String("reservation_id", string(r.ID)),
		zap.Int64("bytes_reserved", bytesNeeded),
		zap.Int64("bytes_available_after", available-bytesNeeded),
	)

	return r, nil
}

// Activate transitions a Pending reservation to Active.
// Must be called just before the GPU task process starts.
func (m *Manager) Activate(id ReservationID) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	r, ok := m.reservations[id]
	if !ok {
		return fmt.Errorf("budget: reservation %s not found", id)
	}
	if r.Status != StatusPending {
		return fmt.Errorf("budget: reservation %s is %s, not pending", id, r.Status)
	}

	r.Status = StatusActive
	r.ActivatedAt = time.Now()
	return nil
}

// Release frees a reservation when a task completes.
// Safe to call multiple times - idempotent.
func (m *Manager) Release(id ReservationID) {
	m.mu.Lock()
	defer m.mu.Unlock()

	r, ok := m.reservations[id]
	if !ok {
		return
	}
	if r.Status == StatusReleased || r.Status == StatusExpired {
		return
	}

	r.Status = StatusReleased
	r.ReleasedAt = time.Now()

	m.log.Debug("vram reservation released",
		zap.String("task_id", r.TaskID),
		zap.String("reservation_id", string(id)),
		zap.Int64("bytes_freed", r.BytesReserved),
	)
}

// LowestPriorityActive returns the active reservation with the lowest
// priority - the first candidate for preemption.
func (m *Manager) LowestPriorityActive() *Reservation {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var lowest *Reservation
	for _, r := range m.reservations {
		if r.Status != StatusActive {
			continue
		}
		if lowest == nil || r.Priority < lowest.Priority {
			lowest = r
		}
	}
	return lowest
}

// Stats returns a snapshot of current reservation state.
func (m *Manager) Stats() BudgetStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var pending, active int64
	activeCount, pendingCount := 0, 0
	for _, r := range m.reservations {
		switch r.Status {
		case StatusPending:
			pending += r.BytesReserved
			pendingCount++
		case StatusActive:
			active += r.BytesReserved
			activeCount++
		}
	}

	return BudgetStats{
		ActiveReservations:  activeCount,
		PendingReservations: pendingCount,
		ActiveBytes:         active,
		PendingBytes:        pending,
	}
}

// pendingBytesLocked returns total bytes in pending and active reservations.
// Must be called with m.mu held.
func (m *Manager) pendingBytesLocked() int64 {
	var total int64
	for _, r := range m.reservations {
		if r.Status == StatusPending || r.Status == StatusActive {
			total += r.BytesReserved
		}
	}
	return total
}

// expireLoop removes timed-out pending reservations.
func (m *Manager) expireLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		m.expireOldReservations()
	}
}

func (m *Manager) expireOldReservations() {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for id, r := range m.reservations {
		if r.Status == StatusPending && now.After(r.ExpiresAt) {
			r.Status = StatusExpired
			m.log.Warn("vram reservation expired without activation",
				zap.String("task_id", r.TaskID),
				zap.String("reservation_id", string(id)),
			)
		}
	}
}

// BudgetStats is a snapshot of VRAM reservation state.
type BudgetStats struct {
	ActiveReservations  int   `json:"active_reservations"`
	PendingReservations int   `json:"pending_reservations"`
	ActiveBytes         int64 `json:"active_bytes"`
	PendingBytes        int64 `json:"pending_bytes"`
}

// InsufficientVRAMError provides detailed VRAM shortage information.
type InsufficientVRAMError struct {
	DeviceIndex   int
	Requested     int64
	Available     int64
	TotalVRAM     int64
	CurrentlyUsed int64
}

func (e *InsufficientVRAMError) Error() string {
	return fmt.Sprintf(
		"insufficient VRAM on GPU %d: requested %.1f GB, available %.1f GB "+
			"(total %.1f GB, in use %.1f GB)",
		e.DeviceIndex,
		float64(e.Requested)/(1024*1024*1024),
		float64(e.Available)/(1024*1024*1024),
		float64(e.TotalVRAM)/(1024*1024*1024),
		float64(e.CurrentlyUsed)/(1024*1024*1024),
	)
}

// UserMessage returns the plain English version for dashboard display.
func (e *InsufficientVRAMError) UserMessage() string {
	return fmt.Sprintf(
		"Not enough GPU memory for this task. Your GPU has %.1f GB free but needs %.1f GB. "+
			"Try a smaller model or wait for other tasks to complete.",
		float64(e.Available)/(1024*1024*1024),
		float64(e.Requested)/(1024*1024*1024),
	)
}
