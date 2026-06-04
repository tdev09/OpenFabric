// Package api - HTTP middleware for the OpenFabric API server.
package api

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/renameio"
	"github.com/openfabric/openfabric/internal/policy"
	"golang.org/x/time/rate"
)

// localhostOnly rejects any request that didn't come from 127.0.0.1 or ::1.
// It also strips any X-Tunnel-Proxy header from incoming requests so that
// external clients cannot forge it to bypass CORS checks. The header is
// only legitimately added by the internal reverse proxy on the outbound leg.
func localhostOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/api/cluster/join" || strings.HasPrefix(path, "/join/") {
			next.ServeHTTP(w, r)
			return
		}
		host := r.RemoteAddr
		// Strip port.
		if idx := strings.LastIndex(host, ":"); idx != -1 {
			host = host[:idx]
		}
		host = strings.Trim(host, "[]") // IPv6 brackets
		if host != "127.0.0.1" && host != "::1" && host != "localhost" {
			r.Header.Del("X-Tunnel-Proxy")
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// CORSMiddleware blocks cross-origin requests from web pages.
// Allows requests from same-origin or allowed localhost/local-network dev origins.
func CORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// SECURITY: X-Tunnel-Proxy is only allowed if it comes from the loopback interface,
		// which is where the internal reverse proxy forwards requests from.
		// If the request came from any other IP (e.g. over the local network), strip it
		// to prevent CORS bypass.
		if r.Header.Get("X-Tunnel-Proxy") != "" {
			host := r.RemoteAddr
			if idx := strings.LastIndex(host, ":"); idx != -1 {
				host = host[:idx]
			}
			host = strings.Trim(host, "[]")
			if host != "127.0.0.1" && host != "::1" && host != "localhost" {
				r.Header.Del("X-Tunnel-Proxy")
			}
		}

		origin := r.Header.Get("Origin")

		// Block cross-origin requests (CSRF protection)
		if origin != "" {
			allowed := false

			// 0. Allow requests forwarded through the secure tunnel reverse proxy.
			if r.Header.Get("X-Tunnel-Proxy") == "1" {
				allowed = true
			}

			if !allowed {
				// 1. Allow same-origin requests
				strippedOrigin := origin
				if strings.HasPrefix(origin, "http://") {
					strippedOrigin = strings.TrimPrefix(origin, "http://")
				} else if strings.HasPrefix(origin, "https://") {
					strippedOrigin = strings.TrimPrefix(origin, "https://")
				}

				if strippedOrigin == r.Host {
					allowed = true
				} else {
					// 2. Allow any localhost or local network origin in development
					oHost := strippedOrigin
					if idx := strings.LastIndex(oHost, ":"); idx != -1 {
						oHost = oHost[:idx]
					}
					oHost = strings.Trim(oHost, "[]")

					if oHost == "localhost" || oHost == "127.0.0.1" || oHost == "::1" {
						allowed = true
					} else {
						ip := net.ParseIP(oHost)
						if ip != nil && isPrivateIP(ip) {
							// A non-loopback private-network origin is only safe if it
							// matches this server's own Host (same device, different port).
							// Any other private IP is a cross-origin device on the LAN.
							serverHost := r.Host
							if idx := strings.LastIndex(serverHost, ":"); idx != -1 {
								serverHost = serverHost[:idx]
							}
							serverHost = strings.Trim(serverHost, "[]")
							if oHost == serverHost {
								allowed = true
							}
						}
					}
				}
			}

			if !allowed {
				http.Error(w, "Cross-origin requests not allowed", http.StatusForbidden)
				return
			}
		}

		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

type ipLimiter struct {
	mu       sync.RWMutex
	limiters map[string]*limiterEntry
}

// limiterEntry wraps a rate.Limiter with a last-seen timestamp for eviction.
type limiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

func newIPLimiter() *ipLimiter {
	il := &ipLimiter{
		limiters: make(map[string]*limiterEntry),
	}
	// SECURITY FIX: Periodically evict stale entries to prevent unbounded memory
	// growth when many unique client IPs connect (e.g. during a scan/DDoS).
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			threshold := time.Now().Add(-10 * time.Minute)
			il.mu.Lock()
			for ip, entry := range il.limiters {
				if entry.lastSeen.Before(threshold) {
					delete(il.limiters, ip)
				}
			}
			il.mu.Unlock()
		}
	}()
	return il
}

