package budget_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/openfabric/openfabric/internal/gpu/backend"
	"github.com/openfabric/openfabric/internal/gpu/budget"
)

type MockBackend struct {
	devices []backend.Device
	vram    map[int]backend.VRAMStats
	temps   map[int]float64
}

func (m *MockBackend) Name() string                       { return "mock" }
func (m *MockBackend) IsAvailable() bool                  { return true }
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

func (m *MockBackend) PowerUsage(int) (float64, error)      { return 150.0, nil }
func (m *MockBackend) Utilization(int) (float64, error)     { return 75.0, nil }
func (m *MockBackend) SetMemoryFraction(int, float64) error { return nil }

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

func TestBudget_ReservationSuccess(t *testing.T) {
	log := zaptest.NewLogger(t)
	b := newMockBackend(24 * 1024 * 1024 * 1024) // 24GB
	mgr := budget.NewManager(0, b, log)

	r, err := mgr.Reserve("task-1", "llm", 4*1024*1024*1024, 50) // 4GB
	require.NoError(t, err)
	assert.NotNil(t, r)
	assert.Equal(t, budget.StatusPending, r.Status)
}

func TestBudget_ReservationFailsWhenInsufficientVRAM(t *testing.T) {
	log := zaptest.NewLogger(t)
	b := newMockBackend(4 * 1024 * 1024 * 1024) // 4GB GPU
	mgr := budget.NewManager(0, b, log)

	_, err := mgr.Reserve("task-1", "llm", 40*1024*1024*1024, 50) // 40GB request
	require.Error(t, err)

	var vramErr *budget.InsufficientVRAMError
	assert.ErrorAs(t, err, &vramErr)
	assert.NotEmpty(t, vramErr.UserMessage())
}

func TestBudget_MultipleReservationsRespectBudget(t *testing.T) {
	log := zaptest.NewLogger(t)
	// GPU with 24GB total VRAM (used 6GB, free 18GB)
	b := newMockBackend(24 * 1024 * 1024 * 1024)
	mgr := budget.NewManager(0, b, log)

	// Reserve 4GB
	r1, err := mgr.Reserve("task-1", "llm", 4*1024*1024*1024, 50)
	require.NoError(t, err)
	require.NoError(t, mgr.Activate(r1.ID))

	// Reserve another 4GB - should succeed
	r2, err := mgr.Reserve("task-2", "image_gen", 4*1024*1024*1024, 30)
	require.NoError(t, err)
	require.NoError(t, mgr.Activate(r2.ID))

	// Release first reservation
	mgr.Release(r1.ID)

	// Now we release and should have budget again
	_, err = mgr.Reserve("task-3", "llm", 4*1024*1024*1024, 50)
	require.NoError(t, err)
}

func TestVRAMEstimator_LLMHeadroom(t *testing.T) {
	e := budget.VRAMEstimator{}
	modelSize := int64(20 * 1024 * 1024 * 1024) // 20GB model
	estimated := e.ForLLM(modelSize)
	assert.Greater(t, estimated, modelSize, "LLM estimate must include headroom")
	assert.LessOrEqual(t, estimated, int64(float64(modelSize)*1.5),
		"LLM headroom should not exceed 1.5x")
}
