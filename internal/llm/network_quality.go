package llm

import (
	"fmt"
	"net"
	"sort"
	"time"

	"github.com/multiformats/go-multiaddr"
)

// LatencyStats contains the p50 and p95 latencies for a node.
type LatencyStats struct {
	P50 time.Duration
	P95 time.Duration
}

// MeasureNodeLatency measures round-trip time to a peer node.
// Uses 10 pings and returns p50 and p95 latency.
func MeasureNodeLatency(nodeAddr string, port int) (p50, p95 time.Duration, err error) {
	samples := make([]time.Duration, 10)
	for i := range samples {
		start := time.Now()
		conn, err := net.DialTimeout("tcp",
			fmt.Sprintf("%s:%d", nodeAddr, port), 2*time.Second)
		if err != nil {
			return 0, 0, err
		}
		conn.Close()
		samples[i] = time.Since(start)
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	return samples[4], samples[9], nil // p50 and p95
}

// GetNodeTCPAddr parses the node's addresses to find the first IPv4 TCP address.
func GetNodeTCPAddr(addresses []string) (string, int, error) {
	for _, addrStr := range addresses {
		ma, err := multiaddr.NewMultiaddr(addrStr)
		if err != nil {
			continue
		}
		// Try to extract IP4 and TCP component
		ip4, err := ma.ValueForProtocol(multiaddr.P_IP4)
		if err != nil {
			continue
		}
		tcp, err := ma.ValueForProtocol(multiaddr.P_TCP)
		if err != nil {
			continue
		}
		var port int
		_, errScan := fmt.Sscanf(tcp, "%d", &port)
		if errScan != nil {
			continue
		}
		return ip4, port, nil
	}
	return "", 0, fmt.Errorf("no valid TCP multiaddr found")
}
