package thermal

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/openfabric/openfabric/internal/gpu/backend"
)

// Monitor continuously samples GPU temperature and enforces thermal policy.
type Monitor struct {
	mu          sync.RWMutex
	backend     backend.Backend
	policy      ThermalPolicy
	state       ThermalState
	lastTemp    map[int]float64 // deviceIndex → last temperature
	onThrottle  func(deviceIndex int, temp float64)
	onRecover   func(deviceIndex int)
	onEmergency func(deviceIndex int, temp float64)
	log         *zap.Logger
}

// NewMonitor creates a thermal monitor for all devices on a backend.
func NewMonitor(
	b backend.Backend,
	policy ThermalPolicy,
	onThrottle func(int, float64),
	onRecover func(int),
	onEmergency func(int, float64),
	log *zap.Logger,
) *Monitor {
	return &Monitor{
		backend:     b,
		policy:      policy,
		state:       ThermalNormal,
		lastTemp:    make(map[int]float64),
		onThrottle:  onThrottle,
		onRecover:   onRecover,
		onEmergency: onEmergency,
		log:         log,
	}
}

// Start begins the thermal monitoring loop.
// Samples all devices every 5 seconds.
func (m *Monitor) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		// Sample once immediately
		m.Sample()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.Sample()
			}
		}
	}()
}

// State returns the current thermal management state.
func (m *Monitor) State() ThermalState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state
}

// IsThrottled returns true if new GPU tasks should be rejected.
func (m *Monitor) IsThrottled() bool {
	s := m.State()
	return s == ThermalThrottled || s == ThermalEmergency
}

// TemperatureOf returns the last known temperature for a device.
func (m *Monitor) TemperatureOf(deviceIndex int) float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastTemp[deviceIndex]
}

// Sample reads temperature from all devices and updates state.
func (m *Monitor) Sample() {
	devices, err := m.backend.Devices()
	if err != nil || len(devices) == 0 {
		return
	}

	for _, device := range devices {
		temp, err := m.backend.Temperature(device.Index)
		if err != nil || temp < 0 {
			continue // backend doesn't support temperature or query failed
		}

		m.mu.Lock()
		m.lastTemp[device.Index] = temp
		var triggeredCallback string

		switch {
		case temp >= m.policy.EmergencyCelsius:
			if m.state != ThermalEmergency {
				m.state = ThermalEmergency
				triggeredCallback = "emergency"
				m.log.Error("GPU emergency temperature reached - cancelling all tasks",
					zap.Int("device", device.Index),
					zap.Float64("temp_c", temp),
					zap.Float64("threshold_c", m.policy.EmergencyCelsius),
				)
			}

		case temp >= m.policy.ThrottleCelsius:
			if m.state == ThermalNormal || m.state == ThermalWarning {
				m.state = ThermalThrottled
				triggeredCallback = "throttle"
				m.log.Warn("GPU temperature throttling new tasks",
					zap.Int("device", device.Index),
					zap.Float64("temp_c", temp),
					zap.Float64("threshold_c", m.policy.ThrottleCelsius),
				)
			}

		case temp >= m.policy.WarnCelsius:
			if m.state == ThermalNormal {
				m.state = ThermalWarning
				m.log.Warn("GPU temperature warning",
					zap.Int("device", device.Index),
					zap.Float64("temp_c", temp),
				)
			}

		case temp < m.policy.RecoveryCelsius:
			if m.state == ThermalThrottled || m.state == ThermalWarning || m.state == ThermalEmergency {
				m.state = ThermalNormal
				triggeredCallback = "recover"
				m.log.Info("GPU temperature recovered - resuming normal operation",
					zap.Int("device", device.Index),
					zap.Float64("temp_c", temp),
				)
			}
		}

		m.mu.Unlock()

		// Fire callbacks outside the lock to prevent deadlock
		if triggeredCallback != "" {
			switch triggeredCallback {
			case "throttle":
				if m.onThrottle != nil {
					m.onThrottle(device.Index, temp)
				}
			case "recover":
				if m.onRecover != nil {
					m.onRecover(device.Index)
				}
			case "emergency":
				if m.onEmergency != nil {
					m.onEmergency(device.Index, temp)
				}
			}
		}
	}
}
