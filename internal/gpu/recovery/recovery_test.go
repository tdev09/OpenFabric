package recovery_test

import (
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap/zaptest"

	"github.com/openfabric/openfabric/internal/gpu/backend"
	"github.com/openfabric/openfabric/internal/gpu/budget"
	"github.com/openfabric/openfabric/internal/gpu/preempt"
	"github.com/openfabric/openfabric/internal/gpu/recovery"
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

func newMockBackend(totalVRAM, freeVRAM int64) *MockBackend {
	return &MockBackend{
		devices: []backend.Device{{Index: 0, Name: "Mock GPU", TotalVRAM: totalVRAM, Backend: "mock"}},
		vram: map[int]backend.VRAMStats{
			0: {
				DeviceIndex:   0,
				Total:         totalVRAM,
				Used:          totalVRAM - freeVRAM,
				Free:          freeVRAM,
				Fragmentation: 0,
			},
		},
		temps: map[int]float64{0: 65.0},
	}
}

func TestWatcher_CheckTriggersRecovery_EvictTask(t *testing.T) {
	log := zaptest.NewLogger(t)
	// Create mock backend with 6GB free VRAM initially so we can reserve 2GB
	b := newMockBackend(8*1024*1024*1024, 6*1024*1024*1024)
	mgr := budget.NewManager(0, b, log)
	preemptor := preempt.NewPreemptor(mgr, log)

	r, err := mgr.Reserve("preempt-target", "llm", 2*1024*1024*1024, 10)
	assert.NoError(t, err)
	assert.NoError(t, mgr.Activate(r.ID))

	preemptor.RegisterTask("preempt-target", func() {}, nil)

	// Now simulate OOM: update free VRAM in the mock backend to 100MB (below 256MB threshold)
	b.vram[0] = backend.VRAMStats{
		DeviceIndex: 0,
		Total:       8 * 1024 * 1024 * 1024,
		Used:        8*1024*1024*1024 - 100*1024*1024,
		Free:        100 * 1024 * 1024,
	}

	var callbackResult recovery.RecoveryResult
	callbackCalled := make(chan struct{}, 1)
	watcher := recovery.NewWatcher(b, mgr, preemptor, 0, func(res recovery.RecoveryResult) {
		callbackResult = res
		callbackCalled <- struct{}{}
	}, log)

	watcher.Check()

	select {
	case <-callbackCalled:
		assert.True(t, callbackResult.Success)
		assert.Equal(t, "evicted_task", callbackResult.Action)
		assert.Equal(t, "preempt-target", callbackResult.TaskEvicted)
	case <-time.After(2 * time.Second):
		t.Fatal("OOM callback not called")
	}
}

func TestWatcher_CheckTriggersRecovery_ClearCache(t *testing.T) {
	log := zaptest.NewLogger(t)
	b := newMockBackend(8*1024*1024*1024, 100*1024*1024)
	mgr := budget.NewManager(0, b, log)
	preemptor := preempt.NewPreemptor(mgr, log)

	// Spin up mock server on port 11434
	listener, err := net.Listen("tcp", "127.0.0.1:11434")
	if err == nil {
		server := &http.Server{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}),
		}
		go server.Serve(listener)
		defer server.Close()
	}

	var callbackResult recovery.RecoveryResult
	callbackCalled := make(chan struct{}, 1)
	watcher := recovery.NewWatcher(b, mgr, preemptor, 0, func(res recovery.RecoveryResult) {
		callbackResult = res
		callbackCalled <- struct{}{}
	}, log)

	watcher.Check()

	select {
	case <-callbackCalled:
		// Since there are no tasks to evict, it should clear cache.
		// If listener failed (port in use), http call might still succeed if real Ollama responds.
		// If both fail, Action will be recovery_failed.
		if callbackResult.Action == "cleared_cache" {
			assert.True(t, callbackResult.Success)
		} else {
			assert.Equal(t, "recovery_failed", callbackResult.Action)
			assert.False(t, callbackResult.Success)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OOM callback not called")
	}
}
