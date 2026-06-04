package gpu

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ImageGenService represents a discovered image generation backend.
type ImageGenService struct {
	Type    string `json:"type"`    // "automatic1111" or "comfyui"
	URL     string `json:"url"`     // full base URL including port
	Version string `json:"version"` // detected version or details
}

// DefaultPorts lists ports to probe for each service type.
// Ordered by likelihood - most common ports first.
var DefaultPorts = map[string][]int{
	"automatic1111": {7860, 7861, 7862, 7863, 8080},
	"comfyui":       {8188, 8189, 3000},
}

var (
	configuredURL   string
	configuredURLMu sync.RWMutex
)

// SetConfiguredURL sets the user-configured URL for image generation.
func SetConfiguredURL(url string) {
	configuredURLMu.Lock()
	configuredURL = url
	configuredURLMu.Unlock()
}

// GetConfiguredURL returns the user-configured URL thread-safely.
func GetConfiguredURL() string {
	configuredURLMu.RLock()
	defer configuredURLMu.RUnlock()
	return configuredURL
}

// DiscoverImageGenServices scans for running image generation services.
// First checks user-configured ports, then falls back to default scan.
func DiscoverImageGenServices(configuredURL string) (*ImageGenService, error) {
	// 1. If user has configured a specific URL, try that first
	if configuredURL != "" {
		normalized := strings.TrimSuffix(configuredURL, "/")
		if svc, err := probeURL(normalized); err == nil {
			return svc, nil
		}
		return nil, fmt.Errorf(
			"configured image gen URL %s is not responding. "+
				"Check that AUTOMATIC1111 or ComfyUI is running.",
			configuredURL,
		)
	}

	// 2. Auto-scan default ports
	// Let's do it deterministically. We'll search in a fixed order.
	// Since DefaultPorts is a map, iteration order is random in Go.
	// To avoid randomness, let's scan "automatic1111" first, then "comfyui".
	typesOrder := []string{"automatic1111", "comfyui"}
	for _, serviceType := range typesOrder {
		ports := DefaultPorts[serviceType]
		for _, port := range ports {
			url := fmt.Sprintf("http://localhost:%d", port)
			if svc, err := probeURL(url); err == nil {
				svc.Type = serviceType
				return svc, nil
			}
		}
	}

	return nil, fmt.Errorf(
		"no image generation service found. " +
			"Install AUTOMATIC1111 or ComfyUI, or configure a custom URL in Settings → GPU.",
	)
}

func httpGet(url string, timeout time.Duration) (*http.Response, error) {
	client := &http.Client{Timeout: timeout}
	return client.Get(url)
}

func probeURL(baseURL string) (*ImageGenService, error) {
	// Try A1111 health endpoint
	a1111URL := baseURL + "/sdapi/v1/sd-models"
	if resp, err := httpGet(a1111URL, 1*time.Second); err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == 200 {
			version := "active"
			var models []any
			if errDec := json.NewDecoder(resp.Body).Decode(&models); errDec == nil {
				version = fmt.Sprintf("%d models available", len(models))
			}
			return &ImageGenService{Type: "automatic1111", URL: baseURL, Version: version}, nil
		}
	}

	// Try ComfyUI health endpoint
	comfyURL := baseURL + "/system_stats"
	if resp, err := httpGet(comfyURL, 1*time.Second); err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == 200 {
			version := "active"
			var stats map[string]any
			if errDec := json.NewDecoder(resp.Body).Decode(&stats); errDec == nil {
				if devices, ok := stats["devices"].([]any); ok {
					version = fmt.Sprintf("%d GPU devices active", len(devices))
				}
			}
			return &ImageGenService{Type: "comfyui", URL: baseURL, Version: version}, nil
		}
	}

	return nil, fmt.Errorf("no image gen service at %s", baseURL)
}
