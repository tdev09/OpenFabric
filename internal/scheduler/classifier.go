package scheduler

import (
	"regexp"
	"strconv"
	"strings"
)

// TaskClass defines the category of a task for routing purposes.
type TaskClass string

const (
	// ClassShell is a generic shell command (low resource, any node).
	ClassShell TaskClass = "shell"
	// ClassLLM is an AI inference request (high RAM, prefers GPU node).
	ClassLLM TaskClass = "llm"
	// ClassGPU is an image generation or GPU compute task.
	ClassGPU TaskClass = "gpu"
	// ClassIO is a file operation or network task (prefers fast storage).
	ClassIO TaskClass = "io"
	// ClassCPU is a compute-heavy task (prefers nodes with free CPU cores).
	ClassCPU TaskClass = "cpu"
)

// TaskRequirements describes the resource needs of a task.
type TaskRequirements struct {
	Class          TaskClass
	MinRAMBytes    int64        // minimum RAM to run this task
	PrefersGPU     bool         // true if GPU would benefit this task
	RequiresGPU    bool         // true if task CANNOT run without GPU
	EstimatedSecs  int          // estimated runtime in seconds
	Priority       TaskPriority // scheduling priority band
	AllowedNodes   []string     // if non-empty, only route to these node IDs
	PreferredNodes []string     // prefer these but not required
}

// Classifier analyses a task submission and returns its requirements.
// It is stateless and safe for concurrent use.
type Classifier struct{}

// Classify inspects the command and hints to determine resource needs.
// It never blocks - must return in under 100 microseconds.
func (c *Classifier) Classify(cmd string, hints map[string]string) TaskRequirements {
	req := TaskRequirements{
		Class:         ClassShell,
		MinRAMBytes:   256 * 1024 * 1024, // 256 MB default
		EstimatedSecs: 30,
		Priority:      PriorityNormal,
	}

	lower := strings.ToLower(cmd)

	// LLM inference detection
	if isLLMCommand(lower) {
		req.Class = ClassLLM
		req.MinRAMBytes = estimateLLMRAM(cmd)
		req.PrefersGPU = true
		req.EstimatedSecs = 120
		req.Priority = PriorityHigh
		return req
	}

	// GPU image generation detection
	if isGPUCommand(lower) {
		req.Class = ClassGPU
		req.RequiresGPU = true
		req.MinRAMBytes = 6 * 1024 * 1024 * 1024 // 6 GB VRAM minimum
		req.EstimatedSecs = 60
		req.Priority = PriorityNormal
		return req
	}

	// CPU-intensive detection (compilation, data processing)
	if isCPUIntensive(lower) {
		req.Class = ClassCPU
		req.MinRAMBytes = 1 * 1024 * 1024 * 1024 // 1 GB
		req.EstimatedSecs = 300
		req.Priority = PriorityNormal
		return req
	}

	// IO-bound detection (file operations, network transfers)
	if isIOBound(lower) {
		req.Class = ClassIO
		req.MinRAMBytes = 128 * 1024 * 1024 // 128 MB
		req.EstimatedSecs = 60
		return req
	}

	// Apply caller-provided hints (can override detection defaults).
	if hints != nil {
		if v, ok := hints["priority"]; ok {
			req.Priority = parsePriority(v)
		}
		if v, ok := hints["min_ram_mb"]; ok {
			req.MinRAMBytes = parseMB(v)
		}
		if v, ok := hints["node"]; ok {
			req.AllowedNodes = []string{v}
		}
	}

	return req
}

// isLLMCommand returns true for ollama or other LLM-related commands.
func isLLMCommand(cmd string) bool {
	patterns := []string{"ollama run", "ollama chat", "llm ", "llama", "mistral", "gemma"}
	for _, p := range patterns {
		if strings.Contains(cmd, p) {
			return true
		}
	}
	return false
}

// isGPUCommand returns true for image generation or GPU compute commands.
func isGPUCommand(cmd string) bool {
	patterns := []string{"stable diffusion", "stable_diffusion", "sdxl", "flux", "comfyui", "a1111", "txt2img"}
	for _, p := range patterns {
		if strings.Contains(cmd, p) {
			return true
		}
	}
	return false
}

// isCPUIntensive returns true for compilation and heavy compute commands.
func isCPUIntensive(cmd string) bool {
	patterns := []string{"go build", "gcc", "make ", "cargo build", "ffmpeg", "python3 train"}
	for _, p := range patterns {
		if strings.Contains(cmd, p) {
			return true
		}
	}
	return false
}

// ioPattern matches common file and network utility names as whole words.
var ioPattern = regexp.MustCompile(`\b(cp|mv|rsync|wget|curl|tar|zip|unzip|scp)\b`)

// isIOBound returns true for file and network operations.
func isIOBound(cmd string) bool {
	return ioPattern.MatchString(cmd)
}

// estimateLLMRAM estimates the RAM needed for an LLM based on model size
// indicators found in the command string.
func estimateLLMRAM(cmd string) int64 {
	GB := int64(1024 * 1024 * 1024)
	switch {
	case strings.Contains(cmd, "405b"):
		return 240 * GB
	case strings.Contains(cmd, "70b"):
		return 40 * GB
	case strings.Contains(cmd, "34b"):
		return 20 * GB
	case strings.Contains(cmd, "13b"):
		return 8 * GB
	case strings.Contains(cmd, "8b"), strings.Contains(cmd, "7b"):
		return 5 * GB
	case strings.Contains(cmd, "3b"):
		return 2 * GB
	default:
		return 4 * GB // conservative default
	}
}

// parsePriority converts a hint string to a TaskPriority band.
func parsePriority(s string) TaskPriority {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "critical":
		return PriorityCritical
	case "high":
		return PriorityHigh
	case "background", "low":
		return PriorityBackground
	default:
		return PriorityNormal
	}
}

// parseMB converts a megabyte count string to bytes.
// Returns 256 MB default on invalid input.
func parseMB(s string) int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || v <= 0 {
		return 256 * 1024 * 1024
	}
	return v * 1024 * 1024
}