func (il *ipLimiter) getLimiter(ip string) *rate.Limiter {
	il.mu.RLock()
	entry, exists := il.limiters[ip]
	il.mu.RUnlock()

	if exists {
		il.mu.Lock()
		entry.lastSeen = time.Now()
		il.mu.Unlock()
		return entry.limiter
	}

	il.mu.Lock()
	defer il.mu.Unlock()
	// Double-check after acquiring write lock.
	entry, exists = il.limiters[ip]
	if !exists {
		entry = &limiterEntry{
			limiter:  rate.NewLimiter(rate.Limit(100), 200), // 100 req/s, burst 200
			lastSeen: time.Now(),
		}
		il.limiters[ip] = entry
	}
	return entry.limiter
}

// RateLimitMiddleware limits requests to 100 per second per IP.
func RateLimitMiddleware(next http.Handler) http.Handler {
	il := newIPLimiter()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := r.RemoteAddr
		if idx := strings.LastIndex(ip, ":"); idx != -1 {
			ip = ip[:idx]
		}
		ip = strings.Trim(ip, "[]")

		limiter := il.getLimiter(ip)
		if !limiter.Allow() {
			http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Settings holds user-configurable agent settings.
type Settings struct {
	mu       sync.Mutex
	filePath string // path for local persistence

	ClusterName        string    `json:"cluster_name"`
	DeviceName         string    `json:"device_name"`
	APIPort            int       `json:"api_port"`
	AutoStart          bool      `json:"auto_start"`
	StorageSyncEnabled bool      `json:"storage_sync_enabled"`
	AcceptTasks        bool      `json:"accept_tasks"`
	NetworkAccess      string    `json:"network_access"`
	MemoryEnabled      bool      `json:"memory_enabled"`
	MemoryAutoExtract  bool      `json:"memory_auto_extract"`
	SandboxMode        bool      `json:"sandbox_mode"`
	AllowedCommands    []string  `json:"allowed_commands"`
	TaskTimeout        int       `json:"task_timeout"`
	ImageGenURL        string    `json:"image_gen_url"`
	WOLMemoryThreshold float64   `json:"wol_memory_threshold"`
	UpdatedAt          time.Time `json:"updated_at"`

	// Fabric Shield - per-task resource limits.
	MaxTaskMemoryMB   int `json:"max_task_memory_mb"`
	MaxTaskProcs      int `json:"max_task_procs"`
	MaxTaskFileSizeMB int `json:"max_task_file_size_mb"`

	// Fabric Policy Engine - cluster-wide dynamic policies.
	Policies []policy.Policy `json:"policies"`

	// Local-only Distributed RAG configurations.
	LocalIndexDirs    []string `json:"local_index_dirs"`
	RAGTimeoutSeconds int      `json:"rag_timeout_seconds"`
}

// MarshalJSON outputs the settings without the mutex.
func (s *Settings) MarshalJSON() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return json.Marshal(&struct {
		ClusterName        string          `json:"cluster_name"`
		DeviceName         string          `json:"device_name"`
		APIPort            int             `json:"api_port"`
		AutoStart          bool            `json:"auto_start"`
		StorageSyncEnabled bool            `json:"storage_sync_enabled"`
		AcceptTasks        bool            `json:"accept_tasks"`
		NetworkAccess      string          `json:"network_access"`
		MemoryEnabled      bool            `json:"memory_enabled"`
		MemoryAutoExtract  bool            `json:"memory_auto_extract"`
		SandboxMode        bool            `json:"sandbox_mode"`
		AllowedCommands    []string        `json:"allowed_commands"`
		TaskTimeout        int             `json:"task_timeout"`
		ImageGenURL        string          `json:"image_gen_url"`
		WOLMemoryThreshold float64         `json:"wol_memory_threshold"`
		UpdatedAt          time.Time       `json:"updated_at"`
		MaxTaskMemoryMB    int             `json:"max_task_memory_mb"`
		MaxTaskProcs       int             `json:"max_task_procs"`
		MaxTaskFileSizeMB  int             `json:"max_task_file_size_mb"`
		Policies           []policy.Policy `json:"policies"`
		LocalIndexDirs     []string        `json:"local_index_dirs"`
		RAGTimeoutSeconds  int             `json:"rag_timeout_seconds"`
	}{
		ClusterName:        s.ClusterName,
		DeviceName:         s.DeviceName,
		APIPort:            s.APIPort,
		AutoStart:          s.AutoStart,
		StorageSyncEnabled: s.StorageSyncEnabled,
		AcceptTasks:        s.AcceptTasks,
		NetworkAccess:      s.NetworkAccess,
		MemoryEnabled:      s.MemoryEnabled,
		MemoryAutoExtract:  s.MemoryAutoExtract,
		SandboxMode:        s.SandboxMode,
		AllowedCommands:    s.AllowedCommands,
		TaskTimeout:        s.TaskTimeout,
		ImageGenURL:        s.ImageGenURL,
		WOLMemoryThreshold: s.WOLMemoryThreshold,
		UpdatedAt:          s.UpdatedAt,
		MaxTaskMemoryMB:    s.MaxTaskMemoryMB,
		MaxTaskProcs:       s.MaxTaskProcs,
		MaxTaskFileSizeMB:  s.MaxTaskFileSizeMB,
		Policies:           s.Policies,
		LocalIndexDirs:     s.LocalIndexDirs,
		RAGTimeoutSeconds:  s.RAGTimeoutSeconds,
	})
}

// SetFilePath configures the settings location on disk.
func (s *Settings) SetFilePath(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.filePath = path
}

// SaveToDisk writes settings atomically to the configured filePath.
func (s *Settings) SaveToDisk() error {
	s.mu.Lock()
	path := s.filePath
	s.mu.Unlock()

	if path == "" {
		return nil
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	pf, err := renameio.TempFile(dir, path)
	if err != nil {
		return err
	}
	defer pf.Cleanup()

	if _, err := pf.Write(data); err != nil {
		return err
	}

	return pf.Commit()
}

// Merge updates non-zero fields from incoming into s and saves to disk.
func (s *Settings) Merge(incoming *Settings) {
	s.mu.Lock()
	if incoming.ClusterName != "" {
		s.ClusterName = incoming.ClusterName
	}
	if incoming.DeviceName != "" {
		s.DeviceName = incoming.DeviceName
	}
	if incoming.APIPort != 0 {
		s.APIPort = incoming.APIPort
	}
	s.AutoStart = incoming.AutoStart
	s.StorageSyncEnabled = incoming.StorageSyncEnabled
	s.AcceptTasks = incoming.AcceptTasks
	if incoming.NetworkAccess == "local_network" || incoming.NetworkAccess == "localhost_only" {
		s.NetworkAccess = incoming.NetworkAccess
	}
	s.MemoryEnabled = incoming.MemoryEnabled
	s.MemoryAutoExtract = incoming.MemoryAutoExtract
	s.SandboxMode = incoming.SandboxMode
	if incoming.AllowedCommands != nil {
		s.AllowedCommands = incoming.AllowedCommands
	}
	if incoming.TaskTimeout != 0 {
		s.TaskTimeout = incoming.TaskTimeout
	}
	s.ImageGenURL = incoming.ImageGenURL
	if incoming.WOLMemoryThreshold != 0 {
		s.WOLMemoryThreshold = incoming.WOLMemoryThreshold
	}
	if incoming.MaxTaskMemoryMB != 0 {
		s.MaxTaskMemoryMB = incoming.MaxTaskMemoryMB
	}
	if incoming.MaxTaskProcs != 0 {
		s.MaxTaskProcs = incoming.MaxTaskProcs
	}
	if incoming.MaxTaskFileSizeMB != 0 {
		s.MaxTaskFileSizeMB = incoming.MaxTaskFileSizeMB
	}
	if incoming.Policies != nil {
		s.Policies = incoming.Policies
	}
	if incoming.LocalIndexDirs != nil {
		s.LocalIndexDirs = incoming.LocalIndexDirs
	}
	if incoming.RAGTimeoutSeconds != 0 {
		s.RAGTimeoutSeconds = incoming.RAGTimeoutSeconds
	}
	if incoming.UpdatedAt.IsZero() {
		s.UpdatedAt = time.Now()
	} else {
		s.UpdatedAt = incoming.UpdatedAt
	}
	s.mu.Unlock()

	_ = s.SaveToDisk()
}

// GetImageGenURL returns image_gen_url thread-safely.
func (s *Settings) GetImageGenURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ImageGenURL
}

// GetSandboxMode returns sandbox_mode thread-safely.
func (s *Settings) GetSandboxMode() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.SandboxMode
}

