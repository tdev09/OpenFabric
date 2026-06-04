package sdn

import (
	"fmt"
	"strings"
	"sync"
)

// UserspaceStubDataPlane is a safe fallback data plane that tracks rule state in-memory
// and simulates packet filtering. Used for testing and unprivileged mode.
type UserspaceStubDataPlane struct {
	mu      sync.RWMutex
	iface   string
	rules   []*KernelRule
	routes  []*KernelRoute
	bands   []*QoSBand
	applied bool
}

// NewUserspaceStubDataPlane creates a new UserspaceStubDataPlane instance.
func NewUserspaceStubDataPlane(iface string) *UserspaceStubDataPlane {
	if iface == "" {
		iface = "stub0"
	}
	return &UserspaceStubDataPlane{iface: iface}
}

// Apply records the RuleSet in memory.
func (s *UserspaceStubDataPlane) Apply(rs *RuleSet) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.rules = rs.Rules
	s.routes = rs.Routes
	s.bands = rs.QoSBands
	s.applied = true
	return nil
}

// Flush clears all in-memory rules.
func (s *UserspaceStubDataPlane) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.rules = nil
	s.routes = nil
	s.bands = nil
	s.applied = false
	return nil
}

// Status returns a textual description of the rules stored in memory.
func (s *UserspaceStubDataPlane) Status() (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var sb strings.Builder
	sb.WriteString("Userspace Stub Data Plane Status:\n")
	sb.WriteString(fmt.Sprintf("Interface: %s\n", s.iface))
	sb.WriteString(fmt.Sprintf("Rules Applied: %v\n", s.applied))
	sb.WriteString(fmt.Sprintf("Firewall Rules Count: %d\n", len(s.rules)))
	for _, r := range s.rules {
		sb.WriteString(fmt.Sprintf("  - [%s] Prio %d: Action %d, Proto %s, DstCIDRs %v, DstPorts %v (Policy: %s)\n",
			r.ID, r.Priority, int(r.Action), r.Match.Protocol, r.Match.DstCIDRs, r.Match.DstPorts, r.PolicyName))
	}
	sb.WriteString(fmt.Sprintf("QoS Bands Count: %d\n", len(s.bands)))
	for _, b := range s.bands {
		sb.WriteString(fmt.Sprintf("  - [%s] Prio %d: Max %d Bps, Match %v\n",
			b.ID, b.Priority, b.MaxBps, b.Match))
	}
	sb.WriteString(fmt.Sprintf("Routes Count: %d\n", len(s.routes)))
	for _, r := range s.routes {
		sb.WriteString(fmt.Sprintf("  - Dst %s, Via %s, Metric %d, Dev %s\n",
			r.Dst, r.Gateway, r.Metric, r.Dev))
	}
	return sb.String(), nil
}

// DataPlaneInterface returns the stub interface name.
func (s *UserspaceStubDataPlane) DataPlaneInterface() string {
	return s.iface
}
