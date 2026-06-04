package pulse

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/openfabric/openfabric/internal/brain"
	"github.com/openfabric/openfabric/internal/cluster"
	"github.com/openfabric/openfabric/internal/llm"
	"github.com/openfabric/openfabric/internal/scheduler"
	"github.com/openfabric/openfabric/internal/shield"
	"go.uber.org/zap"
)

type InsightPriority string

const (
	PriorityInfo    InsightPriority = "info"
	PriorityWarning InsightPriority = "warning"
	PriorityAction  InsightPriority = "action"
)

type Action struct {
	Label string `json:"label"`
	Link  string `json:"link"`
}

type Insight struct {
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	Body        string          `json:"body"`
	Priority    InsightPriority `json:"priority"`
	Actions     []Action        `json:"actions,omitempty"`
	Dismissed   bool            `json:"dismissed"`
	CreatedAt   time.Time       `json:"created_at"`
	DismissedAt *time.Time      `json:"dismissed_at,omitempty"`
}

type WeeklyStats struct {
	TasksRun     int            `json:"tasks_run"`
	AIChats      int            `json:"ai_chats"`
	FilesIndexed int            `json:"files_indexed"`
	MostUsedModel string         `json:"most_used_model"`
	SavedUSD     float64        `json:"saved_usd"`
	FileStatuses map[string]any `json:"file_statuses"`
}

type PulseManager struct {
	cluster    *cluster.Manager
	scheduler  *scheduler.Scheduler
	llmMgr     *llm.Manager
	brainMgr   *brain.Manager
	log        *zap.Logger
	dataDir    string
	broadcast  func(event string, payload any)
	auditLog   *shield.AuditLog

	mu        sync.RWMutex
	insights  []*Insight
	cooldowns map[string]time.Time // ruleID -> last triggered time
}

// New creates a new PulseManager.
func New(
	clusterMgr *cluster.Manager,
	sched *scheduler.Scheduler,
	llmMgr *llm.Manager,
	brainMgr *brain.Manager,
	dataDir string,
	broadcast func(event string, payload any),
	log *zap.Logger,
) *PulseManager {
	pm := &PulseManager{
		cluster:   clusterMgr,
		scheduler: sched,
		llmMgr:    llmMgr,
		brainMgr:  brainMgr,
		dataDir:   dataDir,
		broadcast: broadcast,
		log:       log,
		cooldowns: make(map[string]time.Time),
	}

	pm.loadHistory()
	return pm
}

// SetBroadcast sets the broadcast callback function.
func (pm *PulseManager) SetBroadcast(fn func(event string, payload any)) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.broadcast = fn
}

// SetAuditLog registers the Fabric Shield audit log for anomaly detection.
func (pm *PulseManager) SetAuditLog(al *shield.AuditLog) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.auditLog = al
}

// Run starts the periodic inspection loop.
func (pm *PulseManager) Run(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// Run initial check after a brief delay
	go func() {
		select {
		case <-time.After(5 * time.Second):
			pm.CheckRules()
		case <-ctx.Done():
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pm.CheckRules()
		}
	}
}

// CheckRules evaluates all default rules.
func (pm *PulseManager) CheckRules() {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	now := time.Now()
	for _, rule := range DefaultRules {
		// Check cooldown
		if last, exists := pm.cooldowns[rule.ID]; exists {
			if now.Sub(last) < rule.Cooldown {
				continue
			}
		}

		// Run rule check
		insights := rule.Check(pm)
		if len(insights) == 0 {
			continue
		}

		triggered := false
		for _, ins := range insights {
			// Check if we already have this insight
			existingIdx := -1
			for i, e := range pm.insights {
				if e.ID == ins.ID {
					existingIdx = i
					break
				}
			}

			if existingIdx >= 0 {
				existing := pm.insights[existingIdx]
				// If it is already dismissed, do not re-notify/re-add.
				if existing.Dismissed {
					continue
				}
				// Otherwise, update existing
				existing.Title = ins.Title
				existing.Body = ins.Body
				existing.CreatedAt = ins.CreatedAt
			} else {
				// New insight
				pm.insights = append(pm.insights, &ins)
				if pm.broadcast != nil {
					pm.broadcast("pulse_insight", ins)
				}
				triggered = true
			}
		}

		if triggered {
			pm.cooldowns[rule.ID] = now
			pm.saveHistoryLocked()
		}
	}
}