// GetAllowedCommands returns allowed_commands copy thread-safely.
func (s *Settings) GetAllowedCommands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.AllowedCommands == nil {
		return nil
	}
	cp := make([]string, len(s.AllowedCommands))
	copy(cp, s.AllowedCommands)
	return cp
}

// GetTaskTimeout returns task_timeout as time.Duration thread-safely.
func (s *Settings) GetTaskTimeout() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.TaskTimeout <= 0 {
		return 5 * time.Minute
	}
	return time.Duration(s.TaskTimeout) * time.Minute
}

// GetWOLMemoryThreshold returns the Wake-on-LAN memory threshold thread-safely.
func (s *Settings) GetWOLMemoryThreshold() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.WOLMemoryThreshold
}

// GetResourceLimits returns the per-task OS resource limits configured in Settings.
// Returns defaults for any field that is zero.
func (s *Settings) GetResourceLimits() (maxMemBytes int64, maxProcs int, maxFileSizeBytes int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	maxMemBytes = int64(s.MaxTaskMemoryMB) * 1024 * 1024
	if maxMemBytes <= 0 {
		maxMemBytes = 2 * 1024 * 1024 * 1024 // 2 GiB default
	}

	maxProcs = s.MaxTaskProcs
	if maxProcs <= 0 {
		maxProcs = 64 // default
	}

	maxFileSizeBytes = int64(s.MaxTaskFileSizeMB) * 1024 * 1024
	if maxFileSizeBytes <= 0 {
		maxFileSizeBytes = 100 * 1024 * 1024 // 100 MiB default
	}

	return
}

