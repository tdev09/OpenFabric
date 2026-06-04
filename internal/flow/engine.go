package flow

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/google/uuid"
	"github.com/openfabric/openfabric/internal/cluster"
	"github.com/openfabric/openfabric/internal/config"
	"github.com/openfabric/openfabric/internal/llm"
	"github.com/openfabric/openfabric/internal/reliability/wal"
	"github.com/openfabric/openfabric/internal/scheduler"
	"github.com/openfabric/openfabric/internal/storage"
	"github.com/robfig/cron/v3"
	"go.uber.org/zap"
)

type Engine struct {
	mu           sync.Mutex
	mgr          *Manager
	cluster      *cluster.Manager
	scheduler    *scheduler.Scheduler
	llmMgr       *llm.Manager
	store        *storage.Store
	storageRoot  string
	log          *zap.Logger
	selfID       string
	running      bool
	cron         *cron.Cron
	watcher      *fsnotify.Watcher
	pendingFiles map[string]time.Time
	watcherMu    sync.Mutex
	cancelFuncs  map[string]context.CancelFunc
	activeRuns   map[string]*FlowRun
	runner       *StepRunner
	wal          *wal.WAL
}

func NewEngine(mgr *Manager, clusterMgr *cluster.Manager, sched *scheduler.Scheduler, llmMgr *llm.Manager, store *storage.Store, selfID string, log *zap.Logger) *Engine {
	// Derive the storage root from the flows directory parent (sibling "storage" dir).
	storageRoot := filepath.Join(filepath.Dir(mgr.flowsDir), "storage")

	e := &Engine{
		mgr:          mgr,
		cluster:      clusterMgr,
		scheduler:    sched,
		llmMgr:       llmMgr,
		store:        store,
		storageRoot:  storageRoot,
		selfID:       selfID,
		log:          log,
		pendingFiles: make(map[string]time.Time),
		cancelFuncs:  make(map[string]context.CancelFunc),
		activeRuns:   make(map[string]*FlowRun),
	}

	return e
}

// SetBroadcast sets the SSE broadcast channel.
func (e *Engine) SetBroadcast(fn func(event string, payload any)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.runner = NewStepRunner(e.scheduler, e.llmMgr, e.store, e.storageRoot, fn)
}

// SetWAL registers the WAL instance for flow run tracking.
func (e *Engine) SetWAL(w *wal.WAL) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.wal = w
}

// Run starts the election loop. It blocks until context cancellation.
func (e *Engine) Run(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// Initial check
	e.checkElection(ctx)

	for {
		select {
		case <-ctx.Done():
			e.Stop()
			return
		case <-ticker.C:
			e.checkElection(ctx)
		}
	}
}

func (e *Engine) checkElection(ctx context.Context) {
	isLeader := IsCoordinator(e.cluster, e.selfID)

	e.mu.Lock()
	defer e.mu.Unlock()

	if isLeader && !e.running {
		e.log.Info("node elected as coordinator, starting flow engine")
		e.start(ctx)
	} else if !isLeader && e.running {
		e.log.Info("node lost coordinator status, stopping flow engine")
		e.stop()
	}
}

func (e *Engine) start(ctx context.Context) {
	e.running = true
	e.cron = cron.New()
	e.cron.Start()

	// Setup file watcher
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		e.log.Error("failed to create flow fsnotify watcher", zap.Error(err))
	} else {
		e.watcher = watcher
		storageRoot := filepath.Join(filepath.Dir(e.mgr.flowsDir), "storage")
		if err := watcher.Add(storageRoot); err != nil {
			e.log.Warn("failed to watch storage folder", zap.String("path", storageRoot), zap.Error(err))
		} else {
			go e.watchFiles(ctx)
			go e.debounceLoop(ctx)
		}
	}

	// Register cron schedules for enabled flows
	e.rebuildSchedules()

	// Resume incomplete flow runs
	go e.resumeIncompleteRuns(ctx)
}

func (e *Engine) stop() {
	e.running = false
	if e.cron != nil {
		e.cron.Stop()
		e.cron = nil
	}
	if e.watcher != nil {
		e.watcher.Close()
		e.watcher = nil
	}
}

