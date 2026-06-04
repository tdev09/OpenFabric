package sdn

import (
	"fmt"
	"sync"
	"time"
)

// FlowTelemetryCollector aggregates and buffers active flow statistics on a node.
type FlowTelemetryCollector struct {
	mu      sync.RWMutex
	flows   map[string]*FlowRecord
	maxSize int
}

// NewFlowTelemetryCollector creates a new FlowTelemetryCollector.
func NewFlowTelemetryCollector(maxSize int) *FlowTelemetryCollector {
	if maxSize <= 0 {
		maxSize = 1000
	}
	return &FlowTelemetryCollector{
		flows:   make(map[string]*FlowRecord),
		maxSize: maxSize,
	}
}

// RecordFlow tracks or updates a network flow record.
func (c *FlowTelemetryCollector) RecordFlow(fr *FlowRecord) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := fmt.Sprintf("%s:%d->%s:%d[%s]", fr.SrcIP, fr.SrcPort, fr.DstIP, fr.DstPort, fr.Proto)
	existing, found := c.flows[key]
	if found {
		existing.BytesTrans += fr.BytesTrans
		existing.PacketsTrans += fr.PacketsTrans
		existing.LastSeen = time.Now()
		if fr.PolicyMatch != "" {
			existing.PolicyMatch = fr.PolicyMatch
		}
	} else {
		if len(c.flows) >= c.maxSize {
			// Bounded memory cap: evict first entry
			for k := range c.flows {
				delete(c.flows, k)
				break
			}
		}
		if fr.FirstSeen.IsZero() {
			fr.FirstSeen = time.Now()
		}
		fr.LastSeen = time.Now()
		c.flows[key] = fr
	}
}

// GetActiveFlows returns the aggregated list of active connection flows.
func (c *FlowTelemetryCollector) GetActiveFlows() []*FlowRecord {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make([]*FlowRecord, 0, len(c.flows))
	for _, fr := range c.flows {
		result = append(result, fr)
	}
	return result
}