// GetActiveInsights returns all non-dismissed insights.
func (pm *PulseManager) GetActiveInsights() []Insight {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	var active []Insight
	for _, ins := range pm.insights {
		if !ins.Dismissed {
			active = append(active, *ins)
		}
	}
	return active
}

// DismissInsight marks an insight as dismissed.
func (pm *PulseManager) DismissInsight(id string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	for _, ins := range pm.insights {
		if ins.ID == id {
			ins.Dismissed = true
			now := time.Now()
			ins.DismissedAt = &now
			pm.saveHistoryLocked()
			if pm.broadcast != nil {
				pm.broadcast("pulse_insight_dismissed", map[string]string{"id": id})
			}
			return nil
		}
	}
	return fmt.Errorf("insight not found: %s", id)
}

// GetHistory returns all insights (including dismissed).
func (pm *PulseManager) GetHistory() []Insight {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	history := make([]Insight, len(pm.insights))
	for i, ins := range pm.insights {
		history[i] = *ins
	}
	return history
}

// ClearHistory deletes all insights history.
func (pm *PulseManager) ClearHistory() {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	pm.insights = nil
	pm.saveHistoryLocked()
}

// GetWeeklyDigestStats aggregates stats from other services.
func (pm *PulseManager) GetWeeklyDigestStats() WeeklyStats {
	stats := WeeklyStats{}

	// Aggregate tasks run
	if pm.scheduler != nil {
		stats.TasksRun = len(pm.scheduler.List())
	}

	// Aggregate brain files count
	if pm.brainMgr != nil {
		brainStatus := pm.brainMgr.GetStatus()
		stats.FileStatuses = make(map[string]any) // unused placeholder or actual map
		stats.FilesIndexed = brainStatus.IndexedFiles
	}

	// Aggregate chats and calculate saved USD
	if pm.llmMgr != nil {
		sessions, err := pm.llmMgr.ListChatSessions()
		if err == nil {
			stats.AIChats = len(sessions)
			
			// Find most used model & calculate total tokens
			modelCounts := make(map[string]int)
			totalMsgs := 0
			for _, sess := range sessions {
				modelCounts[sess.Model]++
				// Fetch full session details to get messages count
				fullSess, err := pm.llmMgr.GetChatSession(sess.ID)
				if err == nil {
					totalMsgs += len(fullSess.Messages)
				}
			}

			mostUsed := "none"
			maxCount := 0
			for m, c := range modelCounts {
				if c > maxCount {
					maxCount = c
					mostUsed = m
				}
			}
			stats.MostUsedModel = mostUsed

			// Estimate saved USD compared to commercial cloud APIs ($0.01 per 1K tokens)
			// Assuming average prompt + completion = 400 tokens per message
			stats.SavedUSD = float64(totalMsgs) * 400.0 * 0.00001
		}
	}

	return stats
}

func (pm *PulseManager) historyFilePath() string {
	path := pm.dataDir
	if path == "" {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, ".openfabric")
	}
	return filepath.Join(path, "pulse", "history.json")
}

func (pm *PulseManager) loadHistory() {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	file := pm.historyFilePath()
	data, err := os.ReadFile(file)
	if err != nil {
		if !os.IsNotExist(err) {
			pm.log.Error("failed to read pulse history", zap.Error(err))
		}
		return
	}

	var insights []*Insight
	if err := json.Unmarshal(data, &insights); err != nil {
		pm.log.Error("failed to parse pulse history", zap.Error(err))
		return
	}
	pm.insights = insights
}

func (pm *PulseManager) saveHistoryLocked() {
	file := pm.historyFilePath()
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0755); err != nil {
		pm.log.Error("failed to create pulse history dir", zap.Error(err))
		return
	}

	data, err := json.MarshalIndent(pm.insights, "", "  ")
	if err != nil {
		pm.log.Error("failed to marshal pulse history", zap.Error(err))
		return
	}

	tmpFile := file + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0600); err != nil {
		pm.log.Error("failed to write pulse history temp file", zap.Error(err))
		return
	}

	if err := os.Rename(tmpFile, file); err != nil {
		pm.log.Error("failed to rename pulse history file", zap.Error(err))
		_ = os.Remove(tmpFile)
	}
}