// Stop terminates the engine.
func (e *Engine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stop()

	finished := time.Now()
	for id, cancel := range e.cancelFuncs {
		cancel()
		run, exists := e.activeRuns[id]
		if exists {
			run.Status = RunFailed
			run.Error = "agent shutting down"
			run.FinishedAt = &finished
			_ = e.mgr.UpdateRun(run)
		}
	}
	e.cancelFuncs = make(map[string]context.CancelFunc)
	e.activeRuns = make(map[string]*FlowRun)
}

func (e *Engine) rebuildSchedules() {
	if e.cron == nil {
		return
	}

	// Clear existing schedules
	for _, entry := range e.cron.Entries() {
		e.cron.Remove(entry.ID)
	}

	flows, err := e.mgr.ListFlows()
	if err != nil {
		e.log.Error("failed to list flows for scheduling", zap.Error(err))
		return
	}

	for _, flow := range flows {
		if !flow.Enabled || flow.Trigger.Type != TriggerSchedule || flow.Trigger.Cron == "" {
			continue
		}

		flowID := flow.ID
		flowName := flow.Name
		_, err := e.cron.AddFunc(flow.Trigger.Cron, func() {
			e.log.Info("flow triggered by cron schedule", zap.String("flow_name", flowName))
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			if _, err := e.TriggerFlow(ctx, flowID, "schedule", nil); err != nil {
				e.log.Warn("schedule flow execution failed", zap.String("flow_name", flowName), zap.Error(err))
			}
		})
		if err != nil {
			e.log.Warn("invalid cron expression for flow", zap.String("flow_name", flowName), zap.String("cron", flow.Trigger.Cron), zap.Error(err))
		}
	}
}

// RebuildSchedules allows external triggers (API toggling) to refresh cron entries.
func (e *Engine) RebuildSchedules() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running {
		e.rebuildSchedules()
	}
}

func (e *Engine) watchFiles(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-e.watcher.Events:
			if !ok {
				return
			}
			// Monitor create and write events for debouncing
			if event.Has(fsnotify.Create) || event.Has(fsnotify.Write) {
				e.watcherMu.Lock()
				e.pendingFiles[event.Name] = time.Now()
				e.watcherMu.Unlock()
			}
		case err, ok := <-e.watcher.Errors:
			if !ok {
				return
			}
			e.log.Error("flow fsnotify error", zap.Error(err))
		}
	}
}

func (e *Engine) debounceLoop(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.watcherMu.Lock()
			now := time.Now()
			for path, lastSeen := range e.pendingFiles {
				if now.Sub(lastSeen) >= 1500*time.Millisecond {
					delete(e.pendingFiles, path)
					filename := filepath.Base(path)
					go e.triggerFileFlows(filename)
				}
			}
			e.watcherMu.Unlock()
		}
	}
}

func (e *Engine) triggerFileFlows(filename string) {
	flows, err := e.mgr.ListFlows()
	if err != nil {
		return
	}

	for _, flow := range flows {
		if !flow.Enabled {
			continue
		}
		if flow.Trigger.Type != TriggerFileAdded && flow.Trigger.TriggerType() != TriggerFileModified {
			continue
		}
		pattern := flow.Trigger.Pattern
		if pattern == "" {
			pattern = "*"
		}

		matched, err := filepath.Match(pattern, filename)
		if err == nil && matched {
			e.log.Info("flow triggered by file change", zap.String("flow_name", flow.Name), zap.String("filename", filename))
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			triggerVars := map[string]string{"filename": filename}
			if _, err := e.TriggerFlow(ctx, flow.ID, string(flow.Trigger.Type)+":"+filename, triggerVars); err != nil {
				e.log.Warn("file flow execution failed", zap.String("flow_name", flow.Name), zap.Error(err))
			}
		}
	}
}

