// Package pipeline - pipeline engine unit and integration tests.
package pipeline

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	libp2ppeer "github.com/libp2p/go-libp2p/core/peer"
	libp2ppeerstore "github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/openfabric/openfabric/internal/cluster"
	"github.com/openfabric/openfabric/internal/network"
	"go.uber.org/zap"
)

// Helper to get a free TCP port for testing
func getFreePort() int {
	addr, err := net.ResolveTCPAddr("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		panic(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// TestAudioStreamReader_ReadChunks verifies silence detection VAD splitting on a PCM stream
func TestAudioStreamReader_ReadChunks(t *testing.T) {
	// Let's create dummy raw PCM 16-bit audio data (all zeros represent silence)
	// Sample rate: 16000Hz, 16-bit, 1 channel
	// 1 second of audio = 16000 * 2 bytes = 32000 bytes
	silentAudio := make([]byte, 32000)
	bufReader := bytes.NewReader(silentAudio)

	// silenceDur: 200ms
	asr := NewAudioStreamReader(bufReader, 16000, 16, 1, 200*time.Millisecond)

	var chunkCount int
	var silenceEvents int

	err := asr.ReadChunks(func(chunk []byte, isSegmentComplete bool) {
		chunkCount++
		if isSegmentComplete {
			silenceEvents++
		}
	})

	if err != nil {
		t.Fatalf("ReadChunks failed: %v", err)
	}

	// 1s of audio divided into 100ms frames = 10 chunks
	if chunkCount != 10 {
		t.Errorf("expected 10 chunks, got %d", chunkCount)
	}

	// silenceDur is 200ms. Since we have 1s (1000ms) of pure silence:
	// Chunk 1: 100ms silence
	// Chunk 2: 200ms silence (completes segment) -> silenceEvents = 1
	// Chunk 3: 100ms silence
	// Chunk 4: 200ms silence (completes segment) -> silenceEvents = 2
	// ... and so on. We expect 5 silenceEvents over 10 chunks.
	if silenceEvents != 5 {
		t.Errorf("expected 5 silence segment notifications, got %d", silenceEvents)
	}
}

// TestPipelineOrchestrator_Run sets up a mock multi-node network and executes a streaming run
func TestPipelineOrchestrator_Run(t *testing.T) {
	logger := zap.NewNop()

	// 1. Create temp folders for libp2p host identity configs
	tmpDirA, err := os.MkdirTemp("", "openfabric_pip_a_*")
	if err != nil {
		t.Fatalf("temp dir A: %v", err)
	}
	defer os.RemoveAll(tmpDirA)

	tmpDirB, err := os.MkdirTemp("", "openfabric_pip_b_*")
	if err != nil {
		t.Fatalf("temp dir B: %v", err)
	}
	defer os.RemoveAll(tmpDirB)

	// Create cluster managers
	clusterA := cluster.NewManager(nil)
	clusterB := cluster.NewManager(nil)

	// Get ports
	portA := getFreePort()
	portB := getFreePort()

	// Spin up mock hosts
	hostA, err := network.NewHost(tmpDirA, portA-1, logger)
	if err != nil {
		t.Fatalf("new host A: %v", err)
	}
	defer hostA.Close()

	hostB, err := network.NewHost(tmpDirB, portB-1, logger)
	if err != nil {
		t.Fatalf("new host B: %v", err)
	}
	defer hostB.Close()

	// Setup P2P Trust
	clusterA.TrustPeer(hostB.NodeID())
	clusterB.TrustPeer(hostA.NodeID())

	// Connect hosts at libp2p layer
	targetAddr := hostB.Addrs()[0]
	targetPeerInfo, err := libp2ppeer.AddrInfoFromString(fmt.Sprintf("%s/p2p/%s", targetAddr, hostB.NodeID()))
	if err != nil {
		t.Fatalf("peer info string: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	hostA.Peerstore().AddAddrs(targetPeerInfo.ID, targetPeerInfo.Addrs, libp2ppeerstore.PermanentAddrTTL)
	if err := hostA.Connect(ctx, *targetPeerInfo); err != nil {
		t.Fatalf("failed to connect host A to host B: %v", err)
	}

	// Register cluster nodes
	clusterA.Upsert(&cluster.NodeInfo{ID: hostA.NodeID(), Name: "NodeA", Status: cluster.StatusOnline})
	clusterA.Upsert(&cluster.NodeInfo{ID: hostB.NodeID(), Name: "NodeB", Status: cluster.StatusOnline})
	clusterB.Upsert(&cluster.NodeInfo{ID: hostA.NodeID(), Name: "NodeA", Status: cluster.StatusOnline})
	clusterB.Upsert(&cluster.NodeInfo{ID: hostB.NodeID(), Name: "NodeB", Status: cluster.StatusOnline})

	// Instantiate orchestrators on both nodes
	orchA := NewOrchestrator(hostA, clusterA, logger)
	orchB := NewOrchestrator(hostB, clusterB, logger)

	if err := orchA.Start(ctx); err != nil {
		t.Fatalf("failed to start orchestrator A: %v", err)
	}
	if err := orchB.Start(ctx); err != nil {
		t.Fatalf("failed to start orchestrator B: %v", err)
	}

	// Define Pipeline targeting Node B for transcription and LLM
	p := Pipeline{
		ID: "test-pipeline-run",
		Steps: []PipelineStep{
			{ID: "step-1", Type: StepAudioTranscribe, NodeID: hostB.NodeID(), ModelName: "whisper-base"},
			{ID: "step-2", Type: StepLLMPrompt, NodeID: hostB.NodeID(), ModelName: "llama3:8b", PromptTemplate: "Draw a {{input}}"},
			{ID: "step-3", Type: StepImageGen, NodeID: hostA.NodeID(), ModelName: "comfyui"},
		},
	}

	// Create dummy audio bytes to stream
	dummyAudio := bytes.NewReader(make([]byte, 1000))
	eventCh := make(chan RunEvent, 100)

	var runErr error
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		runErr = orchA.Run(ctx, p, dummyAudio, eventCh)
	}()

	// Collect output events
	var events []RunEvent
	done := make(chan struct{})
	go func() {
		for ev := range eventCh {
			events = append(events, ev)
		}
		close(done)
	}()

	wg.Wait()
	if runErr != nil {
		t.Fatalf("Pipeline run failed: %v", runErr)
	}

	// Wait for pipeline completion events
	time.Sleep(4 * time.Second)
	close(eventCh)
	<-done

	// Verify events are gathered for each step type
	var hasTranscribe, hasLLM, hasImage bool
	for _, ev := range events {
		switch ev.StepType {
		case string(StepAudioTranscribe):
			hasTranscribe = true
		case string(StepLLMPrompt):
			hasLLM = true
		case string(StepImageGen):
			hasImage = true
		}
	}

	if !hasTranscribe {
		t.Error("expected to collect transcription stream events")
	}
	if !hasLLM {
		t.Error("expected to collect LLM prompt stream events")
	}
	if !hasImage {
		t.Error("expected to collect image generation completed events")
	}
}
