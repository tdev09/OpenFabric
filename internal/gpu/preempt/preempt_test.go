package preempt_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/openfabric/openfabric/internal/gpu/backend"
	"github.com/openfabric/openfabric/internal/gpu/budget"
	"github.com/openfabric/openfabric/internal/gpu/preempt"
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

func TestPreempt_SuccessfulPreemption(t *testing.T) {
	log := zaptest.NewLogger(t)
	b := newMockBackend(24 * 1024 * 1024 * 1024)
	mgr := budget.NewManager(0, b, log)
	preemptor := preempt.NewPreemptor(mgr, log)

	// Set up an active low-priority reservation
	r1, err := mgr.Reserve("low-task", "llm", 4*1024*1024*1024, 10)
	require.NoError(t, err)
	require.NoError(t, mgr.Activate(r1.ID))

	cancelled := false
	cancelFn := func() {
		cancelled = true
	}

	preemptor.RegisterTask("low-task", cancelFn, nil)

	// High priority task requests VRAM preemption
	req := preempt.PreemptRequest{
		RequiredBytes:     4 * 1024 * 1024 * 1024,
		RequesterTaskID:   "high-task",
		RequesterPriority: 90,
		DeviceIndex:       0,
	}

	res, err := preemptor.Preempt(req)
	require.NoError(t, err)
	assert.NotNil(t, res)
	assert.Equal(t, "low-task", res.PreemptedTaskID)
	assert.Equal(t, "cancelled", res.Method)
	assert.True(t, cancelled)

	// Check that low-priority reservation is released
	stats := mgr.Stats()
	assert.Equal(t, 0, stats.ActiveReservations)
}

func TestPreempt_LowerOrEqualPriorityNotPreempted(t *testing.T) {
	log := zaptest.NewLogger(t)
	b := newMockBackend(24 * 1024 * 1024 * 1024)
	mgr := budget.NewManager(0, b, log)
	preemptor := preempt.NewPreemptor(mgr, log)

	r1, err := mgr.Reserve("mid-task", "llm", 4*1024*1024*1024, 50)
	require.NoError(t, err)
	require.NoError(t, mgr.Activate(r1.ID))

	preemptor.RegisterTask("mid-task", func() {}, nil)

	// Attempt preemption with lower priority (30 < 50)
	req := preempt.PreemptRequest{
		RequiredBytes:     4 * 1024 * 1024 * 1024,
		RequesterTaskID:   "low-task",
		RequesterPriority: 30,
		DeviceIndex:       0,
	}

	_, err = preemptor.Preempt(req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "priority")
}
