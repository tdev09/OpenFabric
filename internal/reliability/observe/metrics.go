package observe

import (
	"context"
	"expvar"
	"time"
)

// Metrics holds all OpenFabric operational counters and gauges.
// Exposed via /api/metrics as JSON (expvar format).
// Zero external dependencies - uses Go stdlib expvar.
var Metrics = struct {
	// Task metrics
	TasksSubmitted *expvar.Int
	TasksCompleted *expvar.Int
	TasksFailed    *expvar.Int
	TasksCancelled *expvar.Int
	TasksRequeued  *expvar.Int

	// LLM metrics
	LLMRequests    *expvar.Int
	LLMTokensTotal *expvar.Int
	LLMFallbacks   *expvar.Int // times distributed fell back to single-node

	// Storage metrics
	StorageWrites    *expvar.Int
	StorageReadBytes *expvar.Int
	StorageSyncLag   *expvar.Float // seconds since last successful sync

	// Cluster metrics
	NodesOnline   *expvar.Int
	NodeEvictions *expvar.Int
	PeerMessages  *expvar.Int

	// Reliability metrics
	WALEntries          *expvar.Int
	WALCheckpoints      *expvar.Int
	RetryAttempts       *expvar.Int
	PanicCount          *expvar.Int
	CircuitBreakerTrips *expvar.Int

	// Latency histograms (simplified as maps of p50/p95/p99)
	TaskLatencyMs *expvar.Map
	LLMLatencyMs  *expvar.Map

	// Process metrics
	UptimeSeconds *expvar.Int
	StartedAt     time.Time
}{
	TasksSubmitted:      expvar.NewInt("fabric_tasks_submitted_total"),
	TasksCompleted:      expvar.NewInt("fabric_tasks_completed_total"),
	TasksFailed:         expvar.NewInt("fabric_tasks_failed_total"),
	TasksCancelled:      expvar.NewInt("fabric_tasks_cancelled_total"),
	TasksRequeued:       expvar.NewInt("fabric_tasks_requeued_total"),
	LLMRequests:         expvar.NewInt("fabric_llm_requests_total"),
	LLMTokensTotal:      expvar.NewInt("fabric_llm_tokens_total"),
	LLMFallbacks:        expvar.NewInt("fabric_llm_fallbacks_total"),
	StorageWrites:       expvar.NewInt("fabric_storage_writes_total"),
	StorageReadBytes:    expvar.NewInt("fabric_storage_read_bytes_total"),
	StorageSyncLag:      expvar.NewFloat("fabric_storage_sync_lag_seconds"),
	NodesOnline:         expvar.NewInt("fabric_nodes_online"),
	NodeEvictions:       expvar.NewInt("fabric_node_evictions_total"),
	PeerMessages:        expvar.NewInt("fabric_peer_messages_total"),
	WALEntries:          expvar.NewInt("fabric_wal_entries_total"),
	WALCheckpoints:      expvar.NewInt("fabric_wal_checkpoints_total"),
	RetryAttempts:       expvar.NewInt("fabric_retry_attempts_total"),
	PanicCount:          expvar.NewInt("fabric_panics_total"),
	CircuitBreakerTrips: expvar.NewInt("fabric_circuit_breaker_trips_total"),
	TaskLatencyMs:       expvar.NewMap("fabric_task_latency_ms"),
	LLMLatencyMs:        expvar.NewMap("fabric_llm_latency_ms"),
	UptimeSeconds:       expvar.NewInt("fabric_uptime_seconds"),
	StartedAt:           time.Now(),
}

// StartUptimeTracker updates the uptime counter every second.
func StartUptimeTracker(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				Metrics.UptimeSeconds.Set(
					int64(time.Since(Metrics.StartedAt).Seconds()))
			}
		}
	}()
}
