package isolation_test

import (
	"os"
	"os/exec"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openfabric/openfabric/internal/gpu/isolation"
)

func TestIsolate_CUDA(t *testing.T) {
	cmd := exec.Command("echo", "hello")
	cfg := isolation.IsolationConfig{
		DeviceIndex:    1,
		Backend:        "cuda",
		MemoryFraction: 0.8,
		AllowedEnvKeys: []string{"PATH"},
	}

	// Set path in environment to ensure it passes through
	os.Setenv("PATH", "/usr/bin")
	defer os.Unsetenv("PATH")

	proc, err := isolation.Isolate(cmd, cfg)
	require.NoError(t, err)
	assert.NotNil(t, proc)

	// Check environment variables
	var hasCudaDevices, hasPath, hasOverhead bool
	for _, env := range cmd.Env {
		if env == "CUDA_VISIBLE_DEVICES=1" {
			hasCudaDevices = true
		}
		if env == "PATH=/usr/bin" {
			hasPath = true
		}
		if env == "OLLAMA_GPU_OVERHEAD=20" {
			hasOverhead = true
		}
	}
	assert.True(t, hasCudaDevices, "CUDA_VISIBLE_DEVICES not set")
	assert.True(t, hasPath, "PATH env variable not passed through")
	assert.True(t, hasOverhead, "OLLAMA_GPU_OVERHEAD not set based on memory fraction")

	// Check SysProcAttr on Unix
	if runtime.GOOS != "windows" {
		assert.NotNil(t, cmd.SysProcAttr)
		assert.True(t, cmd.SysProcAttr.Setpgid)
	}
}

func TestIsolate_ROCm(t *testing.T) {
	cmd := exec.Command("echo", "hello")
	cfg := isolation.IsolationConfig{
		DeviceIndex:    2,
		Backend:        "rocm",
		MemoryFraction: 0.5,
	}

	proc, err := isolation.Isolate(cmd, cfg)
	require.NoError(t, err)
	assert.NotNil(t, proc)

	var hasRocrDevices, hasHeapSize bool
	for _, env := range cmd.Env {
		if env == "ROCR_VISIBLE_DEVICES=2" {
			hasRocrDevices = true
		}
		if env == "GPU_MAX_HEAP_SIZE=50" {
			hasHeapSize = true
		}
	}
	assert.True(t, hasRocrDevices, "ROCR_VISIBLE_DEVICES not set")
	assert.True(t, hasHeapSize, "GPU_MAX_HEAP_SIZE not set")
}
