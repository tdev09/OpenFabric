package gpu

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDiscoverImageGenServices(t *testing.T) {
	// Set up a mock AUTOMATIC1111 server
	a1111ModelsCalled := false
	mockA1111 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sdapi/v1/sd-models" {
			a1111ModelsCalled = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode([]map[string]any{
				{"title": "model1"},
				{"title": "model2"},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockA1111.Close()

	// Set up a mock ComfyUI server
	comfyStatsCalled := false
	mockComfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/system_stats" {
			comfyStatsCalled = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"devices": []any{
					map[string]any{"name": "mock-gpu"},
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockComfy.Close()

	// 1. Test discovering A1111 on mock URL
	svc, err := DiscoverImageGenServices(mockA1111.URL)
	if err != nil {
		t.Fatalf("failed to discover A1111: %v", err)
	}
	if !a1111ModelsCalled {
		t.Error("expected /sdapi/v1/sd-models to be called")
	}
	if svc.Type != "automatic1111" {
		t.Errorf("expected type automatic1111, got %q", svc.Type)
	}
	if svc.URL != mockA1111.URL {
		t.Errorf("expected URL %q, got %q", mockA1111.URL, svc.URL)
	}
	if !strings.Contains(svc.Version, "2 models available") {
		t.Errorf("expected version to contain '2 models available', got %q", svc.Version)
	}

	// 2. Test discovering ComfyUI on mock URL
	svc, err = DiscoverImageGenServices(mockComfy.URL)
	if err != nil {
		t.Fatalf("failed to discover ComfyUI: %v", err)
	}
	if !comfyStatsCalled {
		t.Error("expected /system_stats to be called")
	}
	if svc.Type != "comfyui" {
		t.Errorf("expected type comfyui, got %q", svc.Type)
	}
	if svc.URL != mockComfy.URL {
		t.Errorf("expected URL %q, got %q", mockComfy.URL, svc.URL)
	}
	if !strings.Contains(svc.Version, "1 GPU devices active") {
		t.Errorf("expected version to contain '1 GPU devices active', got %q", svc.Version)
	}

	// 3. Test invalid URL connection failure
	_, err = DiscoverImageGenServices("http://localhost:59999")
	if err == nil {
		t.Error("expected discovery to fail on invalid URL, but it succeeded")
	}
}
