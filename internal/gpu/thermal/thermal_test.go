package thermal_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap/zaptest"

	"github.com/openfabric/openfabric/internal/gpu/backend"
	"github.com/openfabric/openfabric/internal/gpu/thermal"
)

type MockBackend struct {
	devices []backend.Device
	vram    map[int]backend.VRAMStats
	temps   map[int]float64
}

func (m *MockBackend) Name() string      { return "mock" }
func (m *MockBackend) IsAvailable() bool { return true }
func (m *MockBackend) Devices() ([]backend.Device, error) { return m.devices, nil }

func (m *MockBackend) VRAMStats(idx int) (backend.VRAMStats, error) {
	stats, ok := m.vram[idx]
	if !ok {
		return backend.VRAMStats{}, fmt.Errorf("device %d not found", idx)
	}
	return stats, nil
}

func (m *MockBackend) Temperature(idx int) (float64, error) {
	return m.temps[idx], nil
}

func (m *MockBackend) PowerUsage(int) (float64, error)       { return 150.0, nil }
func (m *MockBackend) Utilization(int) (float64, error)      { return 75.0, nil }
func (m *MockBackend) SetMemoryFraction(int, float64) error  { return nil }

func newMockBackend(totalVRAM int64) *MockBackend {
	return &MockBackend{
		devices: []backend.Device{{Index: 0, Name: "Mock GPU", TotalVRAM: totalVRAM, Backend: "mock"}},
		vram: map[int]backend.VRAMStats{
			0: {
				DeviceIndex:   0,
				Total:         totalVRAM,
				Used:          totalVRAM / 4,
				Free:          totalVRAM * 3 / 4,
				Fragmentation: int64(float64(totalVRAM/4) * 0.17),
			},
		},
		temps: map[int]float64{0: 65.0},
	}
}

func TestThermalMonitor_ThrottleAtThreshold(t *testing.T) {
	log := zaptest.NewLogger(t)
	throttled := make(chan float64, 1)

	b := newMockBackend(24 * 1024 * 1024 * 1024)
	b.temps[0] = 86.0 // above ThrottleCelsius (85°C)

	m := thermal.NewMonitor(b, thermal.DefaultPolicy,
		func(idx int, temp float64) { throttled <- temp },
		nil, nil, log,
	)

	m.Sample() // force one sample

	assert.Equal(t, thermal.ThermalThrottled, m.State())
	assert.True(t, m.IsThrottled())

	select {
	case temp := <-throttled:
		assert.Equal(t, 86.0, temp)
	case <-time.After(time.Second):
		t.Fatal("throttle callback not called")
	}
}

func TestThermalMonitor_RecoveryAfterCooling(t *testing.T) {
	log := zaptest.NewLogger(t)
	recovered := make(chan struct{}, 1)

	b := newMockBackend(24 * 1024 * 1024 * 1024)
	b.temps[0] = 86.0 // throttled

	m := thermal.NewMonitor(b, thermal.DefaultPolicy,
		nil,
		func(idx int) { recovered <- struct{}{} },
		nil, log,
	)

	m.Sample()
	assert.Equal(t, thermal.ThermalThrottled, m.State())

	// GPU cools down
	b.temps[0] = 65.0 // below RecoveryCelsius (70°C)
	m.Sample()

	assert.Equal(t, thermal.ThermalNormal, m.State())
	assert.False(t, m.IsThrottled())
}

func TestThermalMonitor_EmergencyStop(t *testing.T) {
	log := zaptest.NewLogger(t)
	emergency := make(chan float64, 1)

	b := newMockBackend(24 * 1024 * 1024 * 1024)
	b.temps[0] = 95.0 // above EmergencyCelsius (92°C)

	m := thermal.NewMonitor(b, thermal.DefaultPolicy,
		nil, nil,
		func(idx int, temp float64) { emergency <- temp },
		log,
	)

	m.Sample()

	assert.Equal(t, thermal.ThermalEmergency, m.State())
	select {
	case temp := <-emergency:
		assert.Equal(t, 95.0, temp)
	case <-time.After(time.Second):
		t.Fatal("emergency callback not called")
	}
}
