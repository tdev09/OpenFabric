package recovery

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/openfabric/openfabric/internal/gpu/backend"
	"github.com/openfabric/openfabric/internal/gpu/budget"
	"github.com/openfabric/openfabric/internal/gpu/preempt"
)

// OOMEvent describes a detected out-of-memory condition.
type OOMEvent struct {
	DeviceIndex  int
	DetectedAt   time.Time
	VRAMSnapshot backend.VRAMStats
	Cause        string // best-effort diagnosis
}

// RecoveryResult describes what action was taken.
type RecoveryResult struct {
	Event       OOMEvent
	Action      string // "evicted_task", "cleared_cache", "recovery_failed"
	TaskEvicted string // task ID if a task was evicted
	Success     bool
	UserMessage string
}

// Watcher monitors for OOM conditions and triggers recovery.
type Watcher struct {
	mu          sync.Mutex
	backend     backend.Backend
	manager     *budget.Manager
	preemptor   *preempt.Preemptor
	deviceIndex int
	onOOM       func(RecoveryResult) // callback for dashboard notification
	log         *zap.Logger
}

// NewWatcher creates an OOM watcher for a single GPU device.
func NewWatcher(
	b backend.Backend,
	mgr *budget.Manager,
	pre *preempt.Preemptor,
	deviceIndex int,
	onOOM func(RecoveryResult),
	log *zap.Logger,
) *Watcher {
	return &Watcher{
		backend:     b,
		manager:     mgr,
		preemptor:   pre,
		deviceIndex: deviceIndex,
		onOOM:       onOOM,
		log:         log,
	}
}

// Start begins the OOM monitoring loop.
// Samples VRAM every 10 seconds (reduced from 2s to limit overhead on idle systems).
func (w *Watcher) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Only run OOM checks when there are active GPU tasks to avoid
				// constant noisy warnings on idle systems.
				if w.manager.Stats().ActiveBytes > 0 {
					w.Check()
				}
			}
		}
	}()
}

// Check reads VRAM stats and triggers recovery if OOM is detected.
func (w *Watcher) Check() {
	stats, err := w.backend.VRAMStats(w.deviceIndex)
	if err != nil {
		return
	}

	// OOM threshold is backend-dependent:
	//   Metal (Apple Silicon unified memory): 512 MB reclaimable - speculative+free
	//     pages are now counted correctly so this represents genuine pressure.
	//   Other backends (CUDA/ROCm): 256 MB dedicated VRAM free.
	criticalThreshold := int64(256 * 1024 * 1024) // default: 256 MB
	if w.backend.Name() == "metal" {
		criticalThreshold = 512 * 1024 * 1024 // 512 MB of reclaimable unified memory
	}

	if stats.Free > criticalThreshold {
		return // healthy
	}

	w.log.Warn("OOM condition detected",
		zap.Int("device", w.deviceIndex),
		zap.Int64("free_bytes", stats.Free),
		zap.Int64("critical_threshold", criticalThreshold),
	)

	event := OOMEvent{
		DeviceIndex:  w.deviceIndex,
		DetectedAt:   time.Now(),
		VRAMSnapshot: stats,
		Cause:        w.diagnoseCause(stats),
	}

	result := w.recover(event)

	if w.onOOM != nil {
		w.onOOM(result)
	}
}

// recover attempts to free VRAM and stabilize the GPU.
func (w *Watcher) recover(event OOMEvent) RecoveryResult {
	result := RecoveryResult{Event: event}

	// Use backend-aware required bytes: same as the detection threshold.
	requiredBytes := int64(256 * 1024 * 1024)
	if w.backend.Name() == "metal" {
		requiredBytes = 512 * 1024 * 1024
	}

	// Step 1: Try to evict the lowest-priority active task
	preemptResult, err := w.preemptor.Preempt(preempt.PreemptRequest{
		RequiredBytes:     requiredBytes,
		RequesterTaskID:   "oom-recovery",
		RequesterPriority: 999, // highest possible - OOM recovery beats everything
		DeviceIndex:       w.deviceIndex,
	})

	if err == nil {
		result.Action = "evicted_task"
		result.TaskEvicted = preemptResult.PreemptedTaskID
		result.Success = true
		result.UserMessage = fmt.Sprintf(
			"Your GPU ran out of memory. Task '%s' was paused to free space. "+
				"It will be retried automatically.",
			preemptResult.PreemptedTaskID,
		)

		w.log.Info("OOM recovery: evicted task",
			zap.String("evicted_task", preemptResult.PreemptedTaskID),
			zap.Int64("freed_bytes", preemptResult.FreedBytes),
		)
		return result
	}

	// Step 2: No tasks to evict - try clearing Ollama model cache
	w.log.Warn("OOM recovery: no tasks to evict, attempting cache clear")

	if err := w.clearOllamaCache(); err == nil {
		result.Action = "cleared_cache"
		result.Success = true
		result.UserMessage = "Your GPU ran out of memory. Cleared model cache to recover. " +
			"The next request will need to reload the model."
		return result
	}

	// Step 3: Nothing worked - report failure with helpful message
	result.Action = "recovery_failed"
	result.Success = false
	result.UserMessage = fmt.Sprintf(
		"Your GPU (%.1f GB) ran out of memory. "+
			"Try running a smaller model or closing other GPU applications.",
		float64(event.VRAMSnapshot.Total)/(1024*1024*1024),
	)

	w.log.Error("OOM recovery failed - all strategies exhausted",
		zap.Int64("free_bytes", event.VRAMSnapshot.Free),
		zap.Int64("total_bytes", event.VRAMSnapshot.Total),
	)

	return result
}

// diagnoseCause attempts to identify what caused the OOM.
func (w *Watcher) diagnoseCause(stats backend.VRAMStats) string {
	budgetStats := w.manager.Stats()

	if budgetStats.ActiveBytes > stats.Total*8/10 {
		return "active_tasks_over_budget"
	}
	if stats.Fragmentation > stats.Total*2/10 {
		return "high_fragmentation"
	}
	return "unknown"
}

// clearOllamaCache sends an unload request to Ollama to free model VRAM.
func (w *Watcher) clearOllamaCache() error {
	// POST to Ollama API to unload all models
	resp, err := http.Post("http://localhost:11434/api/generate",
		"application/json",
		strings.NewReader(`{"model":"","keep_alive":0}`),
	)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}
