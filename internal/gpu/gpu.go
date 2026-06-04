package gpu

// DeviceCapability describes a single GPU device's state for the cluster.
type DeviceCapability struct {
	Index             int     `json:"index"`
	Name              string  `json:"name"`
	TotalVRAM         int64   `json:"total_vram"`
	FreeVRAM          int64   `json:"free_vram"` // raw free
	EffectiveFreeVRAM int64   `json:"effective_free_vram"` // after fragmentation headroom
	ReservedVRAM      int64   `json:"reserved_vram"`       // committed by active tasks
	TempCelsius       float64 `json:"temp_celsius"`
	Utilization       float64 `json:"utilization"`
	Backend           string  `json:"backend"`
}

// GPUInfo contains state and specifications for a single node's GPU.
type GPUInfo struct {
	Available         bool               `json:"available"`
	Name              string             `json:"name"`
	VRAM              int64              `json:"vram"`      // Total memory in bytes
	VRAMFree          int64              `json:"vram_free"` // Free memory in bytes
	Driver            string             `json:"driver"`    // e.g. "CUDA 12.3", "Metal"
	Backend           string             `json:"backend"`   // "cuda", "rocm", "metal", or "cpu"
	Generator         string             `json:"generator"` // "automatic1111", "comfyui", or "none"
	Devices           []DeviceCapability `json:"devices,omitempty"`
	EffectiveFreeVRAM int64              `json:"effective_free_vram"`
	ThermalState      string             `json:"thermal_state,omitempty"`
}

// ImageRequest defines the prompt and generation configuration.
type ImageRequest struct {
	Prompt         string `json:"prompt"`
	NegativePrompt string `json:"negative_prompt"`
	Width          int    `json:"width"`  // Default 1024
	Height         int    `json:"height"` // Default 1024
	Steps          int    `json:"steps"`  // Default 20
	Model          string `json:"model"`
}

// ImageResult defines the result of a successful generation.
type ImageResult struct {
	StoragePath string `json:"storage_path"`
	NodeID      string `json:"node_id"`
	ElapsedMS   int64  `json:"elapsed_ms"`
}
