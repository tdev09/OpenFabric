package tunnel

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/crypto/curve25519"
)

// EmbeddedRelay is a lightweight relay broker that runs inside the agent
// process. It provides tunnel registration, peer listing, and reverse-proxy
// endpoints so the tunnel feature works out of the box without a separately
// deployed relay server.
type EmbeddedRelay struct {
	mu        sync.RWMutex
	tunnels   map[string]*relayTunnelInfo
	publicKey string
	nextOctet int
	port      int
	listener  net.Listener
	log       *zap.Logger
}

type relayTunnelInfo struct {
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

// EmbeddedRelayPort is the default port for the embedded relay server.
const EmbeddedRelayPort = 4893

// NewEmbeddedRelay creates and starts a local relay server on the given port.
func NewEmbeddedRelay(port int, log *zap.Logger) (*EmbeddedRelay, error) {
	// Generate relay keypair
	var priv [32]byte
	if _, err := rand.Read(priv[:]); err != nil {
		return nil, fmt.Errorf("embedded relay keygen: %w", err)
	}
	priv[0] &= 248
	priv[31] = (priv[31] & 127) | 64

	var pub [32]byte
	curve25519.ScalarBaseMult(&pub, &priv)

	r := &EmbeddedRelay{
		tunnels:   make(map[string]*relayTunnelInfo),
		publicKey: base64.StdEncoding.EncodeToString(pub[:]),
		nextOctet: 2,
		port:      port,
		log:       log.Named("embedded-relay"),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/register", r.handleRegister)
	mux.HandleFunc("/api/v1/tunnels/", r.handleTunnelAPI)
	mux.HandleFunc("/t/", r.handleProxy)
	mux.HandleFunc("/", r.handleFallbackProxy)
	mux.HandleFunc("/health", func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "OK. Embedded relay. Active tunnels: %d\n", len(r.tunnels))
	})

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("embedded relay listen %s: %w", addr, err)
	}
	r.listener = ln

	go func() {
		srv := &http.Server{Handler: mux}
		if serveErr := srv.Serve(ln); serveErr != nil && !strings.Contains(serveErr.Error(), "closed") {
			r.log.Warn("embedded relay stopped", zap.Error(serveErr))
		}
	}()

	r.log.Info("embedded relay started", zap.String("addr", addr), zap.String("pubkey", r.publicKey[:12]+"…"))
	return r, nil
}

// Stop shuts down the embedded relay listener.
func (r *EmbeddedRelay) Stop() {
	if r.listener != nil {
		r.listener.Close()
	}
}

// BaseURL returns the HTTP base URL of the embedded relay.
func (r *EmbeddedRelay) BaseURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", r.port)
}

// --- HTTP handlers ---

