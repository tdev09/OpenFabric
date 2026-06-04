package social

import (
	"context"
	"testing"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSocialHandshake(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	addr, err := multiaddr.NewMultiaddr("/ip4/127.0.0.1/tcp/0")
	require.NoError(t, err)

	// 1. Initialize Lender Host (Host A)
	hA, err := libp2p.New(libp2p.ListenAddrs(addr))
	require.NoError(t, err)
	defer hA.Close()

	// 2. Initialize Borrower Host (Host B)
	hB, err := libp2p.New(libp2p.ListenAddrs(addr))
	require.NoError(t, err)
	defer hB.Close()

	// Connect hosts in peerstore
	hB.Peerstore().AddAddrs(hA.ID(), hA.Addrs(), time.Hour)
	err = hB.Connect(ctx, hB.Peerstore().PeerInfo(hA.ID()))
	require.NoError(t, err)

	// 3. Create Registry and Handshake Server on Lender
	reg := NewRegistry()
	server := NewHandshakeServer(hA, reg)
	hA.SetStreamHandler(HandshakeProtocolID, server.HandleStream)

	// 4. Generate LendToken
	tokenCode, err := reg.GenerateToken(hA.ID().String(), []string{addr.String()}, 4*1024*1024*1024, time.Hour, []string{"wasm"})
	require.NoError(t, err)
	assert.NotEmpty(t, tokenCode)

	// 5. Connect as Borrower from Host B
	err = ConnectAsBorrower(ctx, hB, hA.ID().String(), tokenCode)
	assert.NoError(t, err)

	// Verify session registered on Lender
	server.mu.Lock()
	sess, ok := server.sessions[hB.ID().String()]
	server.mu.Unlock()

	assert.True(t, ok)
	assert.Equal(t, hB.ID().String(), sess.BorrowerID)
	assert.Equal(t, int64(4*1024*1024*1024), sess.MaxVRAM)

	// 6. Test with expired / invalid token
	invalidToken := "ofl_eyJwZWVyX2lkIjoiTmV3UGVlciIsImFkZHJzIjpbXSwiZXhwaXJlc19hdCI6IjIwMjAtMDEtMDFUMDA6MDA6MDBaIiwibWF4X3ZyYW1fYnl0ZXMiOjB9"
	err = ConnectAsBorrower(ctx, hB, hA.ID().String(), invalidToken)
	assert.Error(t, err)
}
