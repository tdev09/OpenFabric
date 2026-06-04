// Package discovery handles mDNS-based zero-config device discovery on the local network.
package discovery

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"time"

	"github.com/hashicorp/mdns"
	"go.uber.org/zap"
)

const (
	serviceType     = "_openfabric._tcp"
	discoveryDomain = "local."
	queryInterval   = 15 * time.Second
)

// PeerInfo holds basic info about a discovered peer.
type PeerInfo struct {
	ID        string
	Name      string
	Host      string
	Port      int
	Addresses []net.IP
	Platform  string
}

// PeerHandler is called when a new peer is discovered or a known peer is refreshed.
type PeerHandler func(peer PeerInfo)

// Service manages mDNS broadcast and discovery.
type Service struct {
	nodeID   string
	nodeName string
	port     int
	platform string
	handler  PeerHandler
	log      *zap.Logger
	server   *mdns.Server
}

// New creates a discovery Service.
func New(nodeID, nodeName string, port int, platform string, handler PeerHandler, zapLog *zap.Logger) *Service {
	// Redirect the standard logger used by hashicorp/mdns so its noisy IPv6
	// "Failed to bind to udp6" messages don't pollute the agent output.
	// These errors are harmless - mDNS continues on IPv4.
	log.SetOutput(newIPv6SuppressWriter(log.Writer()))

	return &Service{
		nodeID:   nodeID,
		nodeName: nodeName,
		port:     port,
		platform: platform,
		handler:  handler,
		log:      zapLog,
	}
}

// Start begins broadcasting our presence and listening for peers.
// It blocks until ctx is cancelled.
func (s *Service) Start(ctx context.Context) error {
	if err := s.startBroadcast(); err != nil {
		return fmt.Errorf("mdns broadcast: %w", err)
	}
	defer s.stopBroadcast()

	s.log.Info("mDNS broadcast started",
		zap.String("node_id", s.nodeID),
		zap.String("service", serviceType),
		zap.Int("port", s.port),
	)

	// Run initial query then repeat on interval.
	s.query()

	ticker := time.NewTicker(queryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.query()
		}
	}
}

// startBroadcast registers this node as an mDNS service.
func (s *Service) startBroadcast() error {
	// Build TXT records with extra metadata.
	txt := []string{
		"id=" + s.nodeID,
		"platform=" + s.platform,
		"v=1",
	}

	// os.Hostname() on macOS returns e.g. "hostname.local".
	// mDNS appends ".local." itself, so strip any existing suffix first.
	host := strings.TrimSuffix(s.nodeName, ".")
	host = strings.TrimSuffix(host, ".local")
	mDNSHost := host + ".local."

	info, err := mdns.NewMDNSService(
		s.nodeID,
		serviceType,
		discoveryDomain,
		mDNSHost,
		s.port,
		nil, // let mdns detect IPs
		txt,
	)
	if err != nil {
		return err
	}

	server, err := mdns.NewServer(&mdns.Config{Zone: info})
	if err != nil {
		return err
	}
	s.server = server
	return nil
}

// stopBroadcast shuts down the mDNS server.
func (s *Service) stopBroadcast() {
	if s.server != nil {
		s.server.Shutdown() //nolint:errcheck
	}
}

// query sends an mDNS query and calls handler for each response.
func (s *Service) query() {
	entries := make(chan *mdns.ServiceEntry, 16)

	go func() {
		for entry := range entries {
			// Skip ourselves.
			if entry.Name == s.nodeID+"."+serviceType+"."+discoveryDomain {
				continue
			}

			peer := PeerInfo{
				Name:      entry.Name,
				Host:      entry.Host,
				Port:      entry.Port,
				Addresses: []net.IP{},
			}

			if entry.AddrV4 != nil {
				peer.Addresses = append(peer.Addresses, entry.AddrV4)
			}
			if entry.AddrV6 != nil {
				peer.Addresses = append(peer.Addresses, entry.AddrV6)
			}

			// Parse TXT records.
			for _, txt := range entry.InfoFields {
				if len(txt) > 3 && txt[:3] == "id=" {
					peer.ID = txt[3:]
				}
				if len(txt) > 9 && txt[:9] == "platform=" {
					peer.Platform = txt[9:]
				}
			}

			// Skip if we couldn't extract a node ID (not an OpenFabric node).
			if peer.ID == "" || peer.ID == s.nodeID {
				continue
			}

			s.log.Debug("discovered peer", zap.String("peer_id", peer.ID), zap.String("host", peer.Host))
			if s.handler != nil {
				s.handler(peer)
			}
		}
	}()

	params := &mdns.QueryParam{
		Service: serviceType,
		Domain:  "local",
		Timeout: 3 * time.Second,
		Entries: entries,
	}

	if err := mdns.Query(params); err != nil {
		// IPv6 send errors are expected when IPv6 multicast is unavailable (common
		// on many home networks). Suppress to DEBUG - mDNS still works on IPv4.
		if isIPv6Error(err) {
			s.log.Debug("mDNS IPv6 unavailable, using IPv4 only", zap.Error(err))
		} else {
			s.log.Warn("mDNS query error", zap.Error(err))
		}
	}
}

// isIPv6Error returns true for errors caused by missing IPv6 multicast support.
func isIPv6Error(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "udp6") ||
		strings.Contains(msg, "no route to host") ||
		strings.Contains(msg, "sendto") ||
		strings.Contains(msg, "ff02::fb")
}

// ipv6SuppressWriter wraps os.Stderr and drops log lines that are just IPv6
// multicast bind failures emitted by the hashicorp/mdns standard library logger.
type ipv6SuppressWriter struct {
	out io.Writer
}

func newIPv6SuppressWriter(out io.Writer) *ipv6SuppressWriter { return &ipv6SuppressWriter{out: out} }

// Write passes through all log output except IPv6-related mdns bind failures.
func (w *ipv6SuppressWriter) Write(p []byte) (n int, err error) {
	// Drop lines mentioning udp6 or ff02 (IPv6 multicast) - these are
	// informational and harmless when IPv6 multicast isn't routed.
	if bytes.Contains(p, []byte("udp6")) || bytes.Contains(p, []byte("ff02")) {
		return len(p), nil // silently swallow
	}
	// Everything else goes to the default log destination.
	return w.out.Write(p)
}