func (r *EmbeddedRelay) handleRegister(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		PublicKey string `json:"public_key"`
		Version   string `json:"version"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if body.PublicKey == "" {
		http.Error(w, "public_key is required", http.StatusBadRequest)
		return
	}

	r.mu.Lock()
	uuidBytes := make([]byte, 16)
	rand.Read(uuidBytes)
	tunnelID := fmt.Sprintf("%x-%x-%x-%x-%x",
		uuidBytes[0:4], uuidBytes[4:6], uuidBytes[6:8], uuidBytes[8:10], uuidBytes[10:])

	// Generate TunnelSecret (32 secure random bytes encoded in base64 URL-safe)
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		r.mu.Unlock()
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	secret := base64.RawURLEncoding.EncodeToString(secretBytes)

	// Compute SHA-256 hash of secret
	hash := sha256.Sum256([]byte(secret))
	secretHash := fmt.Sprintf("%x", hash[:])

	assignedIP := fmt.Sprintf("10.8.0.%d/24", r.nextOctet)
	r.nextOctet++
	if r.nextOctet > 254 {
		r.nextOctet = 2
	}

	info := &relayTunnelInfo{
		TunnelID:    tunnelID,
		PublicKey:   body.PublicKey,
		AssignedIP:  assignedIP,
		LastSeen:    time.Now(),
		RelayPubKey: r.publicKey,
		SecretHash:  secretHash,
	}
	r.tunnels[tunnelID] = info
	r.mu.Unlock()

	r.log.Info("registered tunnel", zap.String("id", tunnelID[:8]), zap.String("ip", assignedIP))

	w.Header().Set("Content-Type", "application/json")
	// Return TunnelSecret as part of the response
	json.NewEncoder(w).Encode(map[string]any{
		"tunnel_id":     info.TunnelID,
		"assigned_ip":   info.AssignedIP,
		"relay_pub_key": info.RelayPubKey,
		"tunnel_secret": secret,
	})
}

func (r *EmbeddedRelay) handleTunnelAPI(w http.ResponseWriter, req *http.Request) {
	parts := strings.Split(req.URL.Path, "/")
	if len(parts) < 5 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	tunnelID := parts[4]

	// Verify X-Tunnel-Secret header to authorize listing peers or deregistering.
	clientSecret := req.Header.Get("X-Tunnel-Secret")
	if clientSecret == "" {
		http.Error(w, "unauthorized: missing X-Tunnel-Secret header", http.StatusUnauthorized)
		return
	}
	clientHash := sha256.Sum256([]byte(clientSecret))
	clientHashHex := fmt.Sprintf("%x", clientHash[:])

	r.mu.RLock()
	info, ok := r.tunnels[tunnelID]
	r.mu.RUnlock()

	if !ok {
		http.Error(w, "tunnel not found", http.StatusNotFound)
		return
	}

	// Constant-time check against stored secret hash to prevent timing attacks.
	if subtle.ConstantTimeCompare([]byte(info.SecretHash), []byte(clientHashHex)) != 1 {
		http.Error(w, "unauthorized: invalid X-Tunnel-Secret", http.StatusUnauthorized)
		return
	}

	switch req.Method {
	case http.MethodGet:
		r.mu.Lock()
		info.LastSeen = time.Now()
		r.mu.Unlock()
		peers := []*relayTunnelInfo{info}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(peers)

	case http.MethodDelete:
		r.mu.Lock()
		delete(r.tunnels, tunnelID)
		r.mu.Unlock()
		r.log.Info("deregistered tunnel", zap.String("id", tunnelID[:8]))
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (r *EmbeddedRelay) handleProxy(w http.ResponseWriter, req *http.Request) {
	path := req.URL.Path
	r.log.Info("embedded relay handleProxy received request", zap.String("path", path))
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

	r.mu.RLock()
	_, ok := r.tunnels[tunnelID]
	r.mu.RUnlock()

	if !ok {
		r.log.Warn("embedded relay handleProxy tunnel not found", zap.String("tunnel_id", tunnelID))
		r.serveNotFoundPage(w, req)
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

	r.log.Info("embedded relay handleProxy redirecting to root", zap.String("tunnel_id", tunnelID))
	http.Redirect(w, req, "/", http.StatusFound)
}

func (r *EmbeddedRelay) handleFallbackProxy(w http.ResponseWriter, req *http.Request) {
	// Try to get tunnel ID from cookie first
	var tunnelID string
	cookie, err := req.Cookie("tunnel_id")
	if err == nil && cookie.Value != "" {
		tunnelID = cookie.Value
	}

	// Try to get tunnel ID from Referer header if cookie is not sent yet
	if tunnelID == "" {
		referer := req.Header.Get("Referer")
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

	// Fallback to the first active local tunnel if no other method succeeds.
	// Since this is the embedded relay, it only ever handles the local node's tunnel.
	if tunnelID == "" {
		r.mu.RLock()
		for tid := range r.tunnels {
			tunnelID = tid
			break
		}
		r.mu.RUnlock()
	}

	if tunnelID == "" {
		r.serveNotFoundPage(w, req)
		return
	}

	r.mu.RLock()
	_, ok := r.tunnels[tunnelID]
	r.mu.RUnlock()

	if !ok {
		// Stale tunnel ID in cookie/referer. Let's see if we have a new active tunnel.
		var activeID string
		r.mu.RLock()
		for tid := range r.tunnels {
			activeID = tid
			break
		}
		r.mu.RUnlock()

		if activeID != "" {
			// Update the stale cookie with the new active tunnel ID
			http.SetCookie(w, &http.Cookie{
				Name:     "tunnel_id",
				Value:    activeID,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
				MaxAge:   86400, // 24 hours
			})
			tunnelID = activeID
		} else {
			r.serveNotFoundPage(w, req)
			return
		}
	}

	r.log.Info("embedded relay handleFallbackProxy forwarding request to 127.0.0.1:4894", zap.String("path", req.URL.Path))
	targetURL, _ := url.Parse("http://127.0.0.1:4894")
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	originalDirector := proxy.Director
	proxy.Director = func(outReq *http.Request) {
		originalDirector(outReq)
		outReq.Header.Set("X-Forwarded-Host", outReq.Host)
		outReq.Header.Set("X-Tunnel-Proxy", "1")
		outReq.URL.Path = req.URL.Path
		if req.URL.RawQuery != "" {
			outReq.URL.RawQuery = req.URL.RawQuery
		}
	}
	proxy.ServeHTTP(w, req)
}

func (r *EmbeddedRelay) serveNotFoundPage(w http.ResponseWriter, req *http.Request) {
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

