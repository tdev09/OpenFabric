package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/curve25519"
)

// TunnelInfo describes a registered client tunnel connection.
type TunnelInfo struct {
	TunnelID     string    `json:"tunnel_id"`
	PublicKey    string    `json:"public_key"`
	AssignedIP   string    `json:"assigned_ip"`
	LastSeen     time.Time `json:"last_seen"`
	BytesRx      int64     `json:"bytes_rx"`
	BytesTx      int64     `json:"bytes_tx"`
	NodeID       string    `json:"node_id"`
	RelayPubKey  string    `json:"relay_pub_key"`
	SecretHash   string    `json:"-"`
}

type RelayServer struct {
	mu          sync.RWMutex
	tunnels     map[string]*TunnelInfo // keyed by TunnelID (UUID)
	publicKey   string
	privateKey  string
	ipPool      map[string]bool // ip -> allocated
	nextOctet   int
	dashboardURL string
}

func NewRelayServer() *RelayServer {
	// Generate public/private keypair for the relay WireGuard peer
	var priv [32]byte
	rand.Read(priv[:])
	priv[0] &= 248
	priv[31] = (priv[31] & 127) | 64

	var pub [32]byte
	curve25519.ScalarBaseMult(&pub, &priv)

	s := &RelayServer{
		tunnels:    make(map[string]*TunnelInfo),
		privateKey: base64.StdEncoding.EncodeToString(priv[:]),
		publicKey:  base64.StdEncoding.EncodeToString(pub[:]),
		ipPool:     make(map[string]bool),
		nextOctet:  2, // 10.8.0.1 is the relay interface IP, assign starting from 10.8.0.2
	}
	s.ipPool["10.8.0.1"] = true
	return s
}

func main() {
	port := flag.Int("port", 443, "Relay HTTP/HTTPS port")
	wgPort := flag.Int("wg-port", 51820, "Relay WireGuard UDP port")
	flag.Parse()

	server := NewRelayServer()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/register", server.handleRegister)
	mux.HandleFunc("/api/v1/tunnels/", server.handleTunnelAPI)
	mux.HandleFunc("/t/", server.handleProxy)
	mux.HandleFunc("/", server.handleFallbackProxy)

	// Status endpoint for health checks
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "OK. Relay WireGuard port: %d. Active tunnels: %d\n", *wgPort, len(server.tunnels))
	})

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("[relay] starting server on http://localhost%s (WireGuard UDP port: %d)", addr, *wgPort)
	log.Printf("[relay] public key: %s", server.publicKey)

	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("[relay] failed to start server: %v", err)
	}
}

