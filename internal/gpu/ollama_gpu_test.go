package gpu

import (
	"testing"
)

func TestDetectOllamaProcessType(t *testing.T) {
	// Since we are running the test in a clean environment, it should either return
	// OllamaSystem (if the system has ollama active in systemd/launchd) or OllamaManual.
	pType := DetectOllamaProcessType()
	if pType != OllamaChild && pType != OllamaSystem && pType != OllamaManual {
		t.Errorf("unexpected process type: %s", pType)
	}
}

func TestConfigureOllamaGPU(t *testing.T) {
	gpuInfo := GPUInfo{
		Available: true,
		Name:      "Mock NVIDIA GPU",
	}

	err := ConfigureOllamaGPU(gpuInfo)
	// If it detects manual, it should return a warning error. Otherwise nil.
	pType := DetectOllamaProcessType()
	if pType == OllamaManual {
		if err == nil {
			t.Error("expected a warning error for manual Ollama instance, but got nil")
		} else if _, ok := err.(*OllamaGPUWarning); !ok {
			t.Errorf("expected *OllamaGPUWarning error type, got %T: %v", err, err)
		}
	} else {
		if err != nil {
			t.Errorf("expected no error, got: %v", err)
		}
	}
}