// TriggerFlow executes a flow run.
func (e *Engine) TriggerFlow(ctx context.Context, flowID string, trigger string, triggerVars map[string]string) (*FlowRun, error) {
	flow, err := e.mgr.GetFlow(flowID)
	if err != nil {
		return nil, err
	}

	runID := uuid.New().String()
	run := &FlowRun{
		ID:        runID,
		FlowID:    flow.ID,
		FlowName:  flow.Name,
		Status:    RunRunning,
		Trigger:   trigger,
		Steps:     []StepResult{},
		StartedAt: time.Now(),
		Variables: make(map[string]interface{}),
	}

	if err := e.mgr.CreateRun(run); err != nil {
		return nil, fmt.Errorf("create run record: %w", err)
	}

	// Run in background sequential block
	runCtx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	e.cancelFuncs[runID] = cancel
	e.activeRuns[runID] = run
	e.mu.Unlock()

	go e.executeRun(runCtx, run, flow, triggerVars)

	return run, nil
}

// dagState holds thread-safe bookkeeping for a single parallel DAG execution.
type dagState struct {
	mu        sync.Mutex
	completed map[string]string // stepID -> output
	failed    map[string]string // stepID -> error message
	running   map[string]bool   // stepID -> currently dispatched
	stepsVars map[string]map[string]string
}

func newDAGState() *dagState {
	return &dagState{
		completed: make(map[string]string),
		failed:    make(map[string]string),
		running:   make(map[string]bool),
		stepsVars: make(map[string]map[string]string),
	}
}

// stepResult is sent back from each goroutine on the finishedCh.
type stepResult struct {
	res StepResult
	err error
}

