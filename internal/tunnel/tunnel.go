package tunnel

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/crypto/curve25519"

	"github.com/openfabric/openfabric/internal/config"
)

// TunnelState represents the current operational state of the tunnel.
type TunnelState string

const (
	StateDisconnected TunnelState = "disconnected"
	StateConnecting   TunnelState = "connecting"
	StateConnected    TunnelState = "connected"
	StateError        TunnelState = "error"
)

// TunnelConfig holds all persistent tunnel settings stored at
// ~/.openfabric/tunnel.json
type TunnelConfig struct {
	RelayURL      string    `json:"relay_url"`   // e.g. "relay.localfabric.dev:51820"
	RelayHTTPS    string    `json:"relay_https"` // e.g. "https://relay.localfabric.dev"
	PrivateKey    string    `json:"private_key"` // WireGuard base64 private key
	PublicKey     string    `json:"public_key"`  // WireGuard base64 public key
	AssignedIP    string    `json:"assigned_ip"` // e.g. "10.8.0.1/24"
	TunnelID      string    `json:"tunnel_id"`   // UUID registered with relay
	TunnelSecret  string    `json:"tunnel_secret"`
	PINHash       string    `json:"pin_hash"`    // bcrypt hash of browser PIN
	PINEnabled    bool      `json:"pin_enabled"`
	LastConnected time.Time `json:"last_connected"`
}

// PeerInfo describes a remote device connected through the tunnel.
type PeerInfo struct {
	NodeID    string    `json:"node_id"`
	PublicKey string    `json:"public_key"`
	TunnelIP  string    `json:"tunnel_ip"`
	LastSeen  time.Time `json:"last_seen"`
	LatencyMs int64     `json:"latency_ms"`
	BytesRx   int64     `json:"bytes_rx"`
	BytesTx   int64     `json:"bytes_tx"`
}

// Manager orchestrates the full tunnel lifecycle.
type Manager struct {
	mu            sync.RWMutex
	cfg           *TunnelConfig
	state         TunnelState
	peers         map[string]*PeerInfo
	wg            *WireGuardManager
	relay         *RelayClient
	proxy         *ReverseProxy
	pin           *PINManager
	cfgPath       string
	log           *zap.Logger
	stateC        chan TunnelState // broadcast state changes to SSE
	embeddedRelay *EmbeddedRelay   // local relay server running in-process
}

// NewManager creates a Manager loading config from the given directory.
func NewManager(dataDir string, log *zap.Logger) (*Manager, error) {
	l := log.Named("tunnel")

	// Start the embedded relay server so the tunnel feature works
	// out of the box without needing an external relay deployment.
	er, err := NewEmbeddedRelay(EmbeddedRelayPort, log)
	if err != nil {
		l.Warn("failed to start embedded relay – tunnel will require an external relay", zap.Error(err))
	}

	m := &Manager{
		cfgPath:       filepath.Join(dataDir, "tunnel.json"),
		peers:         make(map[string]*PeerInfo),
		stateC:        make(chan TunnelState, 8),
		log:           l,
		embeddedRelay: er,
	}

	if err := m.loadOrInitConfig(); err != nil {
		return nil, fmt.Errorf("tunnel config: %w", err)
	}

	m.wg = NewWireGuardManager(m.cfg, m.log)
	m.relay = NewRelayClient(m.cfg)
	m.proxy = NewReverseProxy(4892, m.log)
	m.pin = NewPINManager(m.cfg, m.log)
	m.state = StateDisconnected

	return m, nil
}

