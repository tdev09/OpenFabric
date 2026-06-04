// Package network manages the libp2p P2P host, identity, and encrypted transport.
package network

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	libp2pnetwork "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	libp2pprotocol "github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	"github.com/multiformats/go-multiaddr"
	"go.uber.org/zap"
)

// savedIdentity is the on-disk format for a persistent node keypair.
type savedIdentity struct {
	PrivateKey []byte `json:"private_key"`
}

// DefaultRelays are public bootstrap relay nodes to support WAN P2P hole-punching.
var DefaultRelays = []string{
	"/ip4/104.131.131.82/tcp/4001/p2p/QmaCpDMGvV2mUKiK7rfv1gZjwroGRB96RNNLD5ALnhH1Vz", // bootstrap.libp2p.io
	"/ip4/104.248.58.125/tcp/4001/p2p/QmNnooDu7bfjEDo6qhpvpR29H39vq1A9tB6ZUz1mLDfCUu",
	"/ip4/209.94.90.160/tcp/4001/p2p/QmZa1sAxmqPVQu77YqMpc3S29VjU3ZkYwcX9cQ2GgT4p3f",
}

// Host wraps a libp2p host with helpers for OpenFabric.
type Host struct {
	host.Host
	id         string
	privKey    crypto.PrivKey
	log        *zap.Logger
	meshRouter *MeshRouter
	handlersMu sync.RWMutex
	handlers   map[libp2pprotocol.ID]libp2pnetwork.StreamHandler
}

// NewHost creates (or loads) a persistent Ed25519 identity and starts a libp2p host.
func NewHost(dataDir string, listenPort int, log *zap.Logger) (*Host, error) {
	priv, err := loadOrCreateIdentity(dataDir, log)
	if err != nil {
		return nil, fmt.Errorf("identity: %w", err)
	}

	listenAddr, _ := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", listenPort+1))

	// Parse default bootstrap relays for AutoRelay.
	var staticRelays []peer.AddrInfo
	for _, addrStr := range DefaultRelays {
		ma, err := multiaddr.NewMultiaddr(addrStr)
		if err != nil {
			log.Warn("failed to parse default relay multiaddr", zap.String("addr", addrStr), zap.Error(err))
			continue
		}
		info, err := peer.AddrInfoFromP2pAddr(ma)
		if err != nil {
			log.Warn("failed to resolve relay peer info", zap.String("addr", addrStr), zap.Error(err))
			continue
		}
		staticRelays = append(staticRelays, *info)
	}

	h, err := libp2p.New(
		libp2p.Identity(priv),
		libp2p.ListenAddrs(listenAddr),
		libp2p.Security(noise.ID, noise.New),
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.NATPortMap(),
		libp2p.EnableNATService(),
		libp2p.EnableHolePunching(),
		libp2p.EnableRelay(),
		libp2p.EnableAutoRelayWithStaticRelays(staticRelays),
	)
	if err != nil {
		return nil, fmt.Errorf("libp2p host: %w", err)
	}

	nodeID := h.ID().String()
	log.Info("libp2p host ready",
		zap.String("peer_id", nodeID),
		zap.Any("addrs", h.Addrs()),
	)

	return &Host{
		Host:     h,
		id:       nodeID,
		privKey:  priv,
		log:      log,
		handlers: make(map[libp2pprotocol.ID]libp2pnetwork.StreamHandler),
	}, nil
}

// SetMeshRouter sets the mesh router for multi-hop stream relaying.
func (h *Host) SetMeshRouter(r *MeshRouter) {
	h.meshRouter = r
}

// SetStreamHandler registers a stream handler both locally for loopback routing and on the libp2p host.
func (h *Host) SetStreamHandler(pid libp2pprotocol.ID, handler libp2pnetwork.StreamHandler) {
	h.handlersMu.Lock()
	if h.handlers == nil {
		h.handlers = make(map[libp2pprotocol.ID]libp2pnetwork.StreamHandler)
	}
	h.handlers[pid] = handler
	h.handlersMu.Unlock()

	h.Host.SetStreamHandler(pid, handler)
}

// GetStreamHandler returns the registered local stream handler for the protocol, if any.
func (h *Host) GetStreamHandler(pid libp2pprotocol.ID) libp2pnetwork.StreamHandler {
	h.handlersMu.RLock()
	defer h.handlersMu.RUnlock()
	if h.handlers == nil {
		return nil
	}
	return h.handlers[pid]
}

type pipeStream struct {
	libp2pnetwork.Stream
	r *io.PipeReader
	w *io.PipeWriter
}

func (p *pipeStream) Read(buf []byte) (int, error) {
	return p.r.Read(buf)
}

func (p *pipeStream) Write(buf []byte) (int, error) {
	return p.w.Write(buf)
}

func (p *pipeStream) Close() error {
	err1 := p.r.Close()
	err2 := p.w.Close()
	if err1 != nil {
		return err1
	}
	return err2
}