// GetPolicies returns a copy of the policies thread-safely.
func (s *Settings) GetPolicies() []policy.Policy {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Policies == nil {
		return nil
	}
	cp := make([]policy.Policy, len(s.Policies))
	copy(cp, s.Policies)
	return cp
}

// GetSyncableFields retrieves fields that are synchronized cluster-wide.
func (s *Settings) GetSyncableFields() (clusterName string, allowedCmds []string, sandbox bool, maxMem, maxProcs, maxFile int, policies []policy.Policy, updatedAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	allowedCmds = make([]string, len(s.AllowedCommands))
	copy(allowedCmds, s.AllowedCommands)

	var policiesCopy []policy.Policy
	if s.Policies != nil {
		policiesCopy = make([]policy.Policy, len(s.Policies))
		copy(policiesCopy, s.Policies)
	}

	return s.ClusterName, allowedCmds, s.SandboxMode, s.MaxTaskMemoryMB, s.MaxTaskProcs, s.MaxTaskFileSizeMB, policiesCopy, s.UpdatedAt
}

// ApplySyncableFields resolves incoming synchronized settings fields using LWW.
// Returns true if the settings were updated.
func (s *Settings) ApplySyncableFields(clusterName string, allowedCmds []string, sandbox bool, maxMem, maxProcs, maxFile int, policies []policy.Policy, updatedAt time.Time) bool {
	s.mu.Lock()
	if !updatedAt.After(s.UpdatedAt) {
		s.mu.Unlock()
		return false
	}

	s.ClusterName = clusterName
	s.AllowedCommands = allowedCmds
	s.SandboxMode = sandbox
	s.MaxTaskMemoryMB = maxMem
	s.MaxTaskProcs = maxProcs
	s.MaxTaskFileSizeMB = maxFile
	s.Policies = policies
	s.UpdatedAt = updatedAt
	s.mu.Unlock()

	_ = s.SaveToDisk()
	return true
}

// GetLocalIndexDirs returns local_index_dirs copy thread-safely.
func (s *Settings) GetLocalIndexDirs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.LocalIndexDirs == nil {
		return nil
	}
	cp := make([]string, len(s.LocalIndexDirs))
	copy(cp, s.LocalIndexDirs)
	return cp
}

// GetRAGTimeout returns rag_timeout_seconds as a duration thread-safely.
func (s *Settings) GetRAGTimeout() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.RAGTimeoutSeconds <= 0 {
		return 2 * time.Second
	}
	return time.Duration(s.RAGTimeoutSeconds) * time.Second
}
