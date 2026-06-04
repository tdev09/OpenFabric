package gpu

import (
	"context"
	"fmt"
	"os/exec"
	"sync"

	"go.uber.org/zap"

	"github.com/openfabric/openfabric/internal/gpu/backend"
	"github.com/openfabric/openfabric/internal/gpu/budget"
	"github.com/openfabric/openfabric/internal/gpu/isolation"
	"github.com/openfabric/openfabric/internal/gpu/preempt"
	"github.com/openfabric/openfabric/internal/gpu/recovery"
	"github.com/openfabric/openfabric/internal/gpu/thermal"
)

// Orchestrator is the single entry point for all GPU operations.
// It wires together backend detection, budget management, thermal
// monitoring, task isolation, preemption, and OOM recovery.
type Orchestrator struct {
	mu      sync.RWMutex
	backend backend.Backend
	devices []backend.Device

	// Per-device managers
	budgets    map[int]*budget.Manager
	thermals   map[int]*thermal.Monitor
	preemptors map[int]*preempt.Preemptor
	watchers   map[int]*recovery.Watcher

	estimator budget.VRAMEstimator
	onEvent   func(GPUEvent) // dashboard SSE callback
	log       *zap.Logger
}

// GPUEvent is emitted to the dashboard for live GPU status updates.
type GPUEvent struct {
	Type        string  `json:"type"` // "oom", "thermal_throttle", "thermal_recover", "thermal_emergency", "task_preempted", "reservation_failed"
	DeviceIndex int     `json:"device_index"`
	Message     string  `json:"message"` // plain English for user
	TempCelsius float64 `json:"temp_celsius"`
	TaskID      string  `json:"task_id"`
}

// NewOrchestrator creates and initializes an Orchestrator.
// Detects the GPU backend, enumerates devices, and starts all monitors.
func NewOrchestrator(onEvent func(GPUEvent), log *zap.Logger) (*Orchestrator, error) {
	b := backend.Detect()

	devices, err := b.Devices()
	if err != nil {
		return nil, fmt.Errorf("gpu orchestrator: detect devices: %w", err)
	}

	o := &Orchestrator{
		backend:    b,
		devices:    devices,
		budgets:    make(map[int]*budget.Manager),
		thermals:   make(map[int]*thermal.Monitor),
		preemptors: make(map[int]*preempt.Preemptor),
		watchers:   make(map[int]*recovery.Watcher),
		onEvent:    onEvent,
		log:        log,
	}

	// Initialize per-device components
	for _, device := range devices {
		idx := device.Index

		// Budget manager
		o.budgets[idx] = budget.NewManager(idx, b, log)

		// Preemptor
		o.preemptors[idx] = preempt.NewPreemptor(o.budgets[idx], log)

		// Thermal monitor
		o.thermals[idx] = thermal.NewMonitor(
			b,
			thermal.DefaultPolicy,
			func(i int, temp float64) {
				o.emit(GPUEvent{
					Type:        "thermal_throttle",
					DeviceIndex: i,
					TempCelsius: temp,
					Message: fmt.Sprintf(
						"GPU is running hot (%.0f°C). New AI tasks paused until it cools down.",
						temp,
					),
				})
			},
			func(i int) {
				o.emit(GPUEvent{
					Type:        "thermal_recover",
					DeviceIndex: i,
					Message:     "GPU temperature normal. Resuming AI tasks.",
				})
			},
			func(i int, temp float64) {
				o.emit(GPUEvent{
					Type:        "thermal_emergency",
					DeviceIndex: i,
					TempCelsius: temp,
					Message: fmt.Sprintf(
						"GPU emergency: %.0f°C. All GPU tasks cancelled to prevent damage.",
						temp,
					),
				})
			},
			log,
		)

		// OOM watcher
		o.watchers[idx] = recovery.NewWatcher(
			b, o.budgets[idx], o.preemptors[idx], idx,
			func(result recovery.RecoveryResult) {
				o.emit(GPUEvent{
					Type:        "oom",
					DeviceIndex: result.Event.DeviceIndex,
					TaskID:      result.TaskEvicted,
					Message:     result.UserMessage,
				})
			},
			log,
		)
	}

	return o, nil
}

// Start launches all background monitors.
func (o *Orchestrator) Start(ctx context.Context) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for idx := range o.devices {
		o.thermals[idx].Start(ctx)
		o.watchers[idx].Start(ctx)
	}
}

// ReserveLLM reserves VRAM for an LLM inference task.
// Returns a Reservation that must be Released when the task completes.
func (o *Orchestrator) ReserveLLM(taskID string, modelSizeBytes int64, priority int) (*budget.Reservation, error) {
	device, err := o.bestDeviceForLLM(modelSizeBytes)
	if err != nil {
		return nil, err
	}

	// Check thermal state
	if o.thermals[device.Index].IsThrottled() {
		return nil, fmt.Errorf("gpu: GPU is thermally throttled. Wait for it to cool down")
	}

	bytesNeeded := o.estimator.ForLLM(modelSizeBytes)
	return o.budgets[device.Index].Reserve(taskID, "llm", bytesNeeded, priority)
}

