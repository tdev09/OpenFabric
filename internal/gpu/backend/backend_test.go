package backend_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/openfabric/openfabric/internal/gpu/backend"
)

// MockBackend for testing without real GPU hardware.
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

func NewMockBackend(totalVRAM int64) *MockBackend {
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

func TestDetect_ReturnsNonNil(t *testing.T) {
	b := backend.Detect()
	assert.NotNil(t, b)
	// CPU fallback always returns a non-nil backend
	assert.NotEmpty(t, b.Name())
}

func TestCPUBackend_ZeroVRAM(t *testing.T) {
	b := &backend.CPUBackend{}
	stats, err := b.VRAMStats(0)
	assert.NoError(t, err)
	assert.Equal(t, int64(0), stats.Total)
	assert.Equal(t, int64(0), stats.EffectiveFree())
}
