package backend

// CPUBackend is the fallback when no GPU is available.
// It reports zero VRAM so GPU tasks are never routed here.
type CPUBackend struct{}

func (b *CPUBackend) Name() string                             { return "cpu" }
func (b *CPUBackend) IsAvailable() bool                        { return true }
func (b *CPUBackend) Devices() ([]Device, error)               { return []Device{}, nil }
func (b *CPUBackend) Temperature(deviceIndex int) (float64, error) { return -1, ErrUnsupported }
func (b *CPUBackend) PowerUsage(deviceIndex int) (float64, error)  { return -1, ErrUnsupported }
func (b *CPUBackend) Utilization(deviceIndex int) (float64, error) { return -1, ErrUnsupported }
func (b *CPUBackend) SetMemoryFraction(deviceIndex int, fraction float64) error {
	return ErrUnsupported
}

func (b *CPUBackend) VRAMStats(deviceIndex int) (VRAMStats, error) {
	return VRAMStats{Total: 0, Free: 0, Used: 0, DeviceIndex: deviceIndex}, nil
}
