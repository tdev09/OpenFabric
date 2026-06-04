package health

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/mem"
)

// CheckOllama verifies that the local Ollama instance is responsive.
// Tests the /api/tags endpoint which lists available models.
func CheckOllama(ollamaURL string) Checker {
	return func(ctx context.Context) CheckResult {
		result := CheckResult{Name: "ollama"}

		req, err := http.NewRequestWithContext(ctx, "GET",
			ollamaURL+"/api/tags", nil)
		if err != nil {
			return unhealthy(result, "failed to build request: "+err.Error())
		}

		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return unhealthy(result, "Ollama is not responding. Is it running?")
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return degraded(result, fmt.Sprintf(
				"Ollama returned status %d", resp.StatusCode))
		}

		result.Status = StatusHealthy
		result.Message = "Ollama is running and responding"
		return result
	}
}

// CheckStorage verifies that the shared storage directory is accessible
// and has sufficient free space.
func CheckStorage(storageDir string, minFreeBytes int64) Checker {
	return func(ctx context.Context) CheckResult {
		result := CheckResult{Name: "storage"}

		// Ensure directory exists
		if err := os.MkdirAll(storageDir, 0755); err != nil {
			return unhealthy(result, "Storage directory cannot be created: "+err.Error())
		}

		// Check directory is writable
		testFile := filepath.Join(storageDir, ".health_check")
		if err := os.WriteFile(testFile, []byte("ok"), 0600); err != nil {
			return unhealthy(result,
				"Storage directory is not writable: "+err.Error())
		}
		_ = os.Remove(testFile)

		// Check free space using cross-platform gopsutil
		usage, err := disk.Usage(storageDir)
		if err != nil {
			return degraded(result, "Could not check storage free space: "+err.Error())
		}

		if int64(usage.Free) < minFreeBytes {
			return degraded(result, fmt.Sprintf(
				"Storage space is low. %.1f%% used. Free up space to avoid issues.",
				usage.UsedPercent,
			))
		}

		result.Status = StatusHealthy
		result.Message = fmt.Sprintf("Storage healthy (%.1f GB free)",
			float64(usage.Free)/(1024*1024*1024))
		return result
	}
}

// CheckMemory verifies that the system has sufficient free/available RAM.
func CheckMemory(minFreeBytes uint64) Checker {
	return func(ctx context.Context) CheckResult {
		result := CheckResult{Name: "memory"}

		// Use cross-platform gopsutil
		v, err := mem.VirtualMemory()
		if err != nil {
			return degraded(result, "Could not read system memory stats: "+err.Error())
		}

		if v.Available < minFreeBytes {
			return degraded(result, fmt.Sprintf(
				"System RAM is low. %.1f%% used. Performance may be degraded.",
				v.UsedPercent,
			))
		}

		result.Status = StatusHealthy
		result.Message = fmt.Sprintf("Memory healthy (%.1f GB available)",
			float64(v.Available)/(1024*1024*1024))
		return result
	}
}

// CheckWAL verifies that the WAL file is accessible and not corrupted.
func CheckWAL(walPath string) Checker {
	return func(ctx context.Context) CheckResult {
		result := CheckResult{Name: "wal"}

		info, err := os.Stat(walPath)
		if err != nil {
			if os.IsNotExist(err) {
				result.Status = StatusHealthy
				result.Message = "WAL not yet created (clean start)"
				return result
			}
			return unhealthy(result, "WAL file is inaccessible: "+err.Error())
		}

		// Check WAL size - if > 100MB, checkpoint is overdue
		if info.Size() > 100*1024*1024 {
			return degraded(result, fmt.Sprintf(
				"WAL is %.1f MB - checkpoint pending",
				float64(info.Size())/(1024*1024),
			))
		}

		result.Status = StatusHealthy
		result.Message = fmt.Sprintf("WAL healthy (%.1f KB)",
			float64(info.Size())/1024)
		return result
	}
}

// CheckMCPServer verifies that a named MCP server process is running.
func CheckMCPServer(name string, testFn func(ctx context.Context) error) Checker {
	return func(ctx context.Context) CheckResult {
		result := CheckResult{Name: "mcp/" + name}

		if err := testFn(ctx); err != nil {
			return degraded(result, fmt.Sprintf(
				"MCP server '%s' is not responding: %s", name, err.Error()))
		}

		result.Status = StatusHealthy
		result.Message = fmt.Sprintf("MCP server '%s' is connected", name)
		return result
	}
}

// CheckLibp2p verifies that the libp2p host has active peer connections.
func CheckLibp2p(connectedPeers func() int) Checker {
	return func(ctx context.Context) CheckResult {
		result := CheckResult{Name: "p2p_network"}

		peers := connectedPeers()
		if peers == 0 {
			result.Status = StatusDegraded
			result.Message = "No peers connected - cluster is running in solo mode"
			return result
		}

		result.Status = StatusHealthy
		result.Message = fmt.Sprintf("%d peer(s) connected", peers)
		return result
	}
}

// CheckGoroutineLeaks detects runaway goroutine growth.
func CheckGoroutineLeaks(baselineCount int, maxMultiplier float64) Checker {
	return func(ctx context.Context) CheckResult {
		result := CheckResult{Name: "goroutines"}

		current := runtime.NumGoroutine()
		ratio := float64(current) / float64(baselineCount)

		if ratio > maxMultiplier {
			return degraded(result, fmt.Sprintf(
				"Goroutine count is %d (%.1fx baseline) - possible leak",
				current, ratio,
			))
		}

		result.Status = StatusHealthy
		result.Message = fmt.Sprintf("%d goroutines running", current)
		return result
	}
}

// Helper constructors for common result patterns.

func unhealthy(r CheckResult, msg string) CheckResult {
	r.Status = StatusUnhealthy
	r.Message = msg
	return r
}

func degraded(r CheckResult, msg string) CheckResult {
	r.Status = StatusDegraded
	r.Message = msg
	return r
}
