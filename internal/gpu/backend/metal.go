package backend

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// MetalBackend implements Backend for Apple Silicon using Metal.
// Apple Silicon uses unified memory - VRAM and RAM are the same pool.
// This means VRAM "free" is the same as system free memory.
type MetalBackend struct{}

// IsAvailable returns true on macOS with Apple Silicon.
func (b *MetalBackend) IsAvailable() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	// Check for Apple Silicon via sysctl
	out, err := exec.Command("sysctl", "-n", "hw.optional.arm64").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "1"
}

// Name returns "metal".
func (b *MetalBackend) Name() string { return "metal" }

// Devices returns the unified memory pool as a single virtual GPU device.
func (b *MetalBackend) Devices() ([]Device, error) {
	// Get total unified memory
	out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return nil, fmt.Errorf("metal: get memory size: %w", err)
	}
	totalBytes, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("metal: parse memory size: %w", err)
	}

	// Get chip name
	chipOut, _ := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").Output()
	chipName := strings.TrimSpace(string(chipOut))
	if chipName == "" {
		chipName = "Apple Silicon GPU"
	}

	return []Device{
		{
			Index:        0,
			Name:         chipName,
			Backend:      "metal",
			TotalVRAM:    totalBytes, // unified: total RAM = total "VRAM"
			Architecture: "Apple Silicon",
		},
	}, nil
}

// VRAMStats returns unified memory usage.
// On Apple Silicon, VRAM and RAM share the same physical memory.
//
// IMPORTANT: macOS memory accounting - "Pages free" from vm_stat is almost
// always very small (< 10 MB) because macOS aggressively uses spare memory for
// file caching and compression. The correct reclaimable figure is:
//
//	free + speculative (both are immediately available to new allocations)
//
// Using only "Pages free" as the free signal causes constant false OOM triggers.
func (b *MetalBackend) VRAMStats(deviceIndex int) (VRAMStats, error) {
	out, err := exec.Command("vm_stat").Output()
	if err != nil {
		return VRAMStats{}, fmt.Errorf("metal: vm_stat: %w", err)
	}

	pageSize := int64(4096) // macOS standard page size
	var freePages, speculativePages, compressedPages int64

	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		parseField := func(prefix string) int64 {
			if strings.HasPrefix(line, prefix) {
				parts := strings.Fields(line)
				if len(parts) >= 3 {
					val := strings.TrimRight(parts[len(parts)-1], ".")
					n, _ := strconv.ParseInt(val, 10, 64)
					return n
				}
			}
			return 0
		}
		if v := parseField("Pages free:"); v > 0 {
			freePages = v
		}
		if v := parseField("Pages speculative:"); v > 0 {
			speculativePages = v
		}
		if v := parseField("Pages stored in compressor:"); v > 0 {
			compressedPages = v
		}
		_ = compressedPages // reserved for future pressure scoring
	}

	// Get total memory
	totalOut, _ := exec.Command("sysctl", "-n", "hw.memsize").Output()
	total, _ := strconv.ParseInt(strings.TrimSpace(string(totalOut)), 10, 64)

	// Reclaimable = free + speculative pages (both available on demand).
	// This is the correct "free" signal on macOS; using only Pages free
	// always shows near-zero and triggers false OOM warnings.
	reclaimable := (freePages + speculativePages) * pageSize
	used := total - reclaimable

	// Metal/unified memory fragmentation is lower than CUDA (~5%)
	fragmentation := int64(float64(used) * 0.05)

	return VRAMStats{
		DeviceIndex:   0,
		Total:         total,
		Used:          used,
		Free:          reclaimable,
		Fragmentation: fragmentation,
		Timestamp:     time.Now(),
	}, nil
}

// Temperature returns macOS Apple Silicon GPU temperature if available.
func (b *MetalBackend) Temperature(deviceIndex int) (float64, error) {
	// Use powermetrics for temperature on Apple Silicon
	out, err := exec.Command("sudo", "powermetrics", "-n", "1",
		"--samplers", "gpu_power", "-i", "500").Output()
	if err != nil {
		return -1, ErrUnsupported
	}

	// Parse GPU die temperature from powermetrics output
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "GPU die temperature") {
			parts := strings.Fields(line)
			for i, p := range parts {
				if p == "temperature" && i+2 < len(parts) {
					temp, err := strconv.ParseFloat(parts[i+2], 64)
					if err == nil {
						return temp, nil
					}
				}
			}
		}
	}

	return -1, ErrUnsupported
}

// PowerUsage returns -1 - powermetrics requires sudo on macOS.
func (b *MetalBackend) PowerUsage(deviceIndex int) (float64, error) {
	return -1, ErrUnsupported
}

// Utilization returns GPU utilization via system_profiler.
func (b *MetalBackend) Utilization(deviceIndex int) (float64, error) {
	// Apple doesn't expose GPU utilization via a simple CLI.
	// Return -1 to indicate unavailable.
	return -1, ErrUnsupported
}

// SetMemoryFraction is not supported on Metal.
func (b *MetalBackend) SetMemoryFraction(deviceIndex int, fraction float64) error {
	return ErrUnsupported
}
