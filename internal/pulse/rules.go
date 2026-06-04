package pulse

import (
	"fmt"
	"time"

	"github.com/openfabric/openfabric/internal/cluster"
	"github.com/openfabric/openfabric/internal/scheduler"
)

// Rule represents an inspection check run by the Pulse daemon.
type Rule struct {
	ID       string
	Cooldown time.Duration
	Priority InsightPriority
	Check    func(pm *PulseManager) []Insight
}

// DefaultRules defines the list of rules executed by the daemon.
var DefaultRules = []Rule{
	{
		ID:       "high_ram",
		Cooldown: 30 * time.Minute,
		Priority: PriorityWarning,
		Check:    checkHighRAM,
	},
	{
		ID:       "stuck_task",
		Cooldown: 1 * time.Hour,
		Priority: PriorityWarning,
		Check:    checkStuckTask,
	},
	{
		ID:       "storage_full",
		Cooldown: 1 * time.Hour,
		Priority: PriorityWarning,
		Check:    checkStorageFull,
	},
	{
		ID:       "slow_inference",
		Cooldown: 1 * time.Hour,
		Priority: PriorityInfo,
		Check:    checkSlowInference,
	},
	{
		ID:       "node_offline_long",
		Cooldown: 30 * time.Minute,
		Priority: PriorityInfo,
		Check:    checkNodeOfflineLong,
	},
	{
		ID:       "weekly_digest",
		Cooldown: 7 * 24 * time.Hour, // Weekly digest
		Priority: PriorityInfo,
		Check:    checkWeeklyDigest,
	},
	{
		ID:       "shield_violations",
		Cooldown: 1 * time.Minute,
		Priority: PriorityWarning,
		Check:    checkShieldViolations,
	},
}

func checkHighRAM(pm *PulseManager) []Insight {
	if pm.cluster == nil {
		return nil
	}
	var insights []Insight
	for _, node := range pm.cluster.List() {
		if node.Status != cluster.StatusOnline || node.RAMTotal == 0 {
			continue
		}
		pct := 100.0 * float64(node.RAMUsed) / float64(node.RAMTotal)
		if pct > 90.0 {
			insights = append(insights, Insight{
				ID:       fmt.Sprintf("high_ram-%s", node.ID),
				Title:    fmt.Sprintf("High RAM on %s", node.Name),
				Body:     fmt.Sprintf("RAM usage on %s is at %.1f%%. Consider closing unused apps or adding another device.", node.Name, pct),
				Priority: PriorityWarning,
				Actions: []Action{
					{Label: "Add Device", Link: "/devices"},
				},
				CreatedAt: time.Now(),
			})
		}
	}
	return insights
}

func checkStuckTask(pm *PulseManager) []Insight {
	if pm.scheduler == nil {
		return nil
	}
	var insights []Insight
	for _, task := range pm.scheduler.List() {
		if task.Status == scheduler.TaskRunning && task.StartedAt != nil {
			dur := time.Since(*task.StartedAt)
			if dur > 2*time.Hour {
				insights = append(insights, Insight{
					ID:       fmt.Sprintf("stuck_task-%s", task.ID),
					Title:    fmt.Sprintf("Task %s Might Be Stuck", task.ID),
					Body:     fmt.Sprintf("Task %s (%s) has been running for %s. Consider cancelling if it is stuck.", task.ID, task.Command, dur.Round(time.Minute)),
					Priority: PriorityWarning,
					Actions: []Action{
						{Label: "View Tasks", Link: "/tasks"},
					},
					CreatedAt: time.Now(),
				})
			}
		}
	}
	return insights
}

func checkStorageFull(pm *PulseManager) []Insight {
	if pm.cluster == nil {
		return nil
	}
	var insights []Insight
	for _, node := range pm.cluster.List() {
		if node.Status != cluster.StatusOnline || node.StorageTotal == 0 {
			continue
		}
		pct := 100.0 * float64(node.StorageUsed) / float64(node.StorageTotal)
		if pct > 85.0 {
			insights = append(insights, Insight{
				ID:       fmt.Sprintf("storage_full-%s", node.ID),
				Title:    fmt.Sprintf("Storage Nearly Full on %s", node.Name),
				Body:     fmt.Sprintf("Shared storage on %s is %.1f%% full. Consider removing unused files.", node.Name, pct),
				Priority: PriorityWarning,
				Actions: []Action{
					{Label: "Manage Storage", Link: "/storage"},
				},
				CreatedAt: time.Now(),
			})
		}
	}
	return insights
}