func (e *Engine) executeRun(ctx context.Context, run *FlowRun, flow *FlowDefinition, triggerVars map[string]string) {
	defer func() {
		e.mu.Lock()
		delete(e.cancelFuncs, run.ID)
		delete(e.activeRuns, run.ID)
		e.mu.Unlock()
	}()

	clusterSum := e.cluster.Summary()
	nodeCount := clusterSum.NodeCount
	pooledRAM := clusterSum.TotalRAM
	clusterName := config.ProjectName + " Cluster"

	// Try to resolve cluster name from settings
	nodes := e.cluster.List()
	if len(nodes) > 0 {
		clusterName = nodes[0].Name + "'s Cluster"
	}

	dag := newDAGState()

	// In case of resumed run: prepopulate variables of already-completed steps.
	for _, res := range run.Steps {
		if res.Status == "completed" {
			dag.completed[res.ID] = res.Output
			dag.stepsVars[res.ID] = map[string]string{"output": res.Output}
		} else if res.Status == "failed" {
			dag.failed[res.ID] = res.Error
		}
	}

	var runLsn uint64
	if e.wal != nil {
		var walErr error
		runLsn, walErr = e.wal.Append(wal.EntryFlowRunStart, run.ID, wal.FlowPayload{
			FlowID: flow.ID,
			RunID:  run.ID,
		})
		if walErr != nil {
			e.log.Error("failed to write flow run start to WAL", zap.Error(walErr))
		}
	}

	// Build a quick lookup map for all step definitions.
	stepMap := make(map[string]Step, len(flow.Steps))
	for _, s := range flow.Steps {
		stepMap[s.ID] = s
	}

	// If a flow has no depends_on declarations it should behave exactly as before
	// (sequential). We synthesise implicit dependencies: step[i] depends on step[i-1].
	hasExplicitDeps := false
	for _, s := range flow.Steps {
		if len(s.DependsOn) > 0 {
			hasExplicitDeps = true
			break
		}
	}
	if !hasExplicitDeps && len(flow.Steps) > 1 {
		for i := 1; i < len(flow.Steps); i++ {
			step := flow.Steps[i]
			step.DependsOn = []string{flow.Steps[i-1].ID}
			flow.Steps[i] = step
			stepMap[step.ID] = step
		}
	}

	// finishedCh carries results from goroutines back to the coordinator.
	finishedCh := make(chan stepResult, len(flow.Steps))

	e.mu.Lock()
	runner := e.runner
	e.mu.Unlock()
	if runner == nil {
		finished := time.Now()
		run.FinishedAt = &finished
		run.Status = RunFailed
		run.Error = "step runner not initialized"
		_ = e.mgr.UpdateRun(run)
		return
	}

	// runStep dispatches a single step in a goroutine and sends the result to finishedCh.
	runStep := func(step Step) {
		dag.mu.Lock()
		tplCtx := BuildTemplateContext(clusterName, nodeCount, pooledRAM, triggerVars, dag.stepsVars)
		dag.mu.Unlock()

		// Record the step as "running" in the run record so the UI shows it live.
		dag.mu.Lock()
		// Overwrite any previous running record or add new one.
		updated := false
		for i, r := range run.Steps {
			if r.ID == step.ID {
				run.Steps[i].Status = "running"
				updated = true
				break
			}
		}
		if !updated {
			run.Steps = append(run.Steps, StepResult{ID: step.ID, Status: "running", StartedAt: time.Now()})
		}
		dag.mu.Unlock()
		_ = e.mgr.UpdateRun(run)

		e.log.Info("executing flow step (parallel)",
			zap.String("flow", flow.Name),
			zap.String("step", step.ID),
			zap.String("type", string(step.Type)),
		)

		res, err := runner.RunStep(ctx, step, tplCtx)
		finishedCh <- stepResult{res: res, err: err}
	}

	var runErr error

	// Coordination loop - runs until all steps are done or a fatal error occurs.
	for {
		dag.mu.Lock()
		total := len(flow.Steps)
		done := len(dag.completed) + len(dag.failed)
		// Find steps that are ready to execute now.
		var ready []Step
		for _, s := range flow.Steps {
			if dag.completed[s.ID] != "" || dag.running[s.ID] {
				continue
			}
			if _, wasFailed := dag.failed[s.ID]; wasFailed {
				continue
			}
			// Skip already-completed (resume path)
			if _, ok := dag.completed[s.ID]; ok {
				continue
			}
			// Check all dependencies satisfied.
			depsOK := true
			for _, dep := range s.DependsOn {
				if _, ok := dag.completed[dep]; !ok {
					depsOK = false
					break
				}
			}
			if !depsOK {
				continue
			}
			// Check if any dependency has failed (cascade).
			depFailed := false
			for _, dep := range s.DependsOn {
				if _, failed := dag.failed[dep]; failed {
					depFailed = true
					break
				}
			}
			if depFailed {
				// Cascade failure - mark without running.
				dag.failed[s.ID] = "dependency failed"
				done++
				// Append cascade-failed step to run record.
				cascadeRes := StepResult{
					ID:         s.ID,
					Status:     "failed",
					Error:      "dependency failed",
					StartedAt:  time.Now(),
					FinishedAt: time.Now(),
				}
				run.Steps = append(run.Steps, cascadeRes)
				continue
			}
			ready = append(ready, s)
		}
		activeCount := len(dag.running)
		dag.mu.Unlock()

		// Dispatch all newly-ready steps concurrently.
		for _, s := range ready {
			dag.mu.Lock()
			dag.running[s.ID] = true
			dag.mu.Unlock()
			go runStep(s)
		}
		activeCount += len(ready)

		// All steps accounted for - exit.
		if done == total && activeCount == 0 {
			break
		}

		// If nothing is running and nothing was ready, we're stuck (shouldn't happen
		// after cycle detection, but guard defensively).
		if activeCount == 0 && len(ready) == 0 {
			dag.mu.Lock()
			accounted := len(dag.completed) + len(dag.failed)
			dag.mu.Unlock()
			if accounted < total {
				runErr = fmt.Errorf("execution stalled: %d/%d steps unreachable", total-accounted, total)
				break
			}
			break
		}

		// Wait for the next step to finish or context cancellation.
		select {
		case <-ctx.Done():
			runErr = ctx.Err()
			goto finalize
		case sr := <-finishedCh:
			dag.mu.Lock()
			delete(dag.running, sr.res.ID)

			// Update run.Steps with the final result record.
			updated := false
			for i, r := range run.Steps {
				if r.ID == sr.res.ID {
					run.Steps[i] = sr.res
					updated = true
					break
				}
			}
			if !updated {
				run.Steps = append(run.Steps, sr.res)
			}

			if sr.err != nil {
				dag.failed[sr.res.ID] = sr.err.Error()
				if runErr == nil {
					runErr = sr.err
				}
				e.log.Warn("flow step failed",
					zap.String("flow", flow.Name),
					zap.String("step", sr.res.ID),
					zap.Error(sr.err),
				)
			} else {
				dag.completed[sr.res.ID] = sr.res.Output
				dag.stepsVars[sr.res.ID] = map[string]string{"output": sr.res.Output}
				if stepMap[sr.res.ID].Type == StepSave {
					dag.stepsVars[sr.res.ID]["path"] = sr.res.Output
				}
			}
			dag.mu.Unlock()

			// Persist WAL checkpoint.
			if e.wal != nil {
				stepDef := stepMap[sr.res.ID]
				if sr.err == nil {
					lsn, werr := e.wal.Append(wal.EntryFlowStepComplete, run.ID, wal.FlowPayload{
						FlowID:   flow.ID,
						RunID:    run.ID,
						StepID:   sr.res.ID,
						StepType: string(stepDef.Type),
					})
					if werr == nil && lsn != 0 {
						_ = e.wal.Commit(lsn, sr.res.ID)
					}
				}
			}

			_ = e.mgr.UpdateRun(run)
		}
	}

finalize:
	finished := time.Now()
	run.FinishedAt = &finished

	if runErr != nil {
		run.Status = RunFailed
		run.Error = runErr.Error()
	} else {
		run.Status = RunCompleted
	}

	if e.wal != nil && runLsn != 0 {
		if runErr != nil {
			_ = e.wal.Abort(runLsn, run.ID, runErr.Error())
		} else {
			_ = e.wal.Commit(runLsn, run.ID)
		}
	}

	_ = e.mgr.UpdateRun(run)
	e.log.Info("flow run finished",
		zap.String("flow", flow.Name),
		zap.String("run_id", run.ID),
		zap.String("status", string(run.Status)),
	)
}