// ReserveImageGen reserves VRAM for an image generation task.
func (o *Orchestrator) ReserveImageGen(taskID, modelName string, width, height, priority int) (*budget.Reservation, error) {
	device, err := o.bestDeviceForGPU()
	if err != nil {
		return nil, err
	}

	if o.thermals[device.Index].IsThrottled() {
		return nil, fmt.Errorf("gpu: GPU is thermally throttled. Wait for it to cool down")
	}

	bytesNeeded := o.estimator.ForImageGen(modelName, width, height)
	return o.budgets[device.Index].Reserve(taskID, "image_gen", bytesNeeded, priority)
}

// ActivateReservation transitions a Pending reservation to Active.
func (o *Orchestrator) ActivateReservation(r *budget.Reservation) error {
	o.mu.RLock()
	mgr, ok := o.budgets[r.DeviceIndex]
	o.mu.RUnlock()
	if !ok {
		return fmt.Errorf("no budget manager for device %d", r.DeviceIndex)
	}
	return mgr.Activate(r.ID)
}

// ReleaseReservation releases an active or pending reservation.
func (o *Orchestrator) ReleaseReservation(r *budget.Reservation) {
	if r == nil {
		return
	}
	o.mu.RLock()
	mgr, ok := o.budgets[r.DeviceIndex]
	o.mu.RUnlock()
	if ok {
		mgr.Release(r.ID)
	}
}

// IsolateProcess applies GPU isolation to a command before running it.
func (o *Orchestrator) IsolateProcess(cmd *exec.Cmd, reservation *budget.Reservation, memFraction float64) (*isolation.IsolatedProcess, error) {
	return isolation.Isolate(cmd, isolation.IsolationConfig{
		DeviceIndex:    reservation.DeviceIndex,
		Backend:        o.backend.Name(),
		MemoryFraction: memFraction,
		AllowedEnvKeys: isolation.DefaultAllowedEnvKeys,
	})
}

// RegisterRunningTask registers a task for preemption eligibility.
func (o *Orchestrator) RegisterRunningTask(deviceIndex int, taskID string,
	cancel context.CancelFunc, suspend func() error) {
	if p, ok := o.preemptors[deviceIndex]; ok {
		p.RegisterTask(taskID, cancel, suspend)
	}
}

// DeregisterTask removes a completed task from preemption tracking.
func (o *Orchestrator) DeregisterTask(deviceIndex int, taskID string) {
	if p, ok := o.preemptors[deviceIndex]; ok {
		p.DeregisterTask(taskID)
	}
}

// Status returns a snapshot of all GPU device states.
func (o *Orchestrator) Status() []DeviceStatus {
	o.mu.RLock()
	defer o.mu.RUnlock()

	statuses := make([]DeviceStatus, 0, len(o.devices))
	for _, device := range o.devices {
		stats, _ := o.backend.VRAMStats(device.Index)
		temp, _ := o.backend.Temperature(device.Index)
		util, _ := o.backend.Utilization(device.Index)
		budgetStats := o.budgets[device.Index].Stats()
		thermalState := o.thermals[device.Index].State()

		statuses = append(statuses, DeviceStatus{
			Device:       device,
			VRAMStats:    stats,
			Temperature:  temp,
			Utilization:  util,
			BudgetStats:  budgetStats,
			ThermalState: string(thermalState),
		})
	}
	return statuses
}

// BackendName returns the name of the active GPU backend.
func (o *Orchestrator) BackendName() string {
	return o.backend.Name()
}

// DeviceStatus is the full state of a single GPU device.
type DeviceStatus struct {
	Device       backend.Device     `json:"device"`
	VRAMStats    backend.VRAMStats  `json:"vram_stats"`
	Temperature  float64            `json:"temperature"`
	Utilization  float64            `json:"utilization"`
	BudgetStats  budget.BudgetStats `json:"budget_stats"`
	ThermalState string             `json:"thermal_state"`
}

// bestDeviceForLLM finds the device with the most effective free VRAM.
func (o *Orchestrator) bestDeviceForLLM(modelSizeBytes int64) (*backend.Device, error) {
	needed := o.estimator.ForLLM(modelSizeBytes)
	for _, device := range o.devices {
		stats, err := o.backend.VRAMStats(device.Index)
		if err != nil {
			continue
		}
		if stats.EffectiveFree() >= needed {
			return &device, nil
		}
	}
	return nil, &budget.InsufficientVRAMError{Requested: needed}
}

// bestDeviceForGPU finds the device with the most free VRAM for GPU compute.
func (o *Orchestrator) bestDeviceForGPU() (*backend.Device, error) {
	if len(o.devices) == 0 {
		return nil, fmt.Errorf("no GPU devices available")
	}
	best := &o.devices[0]
	bestFree := int64(-1)
	for i := range o.devices {
		stats, err := o.backend.VRAMStats(o.devices[i].Index)
		if err != nil {
			continue
		}
		if stats.EffectiveFree() > bestFree {
			bestFree = stats.EffectiveFree()
			best = &o.devices[i]
		}
	}
	return best, nil
}

func (o *Orchestrator) emit(event GPUEvent) {
	if o.onEvent != nil {
		o.onEvent(event)
	}
}