func checkSlowInference(pm *PulseManager) []Insight {
	if pm.llmMgr == nil {
		return nil
	}
	var insights []Insight
	speed := pm.llmMgr.LastInferenceSpeed()
	if speed > 0 && speed < 1.0 {
		insights = append(insights, Insight{
			ID:        "slow_inference",
			Title:     "Slow local AI inference detected",
			Body:      fmt.Sprintf("Your last local AI generation ran at %.1f tok/s. Consider switching to a quantized variant or adding devices to pool RAM.", speed),
			Priority:  PriorityInfo,
			Actions:   []Action{{Label: "Models Catalog", Link: "/models"}},
			CreatedAt: time.Now(),
		})
	}
	return insights
}

func checkNodeOfflineLong(pm *PulseManager) []Insight {
	if pm.cluster == nil {
		return nil
	}
	var insights []Insight
	for _, node := range pm.cluster.List() {
		if node.Status == cluster.StatusOffline && !node.LastSeen.IsZero() {
			dur := time.Since(node.LastSeen)
			if dur > 1*time.Hour {
				insights = append(insights, Insight{
					ID:        fmt.Sprintf("node_offline_long-%s", node.ID),
					Title:     fmt.Sprintf("Device %s is offline", node.Name),
					Body:      fmt.Sprintf("Device %s has been offline for %s. Tasks have been re-routed to active nodes.", node.Name, dur.Round(time.Minute)),
					Priority:  PriorityInfo,
					Actions:   []Action{{Label: "View Devices", Link: "/devices"}},
					CreatedAt: time.Now(),
				})
			}
		}
	}
	return insights
}


func checkWeeklyDigest(pm *PulseManager) []Insight {
	var insights []Insight
	stats := pm.GetWeeklyDigestStats()
	
	body := fmt.Sprintf("This week on your cluster: %d tasks run • %d AI chats • %d files indexed. Most used model: %s. Saved vs cloud APIs: $%.2f.",
		stats.TasksRun, stats.AIChats, stats.FilesIndexed, stats.MostUsedModel, stats.SavedUSD)
		
	insights = append(insights, Insight{
		ID:        fmt.Sprintf("weekly_digest-%d", time.Now().Unix()/604800), // unique per week
		Title:     "Weekly Cluster Digest",
		Body:      body,
		Priority:  PriorityInfo,
		CreatedAt: time.Now(),
	})
	return insights
}

func checkShieldViolations(pm *PulseManager) []Insight {
	al := pm.auditLog
	if al == nil {
		return nil
	}

	// Retrieve violation count for the last 24 hours.
	v24h, err := al.ViolationCount(24 * time.Hour)
	if err != nil || v24h == 0 {
		return nil
	}

	// Retrieve violation count for the last hour.
	v1h, err := al.ViolationCount(time.Hour)
	if err != nil {
		return nil
	}

	var insights []Insight
	if v1h > 3 {
		insights = append(insights, Insight{
			ID:       "shield_alert-high",
			Title:    "High Security Risk: Multiple Sandbox Rejections",
			Body:     fmt.Sprintf("Fabric Shield has detected %d sandbox violations/rejections in the last hour. Your system may be under attack.", v1h),
			Priority: PriorityAction,
			Actions: []Action{
				{Label: "View Audit Log", Link: "/shield"},
			},
			CreatedAt: time.Now(),
		})

		// Broadcast a direct alert for UI notification.
		if pm.broadcast != nil {
			pm.broadcast("shield_alert", map[string]any{
				"risk_level":    "high",
				"violations_1h": v1h,
				"message":       "Multiple security violations detected in the last hour",
			})
		}
	} else {
		insights = append(insights, Insight{
			ID:       "shield_alert-medium",
			Title:    "Security Audit Warning",
			Body:     fmt.Sprintf("Fabric Shield has recorded %d security events in the last 24 hours. Verify task origins.", v24h),
			Priority: PriorityWarning,
			Actions: []Action{
				{Label: "View Audit Log", Link: "/shield"},
			},
			CreatedAt: time.Now(),
		})
	}

	return insights
}
