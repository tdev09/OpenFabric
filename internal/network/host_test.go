package network

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	libp2pnetwork "github.com/libp2p/go-libp2p/core/network"
	libp2ppeer "github.com/libp2p/go-libp2p/core/peer"
	libp2pprotocol "github.com/libp2p/go-libp2p/core/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestHost_LocalLoopbackStream(t *testing.T) {
	log := zap.NewNop()
	tmpDir, err := os.MkdirTemp("", "openfabric_host_loopback_*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create host on a random port
	h, err := NewHost(tmpDir, 0, log)
	require.NoError(t, err)
	defer h.Close()

	testProto := libp2pprotocol.ID("/test/loopback-protocol/1.0.0")

	// Set up local handler
	handlerCalled := make(chan struct{})
	h.SetStreamHandler(testProto, func(s libp2pnetwork.Stream) {
		defer s.Close()
		close(handlerCalled)

		// Read request
		buf := make([]byte, 100)
		n, err := s.Read(buf)
		if err != nil && err != io.EOF {
			t.Errorf("handler read error: %v", err)
			return
		}

		reqStr := string(buf[:n])
		if reqStr != "hello loopback" {
			t.Errorf("unexpected request: %q", reqStr)
			return
		}

		// Write response
		_, err = s.Write([]byte("hello client"))
		if err != nil {
			t.Errorf("handler write error: %v", err)
		}
	})

	// Dial ourselves
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	selfPeerID, err := libp2ppeer.Decode(h.NodeID())
	require.NoError(t, err)

	stream, err := h.NewStream(ctx, selfPeerID, testProto)
	require.NoError(t, err)
	defer stream.Close()

	// Write request
	_, err = stream.Write([]byte("hello loopback"))
	require.NoError(t, err)
	err = stream.CloseWrite()
	require.NoError(t, err)

	// Read response
	respBuf, err := io.ReadAll(stream)
	require.NoError(t, err)
	assert.Equal(t, "hello client", string(respBuf))

	// Ensure the handler was executed
	select {
	case <-handlerCalled:
	case <-time.After(time.Second):
		t.Fatal("handler was not called")
	}
}
