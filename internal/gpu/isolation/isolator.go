package isolation

import (
	"fmt"
	"os"
	"os/exec"
)

// IsolatedProcess wraps an exec.Cmd with GPU isolation applied.
type IsolatedProcess struct {
	Cmd         *exec.Cmd
	DeviceIndex int
	Backend     string
}

// IsolationConfig defines how to isolate a GPU task process.
type IsolationConfig struct {
	DeviceIndex    int
	Backend        string   // "cuda", "rocm", "metal", "cpu"
	MemoryFraction float64  // 0.0-1.0, max VRAM fraction
	AllowedEnvKeys []string // environment variables to pass through
}

// DefaultAllowedEnvKeys lists environment variables safe to pass to GPU processes.
var DefaultAllowedEnvKeys = []string{
	"HOME", "PATH", "TERM", "LANG", "USER",
	"OLLAMA_HOST", "OLLAMA_MODELS",
	"HF_HOME", // HuggingFace model cache
}

// Isolate wraps a command with GPU-specific isolation.
// Returns the modified command ready to run.
func Isolate(cmd *exec.Cmd, cfg IsolationConfig) (*IsolatedProcess, error) {
	// Build a clean environment
	env := buildIsolatedEnv(cfg)
	cmd.Env = env

	// Set GPU device targeting
	switch cfg.Backend {
	case "cuda":
		cmd.Env = append(cmd.Env,
			fmt.Sprintf("CUDA_VISIBLE_DEVICES=%d", cfg.DeviceIndex),
			fmt.Sprintf("CUDA_DEVICE_ORDER=PCI_BUS_ID"),
		)
		if cfg.MemoryFraction > 0 {
			// Ollama respects this environment variable
			pct := int(cfg.MemoryFraction * 100)
			cmd.Env = append(cmd.Env,
				fmt.Sprintf("OLLAMA_GPU_OVERHEAD=%d", 100-pct),
			)
		}

	case "rocm":
		cmd.Env = append(cmd.Env,
			fmt.Sprintf("ROCR_VISIBLE_DEVICES=%d", cfg.DeviceIndex),
			fmt.Sprintf("GPU_MAX_HEAP_SIZE=%d",
				int(cfg.MemoryFraction*100)),
		)

	case "metal":
		// Metal uses unified memory - no device targeting needed
	}

	// Process isolation: own process group prevents orphan children
	applyPlatformIsolation(cmd)

	// Working directory: isolated temp dir per task
	workDir, err := os.MkdirTemp("", "openfabric-gpu-*")
	if err != nil {
		return nil, fmt.Errorf("isolation: create work dir: %w", err)
	}
	cmd.Dir = workDir

	return &IsolatedProcess{
		Cmd:         cmd,
		DeviceIndex: cfg.DeviceIndex,
		Backend:     cfg.Backend,
	}, nil
}

// KillGroup terminates the process and all its children.
// Uses process group kill to ensure no orphan GPU processes remain.
func (p *IsolatedProcess) KillGroup() error {
	if p.Cmd.Process == nil {
		return nil
	}

	return killPlatformGroup(p.Cmd.Process)
}

// buildIsolatedEnv constructs a minimal environment for a GPU task.
func buildIsolatedEnv(cfg IsolationConfig) []string {
	env := make([]string, 0, len(cfg.AllowedEnvKeys)+8)

	// Pass through only allowed keys from current environment
	for _, key := range cfg.AllowedEnvKeys {
		if val, ok := os.LookupEnv(key); ok {
			env = append(env, fmt.Sprintf("%s=%s", key, val))
		}
	}

	return env
}