func (e *Engine) resumeIncompleteRuns(ctx context.Context) {
	runs, err := e.mgr.ListRuns("")
	if err != nil {
		return
	}

	for _, run := range runs {
		if run.Status == RunRunning {
			flow, err := e.mgr.GetFlow(run.FlowID)
			if err != nil {
				// Mark as failed if flow deleted
				finished := time.Now()
				run.FinishedAt = &finished
				run.Status = RunFailed
				run.Error = "flow definition no longer exists"
				_ = e.mgr.UpdateRun(run)
				continue
			}

			// Resume in background
			e.log.Info("resuming incomplete flow run", zap.String("flow_name", flow.Name), zap.String("run_id", run.ID))
			runCtx, cancel := context.WithCancel(context.Background())
			e.mu.Lock()
			e.cancelFuncs[run.ID] = cancel
			e.activeRuns[run.ID] = run
			e.mu.Unlock()

			// Extract trigger filename if exists
			var triggerVars map[string]string
			if strings.HasPrefix(run.Trigger, "file_added:") {
				fname := strings.TrimPrefix(run.Trigger, "file_added:")
				triggerVars = map[string]string{"filename": fname}
			} else if strings.HasPrefix(run.Trigger, "file_modified:") {
				fname := strings.TrimPrefix(run.Trigger, "file_modified:")
				triggerVars = map[string]string{"filename": fname}
			}

			go e.executeRun(runCtx, run, flow, triggerVars)
		}
	}
}

// CancelRun terminates an active flow run.
func (e *Engine) CancelRun(runID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	cancel, ok := e.cancelFuncs[runID]
	if ok {
		cancel()
		run, exists := e.activeRuns[runID]
		if exists {
			run.Status = RunFailed
			run.Error = "cancelled by user"
			finished := time.Now()
			run.FinishedAt = &finished
			_ = e.mgr.UpdateRun(run)
		}
		delete(e.cancelFuncs, runID)
		delete(e.activeRuns, runID)
		return true
	}
	return false
}

// TriggerType extension helper
func (t TriggerConfig) TriggerType() TriggerType {
	return t.Type
}
