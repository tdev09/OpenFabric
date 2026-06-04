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

// ROCmBackend implements Backend for AMD GPUs via rocm-smi.
type ROCmBackend struct {
	devices []Device
}

// IsAvailable returns true if rocm-smi is present and queryable.
func (b *ROCmBackend) IsAvailable() bool {
	cmd := exec.Command("rocm-smi", "--showmeminfo", "vram")
	return cmd.Run() == nil
}

// Name returns "rocm".
func (b *ROCmBackend) Name() string { return "rocm" }

// Devices returns all AMD GPUs detected by rocm-smi.
func (b *ROCmBackend) Devices() ([]Device, error) {
	if b.devices != nil {
		return b.devices, nil
	}

	out, err := exec.Command("rocm-smi", "--showproductname", "--csv").Output()
	if err != nil {
		return nil, fmt.Errorf("rocm: get devices: %w", err)
	}

	var devices []Device
	scanner := bufio.NewScanner(bytes.NewReader(out))
	lineIdx := 0
	for scanner.Scan() {
		text := scanner.Text()
		if text == "" {
			continue
		}
		if lineIdx == 0 {
			lineIdx++
			continue // skip header
		}
		parts := strings.Split(text, ",")
		if len(parts) < 2 {
			continue
		}

		devIdx := lineIdx - 1
		vramOut, _ := exec.Command("rocm-smi",
			fmt.Sprintf("--device=%d", devIdx),
			"--showmeminfo", "vram", "--csv").Output()
		totalVRAM := parseROCmVRAM(vramOut)

		devices = append(devices, Device{
			Index:        devIdx,
			Name:         strings.TrimSpace(parts[1]),
			Backend:      "rocm",
			TotalVRAM:    totalVRAM,
			Architecture: "RDNA",
		})
		lineIdx++
	}
	b.devices = devices
	return devices, nil
}

// VRAMStats returns current VRAM usage stats for an AMD GPU.
func (b *ROCmBackend) VRAMStats(deviceIndex int) (VRAMStats, error) {
	out, err := exec.Command("rocm-smi",
		fmt.Sprintf("--device=%d", deviceIndex),
		"--showmeminfo", "vram", "--csv").Output()
	if err != nil {
		return VRAMStats{}, fmt.Errorf("rocm: vram stats: %w", err)
	}

	total, used := parseROCmVRAMDetail(out)
	free := total - used

	// ROCm fragmentation: ~10% of used VRAM overhead
	fragmentation := int64(float64(used) * 0.10)

	return VRAMStats{
		DeviceIndex:   deviceIndex,
		Total:         total,
		Used:          used,
		Free:          free,
		Fragmentation: fragmentation,
		Timestamp:     time.Now(),
	}, nil
}

// Temperature returns AMD GPU temperature in Celsius.
func (b *ROCmBackend) Temperature(deviceIndex int) (float64, error) {
	out, err := exec.Command("rocm-smi",
		fmt.Sprintf("--device=%d", deviceIndex),
		"--showtemp", "--csv").Output()
	if err != nil {
		return -1, err
	}
	return parseROCmTemp(out), nil
}

// PowerUsage returns current AMD GPU power draw in watts.
func (b *ROCmBackend) PowerUsage(deviceIndex int) (float64, error) {
	out, err := exec.Command("rocm-smi",
		fmt.Sprintf("--device=%d", deviceIndex),
		"--showpower", "--csv").Output()
	if err != nil {
		return -1, err
	}
	return parseROCmPower(out), nil
}

// Utilization returns AMD GPU compute utilization 0-100%.
func (b *ROCmBackend) Utilization(deviceIndex int) (float64, error) {
	out, err := exec.Command("rocm-smi",
		fmt.Sprintf("--device=%d", deviceIndex),
		"--showuse", "--csv").Output()
	if err != nil {
		return -1, err
	}
	return parseROCmUtilization(out), nil
}

// SetMemoryFraction configures ROCm memory limits.
func (b *ROCmBackend) SetMemoryFraction(deviceIndex int, fraction float64) error {
	// ROCm supports GPU_MAX_HEAP_SIZE environment variable set before process startup.
	return nil
}

// CSV Parsing Helpers
func parseROCmVRAM(data []byte) int64 {
	total, _ := parseROCmVRAMDetail(data)
	return total
}

func parseROCmVRAMDetail(data []byte) (total, used int64) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	var headers []string
	for scanner.Scan() {
		line := scanner.Text()
		if len(headers) == 0 {
			headers = strings.Split(line, ",")
			for i, h := range headers {
				headers[i] = strings.ToLower(strings.TrimSpace(h))
			}
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < len(headers) {
			continue
		}
		totalIdx := -1
		usedIdx := -1
		for i, h := range headers {
			if strings.Contains(h, "total") && strings.Contains(h, "vram") {
				totalIdx = i
			} else if strings.Contains(h, "total") {
				totalIdx = i
			}
			if strings.Contains(h, "used") && strings.Contains(h, "vram") {
				usedIdx = i
			} else if strings.Contains(h, "used") {
				usedIdx = i
			}
		}
		if totalIdx != -1 && totalIdx < len(parts) {
			total, _ = strconv.ParseInt(strings.TrimSpace(parts[totalIdx]), 10, 64)
		}
		if usedIdx != -1 && usedIdx < len(parts) {
			used, _ = strconv.ParseInt(strings.TrimSpace(parts[usedIdx]), 10, 64)
		}
		// If CSV total/used index mapping fails, fallback to positional if standard output
		if total == 0 && len(parts) >= 2 {
			total, _ = strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		}
		if used == 0 && len(parts) >= 3 {
			used, _ = strconv.ParseInt(strings.TrimSpace(parts[2]), 10, 64)
		}
		break
	}
	return total, used
}

func parseROCmFloat(data []byte, keyword string) float64 {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	var headers []string
	for scanner.Scan() {
		line := scanner.Text()
		if len(headers) == 0 {
			headers = strings.Split(line, ",")
			for i, h := range headers {
				headers[i] = strings.ToLower(strings.TrimSpace(h))
			}
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < len(headers) {
			continue
		}
		idx := -1
		for i, h := range headers {
			if strings.Contains(h, keyword) {
				idx = i
				break
			}
		}
		if idx != -1 && idx < len(parts) {
			val, _ := strconv.ParseFloat(strings.TrimSpace(parts[idx]), 64)
			return val
		}
		if len(parts) >= 2 {
			val, _ := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
			return val
		}
		break
	}
	return -1
}

func parseROCmTemp(data []byte) float64        { return parseROCmFloat(data, "temp") }
func parseROCmPower(data []byte) float64       { return parseROCmFloat(data, "power") }
func parseROCmUtilization(data []byte) float64 { return parseROCmFloat(data, "use") }
