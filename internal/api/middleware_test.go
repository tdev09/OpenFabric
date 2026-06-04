package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCORSMiddleware(t *testing.T) {
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	handler := CORSMiddleware(nextHandler)

	tests := []struct {
		name           string
		origin         string
		host           string
		tunnelProxy    bool
		expectedStatus int
	}{
		{
			name:           "Empty Origin (same-origin/Go client)",
			origin:         "",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "Valid Origin Localhost",
			origin:         "http://localhost:4892",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "Valid Origin Dev Server",
			origin:         "http://localhost:5173",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "Valid Origin IP",
			origin:         "http://127.0.0.1:4892",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "Same-Origin Local IP",
			origin:         "http://192.168.1.50:4892",
			host:           "192.168.1.50:4892",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "Same-Origin Custom Domain",
			origin:         "http://mycluster.local:4892",
			host:           "mycluster.local:4892",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "Cross-Origin IP Mismatch",
			origin:         "http://192.168.1.99:4892",
			host:           "192.168.1.50:4892",
			expectedStatus: http.StatusForbidden,
		},
		{
			name:           "Invalid Origin External",
			origin:         "http://malicious.com",
			expectedStatus: http.StatusForbidden,
		},
		{
			name:           "Invalid Origin Subdomain",
			origin:         "http://sub.localhost:4892",
			expectedStatus: http.StatusForbidden,
		},
		{
			name:           "Tunnel Proxy Header - allowed even with external origin",
			origin:         "http://127.0.0.1:4893", // relay port - different from API host
			host:           "127.0.0.1:4892",
			tunnelProxy:    true,
			expectedStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			if tt.host != "" {
				req.Host = tt.host
			}
			if tt.tunnelProxy {
				req.Header.Set("X-Tunnel-Proxy", "1")
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, rec.Code)
			}

			if tt.expectedStatus == http.StatusOK {
				corsHeader := rec.Header().Get("Access-Control-Allow-Origin")
				if corsHeader != tt.origin {
					t.Errorf("expected Access-Control-Allow-Origin header %q, got %q", tt.origin, corsHeader)
				}
			}
		})
	}
}

func TestRateLimitMiddleware(t *testing.T) {
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := RateLimitMiddleware(nextHandler)

	// Since we set burst to 200, if we send 250 requests rapidly, some must fail with 429
	allowedCount := 0
	limitedCount := 0

	for i := 0; i < 250; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
		req.RemoteAddr = "127.0.0.1:12345"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code == http.StatusOK {
			allowedCount++
		} else if rec.Code == http.StatusTooManyRequests {
			limitedCount++
		} else {
			t.Errorf("unexpected status code: %d", rec.Code)
		}
	}

	t.Logf("Allowed: %d, Limited: %d", allowedCount, limitedCount)

	if limitedCount == 0 {
		t.Error("expected at least some requests to be rate limited (429)")
	}

	if allowedCount == 0 {
		t.Error("expected at least some requests to succeed (up to burst size)")
	}

	// Verify that requests from another IP are treated with a separate rate limit bucket
	otherReq := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	otherReq.RemoteAddr = "192.168.1.50:54321"
	otherRec := httptest.NewRecorder()
	handler.ServeHTTP(otherRec, otherReq)

	if otherRec.Code != http.StatusOK {
		t.Errorf("expected new IP request to succeed, got: %d", otherRec.Code)
	}
}

func TestSettingsPersistence(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "api-settings-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	filePath := filepath.Join(tmpDir, "settings.json")
	s := &Settings{
		ClusterName:     "test-cluster",
		DeviceName:      "test-device",
		APIPort:         4892,
		SandboxMode:     true,
		AllowedCommands: []string{"echo"},
		UpdatedAt:       time.Now().Truncate(time.Second),
	}
	s.SetFilePath(filePath)

	// Save
	if err := s.SaveToDisk(); err != nil {
		t.Fatalf("failed to save: %v", err)
	}

	// Load
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read settings file: %v", err)
	}

	var loaded Settings
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if loaded.ClusterName != s.ClusterName || loaded.DeviceName != s.DeviceName || loaded.APIPort != s.APIPort {
		t.Fatalf("loaded settings mismatch: %+v vs %+v", loaded, s)
	}

	// Test Merge
	incoming := &Settings{
		ClusterName: "new-cluster",
		UpdatedAt:   time.Now().Add(time.Second).Truncate(time.Second),
	}
	s.Merge(incoming)

	if s.ClusterName != "new-cluster" {
		t.Fatalf("expected merged cluster name 'new-cluster', got %s", s.ClusterName)
	}
}

func TestLocalhostOnly(t *testing.T) {
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	handler := localhostOnly(nextHandler)

	tests := []struct {
		name           string
		remoteAddr     string
		tunnelProxy    string
		expectedStatus int
	}{
		{
			name:           "Request from localhost is allowed",
			remoteAddr:     "127.0.0.1:12345",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "Request from loopback keeps X-Tunnel-Proxy",
			remoteAddr:     "127.0.0.1:12345",
			tunnelProxy:    "1",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "Request from external is blocked",
			remoteAddr:     "192.168.1.100:12345",
			expectedStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.tunnelProxy != "" {
				req.Header.Set("X-Tunnel-Proxy", tt.tunnelProxy)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, rec.Code)
			}
		})
	}
}

func TestLocalNetworkOnly(t *testing.T) {
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	handler := localNetworkOnly(nextHandler)

	tests := []struct {
		name           string
		remoteAddr     string
		expectedStatus int
	}{
		{
			name:           "Private class A is allowed",
			remoteAddr:     "10.0.0.5:12345",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "Private class C is allowed",
			remoteAddr:     "192.168.1.100:12345",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "Public IP is blocked",
			remoteAddr:     "8.8.8.8:12345",
			expectedStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
			req.RemoteAddr = tt.remoteAddr
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, rec.Code)
			}
		})
	}
}

