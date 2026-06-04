package gpu

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// OllamaProcessType describes how Ollama is running.
type OllamaProcessType string

const (
	OllamaChild  OllamaProcessType = "child"  // started by OpenFabric
	OllamaSystem OllamaProcessType = "system" // systemd/launchd daemon
	OllamaManual OllamaProcessType = "manual" // started by user separately
)

// OllamaGPUWarning represents a non-fatal warning when GPU cannot be configured.
type OllamaGPUWarning struct {
	Message string
}

func (e *OllamaGPUWarning) Error() string {
	return e.Message
}

func isOllamaOurChild() bool {
	// OpenFabric does not manage Ollama as a child process currently
	return false
}

// DetectOllamaProcessType checks if Ollama is our child or external.
func DetectOllamaProcessType() OllamaProcessType {
	// Check if we started it (tracked in our process registry/variables)
	if isOllamaOurChild() {
		return OllamaChild
	}

	// Check if it's a systemd service (Linux)
	if runtime.GOOS == "linux" {
		out, err := exec.Command("systemctl", "is-active", "ollama").Output()
		if err == nil && strings.TrimSpace(string(out)) == "active" {
			return OllamaSystem
		}
	}

	// Check if it's a launchd service on macOS
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("launchctl", "list", "com.ollama.ollama").Output()
		if err == nil && len(out) > 0 {
			return OllamaSystem
		}
	}

	return OllamaManual
}

// ConfigureOllamaGPU attempts to enable GPU acceleration for Ollama.
func ConfigureOllamaGPU(gpuInfo GPUInfo) error {
	processType := DetectOllamaProcessType()

	switch processType {
	case OllamaChild:
		return restartOllamaWithGPU(gpuInfo)

	case OllamaSystem:
		return configureOllamaSystemService(gpuInfo)

	case OllamaManual:
		return &OllamaGPUWarning{
			Message: fmt.Sprintf(
				"Ollama is running manually and may not use your %s GPU. "+
					"For GPU acceleration, restart Ollama with: "+
					"CUDA_VISIBLE_DEVICES=0 ollama serve",
				gpuInfo.Name,
			),
		}
	}
	return nil
}

func restartOllamaWithGPU(gpuInfo GPUInfo) error {
	// Stub for restarting Ollama child process with visible devices env var
	return nil
}

func configureOllamaSystemService(gpuInfo GPUInfo) error {
	// Stub for setting system service configuration (requires root/manual intervention)
	return nil
}