// Enable starts the tunnel: generates keys if needed, registers with relay,
// optionally brings up WireGuard interface, and starts the reverse proxy.
// If WireGuard is not available, operates in relay-only mode.
func (m *Manager) Enable(ctx context.Context) error {
	m.setState(StateConnecting)

	// Step 1: ensure we have a WireGuard keypair
	m.mu.Lock()
	needKeys := m.cfg.PrivateKey == ""
	m.mu.Unlock()

	if needKeys {
		priv, pub, err := generateWireGuardKeypair()
		if err != nil {
			m.setState(StateError)
			return fmt.Errorf("keygen: %w", err)
		}
		m.mu.Lock()
		m.cfg.PrivateKey = priv
		m.cfg.PublicKey = pub
		m.mu.Unlock()
		m.log.Info("generated new WireGuard keypair")
	}

	// Step 2: register with relay to get a tunnel ID and assigned IP
	m.mu.RLock()
	pubKey := m.cfg.PublicKey
	m.mu.RUnlock()

	m.log.Info("registering tunnel with relay", zap.String("relay", m.cfg.RelayHTTPS))
	reg, err := m.relay.Register(ctx, pubKey)
	if err != nil {
		m.setState(StateError)
		return fmt.Errorf("relay register: %w", err)
	}

	m.mu.Lock()
	m.cfg.TunnelID = reg.TunnelID
	m.cfg.AssignedIP = reg.AssignedIP
	m.cfg.TunnelSecret = reg.TunnelSecret
	m.mu.Unlock()
	m.log.Info("registered tunnel successfully", zap.String("ip", reg.AssignedIP), zap.String("id", reg.TunnelID))

	// Step 3: bring up WireGuard interface (optional - skipped when not available)
	wgUp := false
	if m.wg.Available() {
		if err := m.wg.Up(ctx, reg.RelayPubKey); err != nil {
			m.log.Warn("WireGuard unavailable, continuing in relay-only mode", zap.Error(err))
		} else {
			wgUp = true
		}
	} else {
		_, errWg := exec.LookPath("wireguard-go")
		if errWg == nil && !checkIsRoot() {
			m.log.Warn("WireGuard is installed but requires administrative/root privileges to create TUN interfaces. Continuing in relay-only mode. To enable direct P2P tunnels, please run OpenFabric as root or Administrator.")
		} else {
			m.log.Info("WireGuard not installed or unavailable - operating in relay-only mode (traffic proxied through relay)")
		}
	}

	// Step 4: start reverse proxy (dashboard accessible over tunnel)
	m.proxy.SetPINManager(m.pin)
	listenIP := m.cfg.AssignedIP
	if !wgUp {
		// In relay-only mode, bind proxy to localhost since there's no WG interface
		listenIP = "127.0.0.1/24"
	}
	if err := m.proxy.Start(listenIP); err != nil {
		if wgUp {
			m.wg.Down()
		}
		m.setState(StateError)
		return fmt.Errorf("proxy start: %w", err)
	}

	// Step 5: start peer heartbeat loop
	go m.heartbeatLoop(ctx)

	m.mu.Lock()
	m.cfg.LastConnected = time.Now()
	m.mu.Unlock()
	if err := m.saveConfig(); err != nil {
		m.log.Warn("failed to save config", zap.Error(err))
	}

	m.setState(StateConnected)
	return nil
}

// Disable tears down the tunnel cleanly.
func (m *Manager) Disable() error {
	m.setState(StateDisconnected)

	// Stop proxy and deregister asynchronously so that if this request was received
	// over the tunnel itself, the HTTP server can finish sending the response
	// before the connection is shut down.
	go func() {
		time.Sleep(200 * time.Millisecond)
		m.proxy.Stop()
		m.wg.Down()
		m.mu.RLock()
		tunnelID := m.cfg.TunnelID
		m.mu.RUnlock()
		if tunnelID != "" {
			m.relay.Deregister(tunnelID)
		}
		m.log.Info("tunnel disabled and cleaned up")
	}()

	return nil
}


// Stop shuts down the embedded relay and all tunnel resources.
func (m *Manager) Stop() {
	if m.embeddedRelay != nil {
		m.embeddedRelay.Stop()
	}
}

// Status returns a snapshot of the current tunnel state for the API.
func (m *Manager) Status() map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	peers := make([]*PeerInfo, 0, len(m.peers))
	for _, p := range m.peers {
		peers = append(peers, p)
	}
	return map[string]any{
		"state":       m.state,
		"tunnel_id":   m.cfg.TunnelID,
		"assigned_ip": m.cfg.AssignedIP,
		"relay_url":   m.cfg.RelayURL,
		"public_key":  m.cfg.PublicKey,
		"peers":       peers,
		"pin_enabled": m.cfg.PINEnabled,
		"tunnel_url":  m.tunnelURL(),
		"relay_only":  !m.wg.Available(),
	}
}

// StateChan returns a channel to watch state transitions.
func (m *Manager) StateChan() <-chan TunnelState {
	return m.stateC
}

// GeneratePIN triggers Generation of a new PIN.
func (m *Manager) GeneratePIN() (string, error) {
	pin, err := m.pin.GeneratePIN()
	if err != nil {
		return "", err
	}
	if err := m.saveConfig(); err != nil {
		m.log.Warn("failed to save config with PIN hash", zap.Error(err))
	}
	return pin, nil
}

// RevokePIN disables the current PIN.
func (m *Manager) RevokePIN() error {
	m.pin.RevokePIN()
	return m.saveConfig()
}

// UpdateRelay configures a custom relay URL.
func (m *Manager) UpdateRelay(urlStr string) error {
	m.mu.Lock()
	m.cfg.RelayURL = urlStr
	m.mu.Unlock()
	m.relay.UpdateRelay(urlStr)
	return m.saveConfig()
}

// GetConfig returns the WireGuard config string.
func (m *Manager) GetConfig() (string, error) {
	return m.wg.WireGuardConfig()
}

// tunnelURL returns the shareable HTTPS URL for remote dashboard access.
func (m *Manager) tunnelURL() string {
	if m.cfg.TunnelID == "" {
		return ""
	}
	return fmt.Sprintf("%s/t/%s", m.cfg.RelayHTTPS, m.cfg.TunnelID)
}