func (s *RelayServer) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		PublicKey string `json:"public_key"`
		Version   string `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.PublicKey == "" {
		http.Error(w, "public_key is required", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	// Generate new TunnelID (UUID representation)
	uuidBytes := make([]byte, 16)
	rand.Read(uuidBytes)
	tunnelID := fmt.Sprintf("%x-%x-%x-%x-%x",
		uuidBytes[0:4], uuidBytes[4:6], uuidBytes[6:8], uuidBytes[8:10], uuidBytes[10:])

	// Generate TunnelSecret (32 secure random bytes encoded in base64 URL-safe)
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		s.mu.Unlock()
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	secret := base64.RawURLEncoding.EncodeToString(secretBytes)

	// Compute SHA-256 hash of secret
	hash := sha256.Sum256([]byte(secret))
	secretHash := fmt.Sprintf("%x", hash[:])

	// Allocate next free IP
	assignedIP := fmt.Sprintf("10.8.0.%d/24", s.nextOctet)
	s.nextOctet++
	if s.nextOctet > 254 {
		s.nextOctet = 2 // Wrap around (in production would recycle IPs)
	}

	info := &TunnelInfo{
		TunnelID:    tunnelID,
		PublicKey:   req.PublicKey,
		AssignedIP:  assignedIP,
		LastSeen:    time.Now(),
		RelayPubKey: s.publicKey,
		SecretHash:  secretHash,
	}
	s.tunnels[tunnelID] = info
	s.mu.Unlock()

	log.Printf("[relay] registered tunnel %s -> IP: %s (pubkey: %s)", tunnelID[:8], assignedIP, req.PublicKey[:12])

	w.Header().Set("Content-Type", "application/json")
	// Return TunnelSecret as part of the response
	json.NewEncoder(w).Encode(map[string]any{
		"tunnel_id":     info.TunnelID,
		"assigned_ip":   info.AssignedIP,
		"relay_pub_key": info.RelayPubKey,
		"tunnel_secret": secret,
	})
}

func (s *RelayServer) handleTunnelAPI(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 5 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	tunnelID := parts[4]

	// Verify X-Tunnel-Secret header to authorize listing peers or deregistering.
	clientSecret := r.Header.Get("X-Tunnel-Secret")
	if clientSecret == "" {
		http.Error(w, "unauthorized: missing X-Tunnel-Secret header", http.StatusUnauthorized)
		return
	}
	clientHash := sha256.Sum256([]byte(clientSecret))
	clientHashHex := fmt.Sprintf("%x", clientHash[:])

	s.mu.RLock()
	info, ok := s.tunnels[tunnelID]
	s.mu.RUnlock()

	if !ok {
		http.Error(w, "tunnel not found", http.StatusNotFound)
		return
	}

	// Constant-time check against stored secret hash to prevent timing attacks.
	if subtle.ConstantTimeCompare([]byte(info.SecretHash), []byte(clientHashHex)) != 1 {
		http.Error(w, "unauthorized: invalid X-Tunnel-Secret", http.StatusUnauthorized)
		return
	}

	switch r.Method {
	case http.MethodGet:
		// Return all peer nodes for this tunnel ID (e.g. peer information)
		s.mu.Lock()
		info.LastSeen = time.Now()
		s.mu.Unlock()

		peers := []*TunnelInfo{info} // simple peer array including coordinator
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(peers)

	case http.MethodDelete:
		s.mu.Lock()
		delete(s.tunnels, tunnelID)
		s.mu.Unlock()
		log.Printf("[relay] deregistered tunnel %s", tunnelID[:8])
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleProxy intercepts web traffic to /t/{tunnel_id}/* and redirects to /
// after setting a cookie to bypass SvelteKit SPA routing 404 client-side loops.
func (s *RelayServer) handleProxy(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if !strings.HasPrefix(path, "/t/") {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	parts := strings.Split(strings.TrimPrefix(path, "/t/"), "/")
	if len(parts) < 1 || parts[0] == "" {
		http.Error(w, "missing tunnel ID", http.StatusBadRequest)
		return
	}
	tunnelID := parts[0]

	s.mu.RLock()
	_, ok := s.tunnels[tunnelID]
	s.mu.RUnlock()

	if !ok {
		s.serveNotFoundPage(w, r)
		return
	}

	// Set the tunnel ID cookie and redirect to root /
	http.SetCookie(w, &http.Cookie{
		Name:     "tunnel_id",
		Value:    tunnelID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400, // 24 hours
	})

	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *RelayServer) handleFallbackProxy(w http.ResponseWriter, r *http.Request) {
	// Try to get tunnel ID from cookie first
	var tunnelID string
	cookie, err := r.Cookie("tunnel_id")
	if err == nil && cookie.Value != "" {
		tunnelID = cookie.Value
	}

	// Try to get tunnel ID from Referer header if cookie is not sent yet
	if tunnelID == "" {
		referer := r.Header.Get("Referer")
		if referer != "" {
			parts := strings.Split(referer, "/t/")
			if len(parts) > 1 {
				subParts := strings.Split(parts[1], "/")
				if len(subParts) > 0 && subParts[0] != "" {
					tunnelID = subParts[0]
				}
			}
		}
	}

	if tunnelID == "" {
		s.serveNotFoundPage(w, r)
		return
	}

	s.mu.RLock()
	info, ok := s.tunnels[tunnelID]
	s.mu.RUnlock()

	if !ok {
		s.serveNotFoundPage(w, r)
		return
	}

	targetIP := strings.Split(info.AssignedIP, "/")[0]
	targetURL, err := url.Parse(fmt.Sprintf("http://%s:4894", targetIP))
	if err != nil {
		http.Error(w, "internal routing error", http.StatusInternalServerError)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Header.Set("X-Forwarded-Host", req.Host)
		req.Header.Set("X-Tunnel-Proxy", "1")
		req.URL.Path = r.URL.Path
		if r.URL.RawQuery != "" {
			req.URL.RawQuery = r.URL.RawQuery
		}
	}
	proxy.ServeHTTP(w, r)
}

func (s *RelayServer) serveNotFoundPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusNotFound)
	fmt.Fprint(w, notFoundPageHTML)
}

const notFoundPageHTML = `<!DOCTYPE html>
<html>
<head>
  <title>OpenFabric - Connection Offline</title>
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <style>
    body {
      background: #0B0E14;
      color: #94A3B8;
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
      display: flex;
      align-items: center;
      justify-content: center;
      height: 100vh;
      margin: 0;
      overflow: hidden;
    }
    .card {
      background: #111827;
      border: 1px solid #1F2937;
      border-radius: 16px;
      padding: 40px;
      text-align: center;
      width: 360px;
      box-shadow: 0 10px 25px -5px rgba(0, 0, 0, 0.5), 0 8px 10px -6px rgba(0, 0, 0, 0.5);
      animation: fadeIn 0.4s ease-out;
    }
    .icon-wrapper {
      width: 64px;
      height: 64px;
      background: rgba(248, 81, 73, 0.1);
      border: 1px solid rgba(248, 81, 73, 0.2);
      border-radius: 50%;
      display: flex;
      align-items: center;
      justify-content: center;
      margin: 0 auto 24px;
      color: #F85149;
    }
    h2 {
      color: #F3F4F6;
      margin: 0 0 12px;
      font-weight: 600;
      font-size: 20px;
      letter-spacing: -0.025em;
    }
    p {
      color: #9CA3AF;
      font-size: 14px;
      margin: 0 0 28px;
      line-height: 1.6;
    }
    .btn {
      display: inline-block;
      width: 100%;
      padding: 12px;
      background: #00C9A7;
      border: none;
      border-radius: 8px;
      color: #0B0E14;
      font-weight: 700;
      font-size: 15px;
      text-decoration: none;
      cursor: pointer;
      box-sizing: border-box;
      transition: background 0.2s, transform 0.1s;
    }
    .btn:hover {
      background: #00B395;
    }
    .btn:active {
      transform: scale(0.98);
    }
    @keyframes fadeIn {
      from { opacity: 0; transform: translateY(10px); }
      to { opacity: 1; transform: translateY(0); }
    }
  </style>
</head>
<body>
  <div class="card">
    <div class="icon-wrapper">
      <svg width="28" height="28" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
        <path d="M18 10h-1.26A8 8 0 1 0 9 20h9a5 5 0 0 0 0-10z"></path>
        <line x1="1" y1="1" x2="23" y2="23"></line>
      </svg>
    </div>
    <h2>Connection Offline</h2>
    <p>This secure tunnel connection has been disabled or is no longer active. Re-enable the tunnel from your local dashboard to restore access.</p>
    <button class="btn" onclick="window.location.reload()">Retry Connection</button>
  </div>
</body>
</html>`


