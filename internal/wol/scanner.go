package wol

import (
	"context"
	"time"

	"github.com/openfabric/openfabric/internal/cluster"
	"go.uber.org/zap"
)

// StartAutoWaker runs a background ticker checking cluster memory pressure.
func (m *Manager) StartAutoWaker(ctx context.Context, localNodeID string, getThreshold func() float64) {
	m.log.Info("starting AutoWaker memory monitoring loop...")
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			m.log.Info("AutoWaker memory monitoring loop stopped")
			return
		case <-ticker.C:
			// Ensure only the elected coordinator executes auto-wake logic.
			if m.clusterMgr.CoordinatorID() != localNodeID {
				continue
			}

			threshold := getThreshold()
			if threshold <= 0 {
				continue // auto-wake disabled or invalid threshold
			}

			m.checkMemoryPressure(localNodeID, threshold)
		}
	}
}

func (m *Manager) checkMemoryPressure(localNodeID string, threshold float64) {
	summary := m.clusterMgr.Summary()
	if summary.TotalRAM == 0 {
		return
	}

	freeRAM := float64(summary.TotalRAM - summary.UsedRAM)
	freePct := freeRAM / float64(summary.TotalRAM)

	if freePct >= threshold {
		return // Sufficient RAM available in the cluster
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Only emit a Warn if we actually have WoL devices registered - otherwise
	// this fires every 30s on a single-node setup with no actionable outcome.
	if len(m.devices) == 0 {
		m.log.Debug("cluster memory pressure detected but no WoL devices registered",
			zap.Float64("free_pct", freePct),
			zap.Float64("threshold", threshold),
		)
		return
	}

	m.log.Warn("cluster memory pressure detected, searching for offline devices to wake",
		zap.Float64("free_pct", freePct),
		zap.Float64("threshold", threshold),
		zap.Uint64("total_ram", summary.TotalRAM),
		zap.Uint64("used_ram", summary.UsedRAM),
	)

	var candidate *Device
	now := time.Now()

	for _, d := range m.devices {
		if d.LinkedNodeID == "" {
			continue
		}

		node, exists := m.clusterMgr.Get(d.LinkedNodeID)
		isOffline := !exists || node.Status == cluster.StatusOffline
		onCooldown := now.Sub(d.LastWoken) < 5*time.Minute

		if isOffline && !onCooldown {
			// Prioritize waking the device with the lowest wake count to balance wear.
			if candidate == nil || d.WakeCount < candidate.WakeCount {
				candidate = d
			}
		}
	}

	if candidate != nil {
		candidate.LastWoken = now
		candidate.WakeCount++
		_ = m.save()

		m.log.Info("AutoWaker: waking offline cluster node",
			zap.String("node_id", candidate.LinkedNodeID),
			zap.String("mac", candidate.MAC),
			zap.String("name", candidate.Name),
		)

		// Broadcast update event (simulate node state change notification)
		// We'll rely on handlers or SSE triggers to notify frontend of the wake attempt.
		go func(mac, bcast, lastIP string) {
			if err := SendMagicPacket(mac, bcast, lastIP, m.log); err != nil {
				m.log.Error("AutoWaker failed to send magic packet", zap.String("mac", mac), zap.Error(err))
			}
		}(candidate.MAC, candidate.BroadcastIP, candidate.LastIP)
	} else {
		m.log.Debug("AutoWaker: no offline devices available to wake (or all are on 5m cooldown)")
	}
}