// heartbeatLoop pings the relay every 25 seconds to maintain the WireGuard
// session and refreshes peer latency measurements.
func (m *Manager) heartbeatLoop(ctx context.Context) {
	tick := time.NewTicker(25 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			m.mu.RLock()
			tunnelID := m.cfg.TunnelID
			state := m.state
			m.mu.RUnlock()

			if state != StateConnected || tunnelID == "" {
				continue
			}

			peers, err := m.relay.ListPeers(ctx, tunnelID)
			if err != nil {
				m.log.Warn("failed to list peers from relay", zap.Error(err))
				continue
			}

			m.mu.Lock()
			for _, p := range peers {
				existing, ok := m.peers[p.PublicKey]
				if !ok {
					m.peers[p.PublicKey] = p
					// add WireGuard peer config on new join
					_ = m.wg.AddPeer(p.PublicKey, p.TunnelIP)
					m.log.Info("added new tunnel peer", zap.String("peer_ip", p.TunnelIP))
				} else {
					existing.LastSeen = p.LastSeen
					existing.LatencyMs = m.pingPeer(p.TunnelIP)
				}
			}
			m.mu.Unlock()
		}
	}
}

// pingPeer sends a single ICMP echo/TCP ping and returns round-trip ms, or -1 on fail.
func (m *Manager) pingPeer(ip string) int64 {
	start := time.Now()
	// Strip CIDR suffix if any
	host := ip
	if idx := filepath.Separator; idx == '/' { // use normal string splitting
		if i := stringsIndex(ip, "/"); i != -1 {
			host = ip[:i]
		}
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, "4892"), 2*time.Second)
	if err != nil {
		return -1
	}
	conn.Close()
	return time.Since(start).Milliseconds()
}

func stringsIndex(s, sep string) int {
	for i := 0; i <= len(s)-len(sep); i++ {
		if s[i:i+len(sep)] == sep {
			return i
		}
	}
	return -1
}

func (m *Manager) setState(s TunnelState) {
	m.mu.Lock()
	m.state = s
	m.mu.Unlock()
	select {
	case m.stateC <- s:
	default:
	}
}

func (m *Manager) loadOrInitConfig() error {
	data, err := os.ReadFile(m.cfgPath)
	if os.IsNotExist(err) {
		m.cfg = &TunnelConfig{}
	} else if err != nil {
		return err
	} else {
		m.cfg = &TunnelConfig{}
		if err := json.Unmarshal(data, m.cfg); err != nil {
			return err
		}
	}

	// Migration: clear any old production-domain defaults that are unreachable.
	// This covers both the legacy hardcoded values and the previously generated
	// "relay.<ProjectDomain>" values which require DNS that may not exist yet.
	oldPrefixes := []string{
		"relay.openfabric.dev",
		"relay.localfabric.dev",
		"relay." + config.ProjectDomain,
	}
	for _, prefix := range oldPrefixes {
		if m.cfg.RelayURL == prefix+":51820" {
			m.cfg.RelayURL = ""
		}
		if m.cfg.RelayHTTPS == "https://"+prefix {
			m.cfg.RelayHTTPS = ""
		}
	}

	// Default to the embedded local relay so everything works out of the box.
	// Users can override these in the UI to point to a production relay.
	if m.cfg.RelayURL == "" {
		m.cfg.RelayURL = fmt.Sprintf("127.0.0.1:%d", EmbeddedRelayPort)
	}
	if m.cfg.RelayHTTPS == "" {
		m.cfg.RelayHTTPS = fmt.Sprintf("http://127.0.0.1:%d", EmbeddedRelayPort)
	}

	m.log.Info("tunnel config loaded",
		zap.String("relay_url", m.cfg.RelayURL),
		zap.String("relay_https", m.cfg.RelayHTTPS),
	)

	// Save configuration back to persist defaults/migrations
	return m.saveConfig()
}

func (m *Manager) saveConfig() error {
	m.mu.RLock()
	cfg := m.cfg
	m.mu.RUnlock()
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.cfgPath, data, 0600)
}

// generateWireGuardKeypair creates a new Curve25519 keypair for WireGuard.
// Returns base64-encoded (private, public).
func generateWireGuardKeypair() (string, string, error) {
	var priv [32]byte
	if _, err := rand.Read(priv[:]); err != nil {
		return "", "", err
	}
	// Clamp private key keys per RFC 7748
	priv[0] &= 248
	priv[31] = (priv[31] & 127) | 64

	pub, err := deriveCurve25519PublicKey(priv)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(priv[:]),
		base64.StdEncoding.EncodeToString(pub[:]),
		nil
}

// deriveCurve25519PublicKey computes Curve25519 public key using x/crypto/curve25519
func deriveCurve25519PublicKey(priv [32]byte) ([32]byte, error) {
	var pub [32]byte
	curve25519.ScalarBaseMult(&pub, &priv)
	return pub, nil
}