func (p *pipeStream) CloseWrite() error {
	return p.w.Close()
}

func (p *pipeStream) CloseRead() error {
	return p.r.Close()
}

func (p *pipeStream) Reset() error {
	return p.Close()
}

func createLoopbackStreams() (*pipeStream, *pipeStream) {
	r1, w1 := io.Pipe()
	r2, w2 := io.Pipe()

	s1 := &pipeStream{
		r: r1,
		w: w2,
	}
	s2 := &pipeStream{
		r: r2,
		w: w1,
	}
	return s1, s2
}

// NewStream opens a new stream. If the destination is the local node itself, it uses
// local loopback routing bypass. If a mesh router is configured, it will use it to attempt
// multi-hop routing if the peer is not directly connected.
func (h *Host) NewStream(ctx context.Context, p peer.ID, pids ...libp2pprotocol.ID) (libp2pnetwork.Stream, error) {
	if p.String() == h.id {
		if len(pids) == 0 {
			return nil, fmt.Errorf("no protocol IDs provided")
		}
		protoID := pids[0]
		handler := h.GetStreamHandler(protoID)
		if handler == nil {
			return nil, fmt.Errorf("local loopback: no handler registered for protocol %s", protoID)
		}

		s1, s2 := createLoopbackStreams()
		go handler(s2)

		return s1, nil
	}

	if h.meshRouter != nil {
		return h.meshRouter.NewStream(ctx, p, pids...)
	}
	return h.Host.NewStream(ctx, p, pids...)
}

// PrivateKey returns the host's private key.
func (h *Host) PrivateKey() crypto.PrivKey {
	return h.privKey
}

// Ed25519PrivateKey extracts the raw crypto/ed25519.PrivateKey from the libp2p
// private key. This is used by the Shield audit log for Ed25519 event signing.
// Returns nil if the key is not an Ed25519 key.
func (h *Host) Ed25519PrivateKey() ed25519.PrivateKey {
	rawBytes, err := h.privKey.Raw()
	if err != nil {
		return nil
	}
	// libp2p Ed25519 private keys are stored as the 64-byte seed+public concatenation.
	// crypto/ed25519.PrivateKey is exactly this 64-byte layout.
	if len(rawBytes) == ed25519.PrivateKeySize {
		return ed25519.PrivateKey(rawBytes)
	}
	// Some versions store only the 32-byte seed; expand it.
	if len(rawBytes) == ed25519.SeedSize {
		return ed25519.NewKeyFromSeed(rawBytes)
	}
	return nil
}

// NodeID returns the string peer ID of this host.
func (h *Host) NodeID() string {
	return h.id
}

// ConnectToPeer attempts to connect to a peer by multiaddr string.
func (h *Host) ConnectToPeer(ctx context.Context, addrStr string) error {
	ma, err := multiaddr.NewMultiaddr(addrStr)
	if err != nil {
		return fmt.Errorf("invalid multiaddr: %w", err)
	}

	info, err := peer.AddrInfoFromP2pAddr(ma)
	if err != nil {
		return fmt.Errorf("addr info: %w", err)
	}

	return h.Connect(ctx, *info)
}

// loadOrCreateIdentity loads an Ed25519 keypair from disk, or generates a new one.
func loadOrCreateIdentity(dataDir string, log *zap.Logger) (crypto.PrivKey, error) {
	identPath := filepath.Join(dataDir, "identity.json")

	data, err := os.ReadFile(identPath)
	if err == nil {
		// Existing identity found.
		var saved savedIdentity
		if err := json.Unmarshal(data, &saved); err != nil {
			return nil, fmt.Errorf("parse identity: %w", err)
		}
		priv, err := crypto.UnmarshalPrivateKey(saved.PrivateKey)
		if err != nil {
			return nil, fmt.Errorf("decode keypair: %w", err)
		}
		log.Info("loaded existing identity", zap.String("path", identPath))
		return priv, nil
	}

	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read identity file: %w", err)
	}

	// Generate a fresh Ed25519 keypair.
	priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate keypair: %w", err)
	}

	privBytes, err := crypto.MarshalPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("marshal keypair: %w", err)
	}

	saved := savedIdentity{PrivateKey: privBytes}
	data, err = json.Marshal(saved)
	if err != nil {
		return nil, fmt.Errorf("marshal identity: %w", err)
	}

	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	if err := os.WriteFile(identPath, data, 0600); err != nil {
		return nil, fmt.Errorf("save identity: %w", err)
	}

	log.Info("generated new identity", zap.String("path", identPath))
	return priv, nil
}

// LocalIPs returns all active, non-loopback IPv4 addresses on the host.
func LocalIPs() []string {
	var ips []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() || ip.To4() == nil {
				continue
			}
			ips = append(ips, ip.String())
		}
	}
	return ips
}
