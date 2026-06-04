package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	libp2pnetwork "github.com/libp2p/go-libp2p/core/network"
	libp2pprotocol "github.com/libp2p/go-libp2p/core/protocol"
	"go.uber.org/zap"
)

// AgentSwarmProtocolID is the P2P protocol for agent swarm coordination
const AgentSwarmProtocolID = libp2pprotocol.ID("/openfabric/agent-swarm/1.0.0")

// SwarmSpawnRequest represents the incoming command to execute a sub-agent
type SwarmSpawnRequest struct {
	ParentAgentID string   `json:"parent_agent_id"`
	Goal          string   `json:"goal"`
	Tools         []string `json:"tools"`
}

// SwarmEvent is the streaming message format sent back to the parent agent
type SwarmEvent struct {
	Type    string `json:"type"` // "step_start", "step_complete", "log", "complete", "error"
	StepNum int    `json:"step_num,omitempty"`
	Content string `json:"content,omitempty"`
}

// HandleSwarmStream accepts incoming sub-agent spawning requests from remote nodes
func (m *Manager) HandleSwarmStream(s libp2pnetwork.Stream) {
	defer s.Close()
	remotePeerID := s.Conn().RemotePeer().String()

	// Verify sender peer trust in the cluster
	if m.clusterMgr != nil && !m.clusterMgr.IsPeerTrusted(remotePeerID) {
		m.log.Warn("swarm handler: blocked spawn request from untrusted peer", zap.String("peer_id", remotePeerID))
		return
	}

	dec := json.NewDecoder(s)
	enc := json.NewEncoder(s)

	var req SwarmSpawnRequest
	if err := dec.Decode(&req); err != nil {
		m.log.Warn("swarm handler: failed to decode spawn request", zap.Error(err))
		_ = enc.Encode(SwarmEvent{Type: "error", Content: "decode request: " + err.Error()})
		return
	}

	m.log.Info("swarm handler: spawning sub-agent actor",
		zap.String("parent_agent_id", req.ParentAgentID),
		zap.String("goal", req.Goal),
		zap.String("peer_id", remotePeerID),
	)

	// Create local sub-agent
	agent, err := m.CreateAgent(req.Goal, req.Tools)
	if err != nil {
		m.log.Error("swarm handler: failed to create sub-agent", zap.Error(err))
		_ = enc.Encode(SwarmEvent{Type: "error", Content: "create agent: " + err.Error()})
		return
	}

	eventCh := make(chan SwarmEvent, 128)
	listenerID := fmt.Sprintf("swarm-%s", agent.ID)

	var once sync.Once
	closeEventCh := func() {
		once.Do(func() {
			close(eventCh)
		})
	}

	m.AddListener(listenerID, func(event string, payload any) {
		if event == "agent_updated" {
			ag, ok := payload.(*Agent)
			if ok && ag.ID == agent.ID {
				switch ag.Status {
				case "completed":
					select {
					case eventCh <- SwarmEvent{Type: "complete", Content: ag.Output}:
					default:
					}
					closeEventCh()
				case "failed":
					select {
					case eventCh <- SwarmEvent{Type: "error", Content: ag.Error}:
					default:
					}
					closeEventCh()
				case "cancelled":
					select {
					case eventCh <- SwarmEvent{Type: "error", Content: "sub-agent execution cancelled"}:
					default:
					}
					closeEventCh()
				case "running":
					if len(ag.Steps) > 0 {
						latestStep := ag.Steps[len(ag.Steps)-1]
						var ev SwarmEvent
						if latestStep.Status == "running" {
							ev = SwarmEvent{
								Type:    "log",
								StepNum: latestStep.Number,
								Content: latestStep.Log,
							}
						} else if latestStep.Status == "completed" {
							ev = SwarmEvent{
								Type:    "step_complete",
								StepNum: latestStep.Number,
								Content: fmt.Sprintf("Executed tool %s: %s", latestStep.Tool, latestStep.Log),
							}
						} else if latestStep.Status == "failed" {
							ev = SwarmEvent{
								Type:    "log",
								StepNum: latestStep.Number,
								Content: fmt.Sprintf("Tool %s failed: %s", latestStep.Tool, latestStep.Result),
							}
						}

						if ev.Type != "" {
							select {
							case eventCh <- ev:
							default:
							}
						}
					}
				}
			}
		}
	})
	defer func() {
		m.RemoveListener(listenerID)
		closeEventCh()
	}()

	// Monitor stream closure in background to support propagation of cancellations
	cancelCtx, cancelFunc := context.WithCancel(context.Background())
	defer cancelFunc()

	go func() {
		buf := make([]byte, 1)
		for {
			select {
			case <-cancelCtx.Done():
				return
			default:
				_ = s.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
				_, err := s.Read(buf)
				if err != nil {
					if err == io.EOF || strings.Contains(err.Error(), "reset") || strings.Contains(err.Error(), "closed") {
						m.log.Info("swarm handler: connection lost, cancelling sub-agent", zap.String("agent_id", agent.ID))
						_ = m.CancelAgent(agent.ID)
						return
					}
				}
			}
		}
	}()

	// Execute local agent ReAct loop
	if err := m.StartAgent(agent.ID); err != nil {
		m.log.Error("swarm handler: failed to start sub-agent", zap.Error(err))
		_ = enc.Encode(SwarmEvent{Type: "error", Content: "start agent: " + err.Error()})
		return
	}

	// Read and forward events to remote peer
	for ev := range eventCh {
		if err := enc.Encode(ev); err != nil {
			m.log.Warn("swarm handler: stream write error", zap.Error(err))
			return
		}
	}
}
