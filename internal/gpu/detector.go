package gpu

import (
	"context"
	"sync"
	"time"

	"github.com/openfabric/openfabric/internal/gpu/backend"
)

var (
	cachedGPU GPUInfo
	cacheMu   sync.RWMutex
	once      sync.Once

	orch   *Orchestrator
	orchMu sync.RWMutex
)

// SetOrchestrator registers a running Orchestrator to provide real-time GPU statistics.
func SetOrchestrator(o *Orchestrator) {
	orchMu.Lock()
	defer orchMu.Unlock()
	orch = o
}

// GetOrchestrator returns the registered Orchestrator, if any.
func GetOrchestrator() *Orchestrator {
	orchMu.RLock()
	defer orchMu.RUnlock()
	return orch
}

// GetGPUInfo returns the last detected GPU information.
func GetGPUInfo() GPUInfo {
	o := GetOrchestrator()
	if o != nil {
		status := o.Status()
		backendName := o.BackendName()

		info := GPUInfo{
			Available: len(status) > 0 && backendName != "cpu",
			Backend:   backendName,
			Generator: "none",
		}

		if len(status) > 0 {
			info.Name = status[0].Device.Name
			info.Driver = status[0].Device.DriverVersion
			if info.Driver == "" {
				if backendName == "metal" {
					info.Driver = "Metal"
				} else if backendName == "rocm" {
					info.Driver = "ROCm"
				} else {
					info.Driver = "CPU"
				}
			}

			var totalVRAM, freeVRAM, effFreeVRAM int64
			var maxTemp float64
			var maxUtil float64
			var thermalState = "normal"
			var devices []DeviceCapability

			for _, dev := range status {
				totalVRAM += dev.VRAMStats.Total
				freeVRAM += dev.VRAMStats.Free
				effFreeVRAM += dev.VRAMStats.EffectiveFree()
				if dev.Temperature > maxTemp {
					maxTemp = dev.Temperature
				}
				if dev.Utilization > maxUtil {
					maxUtil = dev.Utilization
				}
				if dev.ThermalState != "normal" && dev.ThermalState != "" {
					thermalState = dev.ThermalState
				}

				devices = append(devices, DeviceCapability{
					Index:             dev.Device.Index,
					Name:              dev.Device.Name,
					TotalVRAM:         dev.VRAMStats.Total,
					FreeVRAM:          dev.VRAMStats.Free,
					EffectiveFreeVRAM: dev.VRAMStats.EffectiveFree(),
					ReservedVRAM:      dev.VRAMStats.Reserved,
					TempCelsius:       dev.Temperature,
					Utilization:       dev.Utilization,
					Backend:           dev.Device.Backend,
				})
			}

			info.VRAM = totalVRAM
			info.VRAMFree = freeVRAM
			info.EffectiveFreeVRAM = effFreeVRAM
			info.Devices = devices
			info.ThermalState = thermalState
		} else {
			info.Driver = "CPU"
		}

		info.Generator = probeGenerators()
		return info
	}

	cacheMu.RLock()
	defer cacheMu.RUnlock()
	return cachedGPU
}

// StartDetector runs GPU auto-detection immediately and starts a background
// ticker to refresh free VRAM and active engines every 15 seconds.
func StartDetector(ctx context.Context) {
	once.Do(func() {
		// Run initial detection synchronously so it is available on startup
		Detect()

		go func() {
			ticker := time.NewTicker(15 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					Detect()
				}
			}
		}()
	})
}

// Detect executes GPU CLI commands and updates the cached GPUInfo.
func Detect() GPUInfo {
	b := backend.Detect()
	info := GPUInfo{
		Available: b.Name() != "cpu",
		Backend:   b.Name(),
		Generator: "none",
	}

	devices, err := b.Devices()
	if err == nil && len(devices) > 0 {
		info.Name = devices[0].Name
		info.Driver = devices[0].DriverVersion
		if info.Driver == "" {
			if b.Name() == "metal" {
				info.Driver = "Metal"
			} else if b.Name() == "rocm" {
				info.Driver = "ROCm"
			} else {
				info.Driver = "CPU"
			}
		}

		var totalVRAM, freeVRAM, effFreeVRAM int64
		var maxTemp float64
		var maxUtil float64
		var thermalState = "normal"
		var deviceCapabilities []DeviceCapability

		for _, dev := range devices {
			stats, err := b.VRAMStats(dev.Index)
			if err != nil {
				continue
			}

			temp, _ := b.Temperature(dev.Index)
			util, _ := b.Utilization(dev.Index)

			totalVRAM += stats.Total
			freeVRAM += stats.Free
			effFreeVRAM += stats.EffectiveFree()
			if temp > maxTemp {
				maxTemp = temp
			}
			if util > maxUtil {
				maxUtil = util
			}

			deviceCapabilities = append(deviceCapabilities, DeviceCapability{
				Index:             dev.Index,
				Name:              dev.Name,
				TotalVRAM:         stats.Total,
				FreeVRAM:          stats.Free,
				EffectiveFreeVRAM: stats.EffectiveFree(),
				ReservedVRAM:      stats.Reserved,
				TempCelsius:       temp,
				Utilization:       util,
				Backend:           dev.Backend,
			})
		}

		info.VRAM = totalVRAM
		info.VRAMFree = freeVRAM
		info.EffectiveFreeVRAM = effFreeVRAM
		info.Devices = deviceCapabilities

		// Determine fallback thermal state
		if maxTemp >= 92.0 {
			thermalState = "emergency"
		} else if maxTemp >= 85.0 {
			thermalState = "throttled"
		} else if maxTemp >= 75.0 {
			thermalState = "warning"
		}
		info.ThermalState = thermalState
	} else {
		info.Driver = "CPU"
	}

	// Probe for active local image generators
	if info.Available {
		info.Generator = probeGenerators()
	}

	cacheMu.Lock()
	cachedGPU = info
	cacheMu.Unlock()

	return info
}

// probeGenerators checks local ports or custom URL to identify active image generation APIs.
func probeGenerators() string {
	svc, err := DiscoverImageGenServices(GetConfiguredURL())
	if err != nil {
		return "none"
	}
	return svc.Type
}
