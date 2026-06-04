// Package cluster manages health checks and eviction of dead nodes.
package cluster

import (
	"context"
	"time"

	"go.uber.org/zap"
)

const (
	// healthCheckInterval is how often we scan for dead nodes.
	healthCheckInterval = 5 * time.Second
	// deadNodeTimeout is how long a node can be silent before being marked offline.
	deadNodeTimeout = 10 * time.Second
)

// HealthChecker runs periodic checks on all known nodes.
type HealthChecker struct {
	manager *Manager
	selfID  string
	OnEvict func(nodeID string)
	log     *zap.Logger
}

// NewHealthChecker creates a HealthChecker backed by the given manager.
func NewHealthChecker(manager *Manager, selfID string, onEvict func(nodeID string), log *zap.Logger) *HealthChecker {
	return &HealthChecker{manager: manager, selfID: selfID, OnEvict: onEvict, log: log}
}

// Run starts the health check loop. It blocks until ctx is cancelled.
func (h *HealthChecker) Run(ctx context.Context) {
	ticker := time.NewTicker(healthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.check()
		}
	}
}

// check scans all online nodes and marks any that have exceeded the deadline.
func (h *HealthChecker) check() {
	isCoordinator := h.manager.CoordinatorID() == h.selfID
	deadline := time.Now().Add(-deadNodeTimeout)
	nodes := h.manager.List()

	for _, node := range nodes {
		if node.ID == h.selfID {
			continue
		}
		if node.Status == StatusOffline {
			continue
		}
		if node.LastSeen.Before(deadline) {
			h.log.Info("node timed out, marking offline",
				zap.String("node_id", node.ID),
				zap.String("node_name", node.Name),
				zap.Duration("silent_for", time.Since(node.LastSeen)),
			)
			h.manager.MarkOffline(node.ID)

			if isCoordinator && h.OnEvict != nil {
				h.OnEvict(node.ID)
			}
		}
	}
}
