package tunnel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// RelayClient communicates with the OpenFabric relay server REST API.
type RelayClient struct {
	cfg     *TunnelConfig
	http    *http.Client
	baseURL string
}

// RegistrationResponse is returned by the relay on successful registration.
type RegistrationResponse struct {
	TunnelID     string `json:"tunnel_id"`
	AssignedIP   string `json:"assigned_ip"`
	RelayPubKey  string `json:"relay_pub_key"`
	TunnelSecret string `json:"tunnel_secret,omitempty"`
}

// NewRelayClient creates a client pointed at the configured relay.
func NewRelayClient(cfg *TunnelConfig) *RelayClient {
	return &RelayClient{
		cfg:     cfg,
		baseURL: cfg.RelayHTTPS,
		http: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// Register announces this node to the relay, receiving a tunnel ID, IP, and relay public key.
func (r *RelayClient) Register(ctx context.Context, publicKey string) (*RegistrationResponse, error) {
	body, _ := json.Marshal(map[string]string{
		"public_key": publicKey,
		"version":    "1.1",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		r.baseURL+"/api/v1/register", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("relay unreachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errResp map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&errResp)
		msg := errResp["error"]
		if msg == "" {
			msg = fmt.Sprintf("HTTP status %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("relay registration failed: %s", msg)
	}

	var reg RegistrationResponse
	if err := json.NewDecoder(resp.Body).Decode(&reg); err != nil {
		return nil, fmt.Errorf("decode registration: %w", err)
	}
	return &reg, nil
}

// ListPeers fetches the current list of remote peers registered under the same tunnel.
func (r *RelayClient) ListPeers(ctx context.Context, tunnelID string) ([]*PeerInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/api/v1/tunnels/%s/peers", r.baseURL, tunnelID), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Tunnel-ID", tunnelID)
	req.Header.Set("X-Tunnel-Secret", r.cfg.TunnelSecret)

	resp, err := r.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list peers returned HTTP status %d", resp.StatusCode)
	}

	var peers []*PeerInfo
	if err := json.NewDecoder(resp.Body).Decode(&peers); err != nil {
		return nil, fmt.Errorf("decode peers list: %w", err)
	}
	return peers, nil
}

// Deregister gracefully removes this node from the relay.
func (r *RelayClient) Deregister(tunnelID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		fmt.Sprintf("%s/api/v1/tunnels/%s", r.baseURL, tunnelID), nil)
	if err == nil {
		req.Header.Set("X-Tunnel-ID", tunnelID)
		req.Header.Set("X-Tunnel-Secret", r.cfg.TunnelSecret)
		resp, errDo := r.http.Do(req)
		if errDo == nil {
			resp.Body.Close()
		}
	}
}

// UpdateRelay changes the relay URL at runtime.
func (r *RelayClient) UpdateRelay(urlStr string) {
	host := urlStr
	originalScheme := ""

	if stringsHasPrefix(urlStr, "http://") {
		host = urlStr[7:]
		originalScheme = "http://"
	} else if stringsHasPrefix(urlStr, "https://") {
		host = urlStr[8:]
		originalScheme = "https://"
	}

	isLocal := stringsHasPrefix(host, "localhost") || stringsHasPrefix(host, "127.0.0.1")

	scheme := "https://"
	if isLocal {
		if originalScheme == "http://" || originalScheme == "" {
			scheme = "http://"
		} else {
			scheme = "https://"
		}
	} else {
		scheme = "https://" // Force https for remote WAN
	}

	r.baseURL = scheme + host
	r.cfg.RelayHTTPS = r.baseURL
}

func stringsHasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
