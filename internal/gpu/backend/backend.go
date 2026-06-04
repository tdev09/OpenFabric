package backend

import (
	"fmt"
	"time"
)

// Backend is the unified interface for all GPU hardware backends.
// CUDA, ROCm, Metal, and CPU fallback all implement this interface.
// All callers use this interface - no platform-specific code leaks out.
type Backend interface {
	// Name returns the backend identifier: "cuda", "rocm", "metal", "cpu".
	Name() string

	// Devices returns all GPU devices detected on this node.
	Devices() ([]Device, error)

	// VRAMStats returns current VRAM usage for a specific device.
	VRAMStats(deviceIndex int) (VRAMStats, error)

	// Temperature returns the current GPU temperature in Celsius.
	// Returns -1 if not supported by this backend.
	Temperature(deviceIndex int) (float64, error)

	// PowerUsage returns current power draw in watts.
	// Returns -1 if not supported by this backend.
	PowerUsage(deviceIndex int) (float64, error)

	// Utilization returns GPU compute utilization 0-100%.
	// Returns -1 if not supported by this backend.
	Utilization(deviceIndex int) (float64, error)

	// SetMemoryFraction configures the maximum fraction of VRAM
	// a single Ollama process may use. 0.0-1.0.
	// On backends that don't support this, returns ErrUnsupported.
	SetMemoryFraction(deviceIndex int, fraction float64) error

	// IsAvailable returns true if this backend can be used on this node.
	IsAvailable() bool
}

// Device represents a single GPU device.
type Device struct {
	Index         int    `json:"index"`        // 0-based device index
	Name          string `json:"name"`         // e.g. "NVIDIA RTX 3090", "AMD RX 7900 XTX", "Apple M3 Max"
	Backend       string `json:"backend"`      // "cuda", "rocm", "metal"
	TotalVRAM     int64  `json:"total_vram"`   // bytes
	Architecture  string `json:"architecture"` // e.g. "Ampere", "RDNA3", "Apple Silicon"
	DriverVersion string `json:"driver_version"`
}

// VRAMStats holds a point-in-time VRAM snapshot.
type VRAMStats struct {
	DeviceIndex   int       `json:"device_index"`
	Total         int64     `json:"total"` // total VRAM in bytes
	Used          int64     `json:"used"`  // currently allocated by all processes
	Free          int64     `json:"free"`  // Total - Used
	Reserved      int64     `json:"reserved"`
	Fragmentation int64     `json:"fragmentation"`
	Timestamp     time.Time `json:"timestamp"`
}

// EffectiveFree returns the VRAM safely available for new tasks.
// Subtracts Reserved and Fragmentation from Free.
func (v VRAMStats) EffectiveFree() int64 {
	effective := v.Free - v.Reserved - v.Fragmentation
	if effective < 0 {
		return 0
	}
	return effective
}

// ErrUnsupported is returned when a backend does not support an operation.
var ErrUnsupported = fmt.Errorf("operation not supported by this GPU backend")

// Detect returns the best available Backend for this node.
// Tries CUDA first, then ROCm, then Metal, then CPU fallback.
// Never returns nil - CPU is always available.
func Detect() Backend {
	// Try CUDA (NVIDIA)
	cuda := &CUDABackend{}
	if cuda.IsAvailable() {
		return cuda
	}

	// Try ROCm (AMD)
	rocm := &ROCmBackend{}
	if rocm.IsAvailable() {
		return rocm
	}

	// Try Metal (Apple Silicon)
	metal := &MetalBackend{}
	if metal.IsAvailable() {
		return metal
	}

	// CPU fallback - always succeeds
	return &CPUBackend{}
}
