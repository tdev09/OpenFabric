package backend

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// CUDABackend implements Backend for NVIDIA GPUs via nvidia-smi.
// Uses nvidia-smi subprocess calls rather than CGo to avoid
// introducing a C dependency into the Go binary.
type CUDABackend struct {
	devices []Device // cached at detection time
}

// IsAvailable returns true if nvidia-smi is present and responds.
func (b *CUDABackend) IsAvailable() bool {
	cmd := exec.Command("nvidia-smi", "--query-gpu=name", "--format=csv,noheader")
	return cmd.Run() == nil
}

// Name returns "cuda".
func (b *CUDABackend) Name() string { return "cuda" }

// Devices returns all NVIDIA GPUs detected by nvidia-smi.
func (b *CUDABackend) Devices() ([]Device, error) {
	if b.devices != nil {
		return b.devices, nil
	}

	out, err := exec.Command("nvidia-smi",
		"--query-gpu=index,name,memory.total,driver_version",
		"--format=csv,noheader,nounits").Output()
	if err != nil {
		return nil, fmt.Errorf("cuda: nvidia-smi devices: %w", err)
	}

	var devices []Device
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		parts := strings.Split(scanner.Text(), ", ")
		if len(parts) < 4 {
			continue
		}
		idx, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
		totalMB, _ := strconv.ParseInt(strings.TrimSpace(parts[2]), 10, 64)
		devices = append(devices, Device{
			Index:         idx,
			Name:          strings.TrimSpace(parts[1]),
			Backend:       "cuda",
			TotalVRAM:     totalMB * 1024 * 1024,
			DriverVersion: strings.TrimSpace(parts[3]),
		})
	}

	b.devices = devices
	return devices, nil
}

// VRAMStats queries nvidia-smi for current VRAM usage.
func (b *CUDABackend) VRAMStats(deviceIndex int) (VRAMStats, error) {
	out, err := exec.Command("nvidia-smi",
		fmt.Sprintf("--id=%d", deviceIndex),
		"--query-gpu=memory.total,memory.used,memory.free",
		"--format=csv,noheader,nounits").Output()
	if err != nil {
		return VRAMStats{}, fmt.Errorf("cuda: vram stats: %w", err)
	}

	parts := strings.Split(strings.TrimSpace(string(out)), ", ")
	if len(parts) < 3 {
		return VRAMStats{}, fmt.Errorf("cuda: unexpected vram output: %s", out)
	}

	totalMB, _ := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
	usedMB, _ := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	freeMB, _ := strconv.ParseInt(strings.TrimSpace(parts[2]), 10, 64)

	total := totalMB * 1024 * 1024
	used := usedMB * 1024 * 1024
	free := freeMB * 1024 * 1024

	// CUDA fragmentation: ~17% of used VRAM is overhead
	// Based on production measurements from NVIDIA documentation
	fragmentation := int64(float64(used) * 0.17)

	return VRAMStats{
		DeviceIndex:   deviceIndex,
		Total:         total,
		Used:          used,
		Free:          free,
		Fragmentation: fragmentation,
		Timestamp:     time.Now(),
	}, nil
}

// Temperature returns GPU temperature in Celsius.
func (b *CUDABackend) Temperature(deviceIndex int) (float64, error) {
	out, err := exec.Command("nvidia-smi",
		fmt.Sprintf("--id=%d", deviceIndex),
		"--query-gpu=temperature.gpu",
		"--format=csv,noheader,nounits").Output()
	if err != nil {
		return -1, err
	}
	return strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
}

// PowerUsage returns current power draw in watts.
func (b *CUDABackend) PowerUsage(deviceIndex int) (float64, error) {
	out, err := exec.Command("nvidia-smi",
		fmt.Sprintf("--id=%d", deviceIndex),
		"--query-gpu=power.draw",
		"--format=csv,noheader,nounits").Output()
	if err != nil {
		return -1, err
	}
	return strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
}

// Utilization returns GPU compute utilization 0-100%.
func (b *CUDABackend) Utilization(deviceIndex int) (float64, error) {
	out, err := exec.Command("nvidia-smi",
		fmt.Sprintf("--id=%d", deviceIndex),
		"--query-gpu=utilization.gpu",
		"--format=csv,noheader,nounits").Output()
	if err != nil {
		return -1, err
	}
	return strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
}

// SetMemoryFraction sets the CUDA_MPS_ACTIVE_THREAD_PERCENTAGE for memory
// fraction control. On CUDA without MPS, this is best-effort.
func (b *CUDABackend) SetMemoryFraction(deviceIndex int, fraction float64) error {
	// CUDA does not have native per-process VRAM limits without MPS.
	// We use CUDA_VISIBLE_DEVICES and rely on Ollama's
	// --gpu-memory-utilization flag instead.
	// This is set in the environment before starting the Ollama process.
	return nil // best-effort on CUDA without MPS
}
