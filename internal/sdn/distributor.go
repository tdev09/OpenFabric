package sdn

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	libp2ppeer "github.com/libp2p/go-libp2p/core/peer"
	libp2pprotocol "github.com/libp2p/go-libp2p/core/protocol"
	"github.com/openfabric/openfabric/internal/network"
)

// SDNRuleSetProtocol is the libp2p protocol ID used to send target node rule sets.
const SDNRuleSetProtocol = libp2pprotocol.ID("/openfabric/sdn/ruleset/1.0.0")

// Distributor handles sending rule sets to target nodes in the cluster.
type Distributor struct {
	host *network.Host
}

// NewDistributor creates a new Distributor.
func NewDistributor(h *network.Host) *Distributor {
	return &Distributor{host: h}
}

// Distribute pushes rule sets to all online nodes in parallel.
func (d *Distributor) Distribute(ctx context.Context, ruleSets map[string]*RuleSet) error {
	log.Printf("[sdn] Distribute: target ruleSets size = %d, self nodeID = %q", len(ruleSets), d.host.NodeID())
	var wg sync.WaitGroup
	errCh := make(chan error, len(ruleSets))

	for nodeID, rs := range ruleSets {
		log.Printf("[sdn] Distribute: processing nodeID = %q", nodeID)
		if nodeID == d.host.NodeID() {
			log.Printf("[sdn] Distribute: skipping self node %q", nodeID)
			// Local node rules will be applied locally by the controller or reconciler.
			continue
		}

		log.Printf("[sdn] Distribute: starting distribution goroutine for node %q", nodeID)
		wg.Add(1)
		go func(nid string, rules *RuleSet) {
			defer wg.Done()
			peerID, err := libp2ppeer.Decode(nid)
			if err != nil {
				errCh <- fmt.Errorf("decode node ID %s: %w", nid, err)
				return
			}

			dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()

			stream, err := d.host.NewStream(dialCtx, peerID, SDNRuleSetProtocol)
			if err != nil {
				errCh <- fmt.Errorf("connect to node %s: %w", nid, err)
				return
			}
			defer stream.Close()

			_ = stream.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := json.NewEncoder(stream).Encode(rules); err != nil {
				_ = stream.Reset()
				errCh <- fmt.Errorf("send rule set to node %s: %w", nid, err)
				return
			}

			// Read acknowledgment byte (1 = success, 0 = fail)
			_ = stream.SetReadDeadline(time.Now().Add(5 * time.Second))
			buf := []byte{0}
			if _, err := stream.Read(buf); err != nil {
				_ = stream.Reset()
				errCh <- fmt.Errorf("failed to read ack from node %s: %w", nid, err)
				return
			}
			if buf[0] != 1 {
				_ = stream.Reset()
				errCh <- fmt.Errorf("node %s rejected ruleset apply", nid)
				return
			}
		}(nodeID, rs)
	}

	wg.Wait()
	close(errCh)

	var errs []string
	for err := range errCh {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return fmt.Errorf("distribution failed: %s", strings.Join(errs, "; "))
	}
	return nil
}
