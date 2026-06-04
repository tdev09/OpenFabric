package wol

import (
	"net"
	"os"
	"testing"
	"time"

	"github.com/openfabric/openfabric/internal/cluster"
	"go.uber.org/zap"
)

func TestNormalizeMAC(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"00:11:22:33:44:55", "00:11:22:33:44:55"},
		{"00-11-22-33-44-55", "00:11:22:33:44:55"},
		{"001122334455", "00:11:22:33:44:55"},
		{"0:11:22:33:44:55", "00:11:22:33:44:55"},
		{"0:1:2:3:4:5", "00:01:02:03:04:05"},
		{"00 11 22 33 44 55", "00:11:22:33:44:55"},
		{"00:11:22:33:44:55  ", "00:11:22:33:44:55"},
	}

	for _, tc := range tests {
		got := normalizeMAC(tc.input)
		if got != tc.expected {
			t.Errorf("normalizeMAC(%q) = %q; want %q", tc.input, got, tc.expected)
		}
	}
}

func TestDeviceRegistry(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "wol-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	log := zap.NewNop()
	clusterMgr := cluster.NewManager(nil)

	mgr, err := NewManager(tempDir, clusterMgr, log)
	if err != nil {
		t.Fatalf("failed to create Manager: %v", err)
	}

	// 1. Test registration
	dev := &Device{
		MAC:          "00:11:22:33:44:55",
		Name:         "Test Node",
		LinkedNodeID: "node-1",
	}

	if err := mgr.Register(dev); err != nil {
		t.Fatalf("failed to register device: %v", err)
	}

	// 2. Test listing
	list := mgr.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 device, got %d", len(list))
	}
	if list[0].MAC != "00:11:22:33:44:55" || list[0].Name != "Test Node" {
		t.Errorf("list returned incorrect device: %+v", list[0])
	}

	// 3. Test persistence reload
	mgr2, err := NewManager(tempDir, clusterMgr, log)
	if err != nil {
		t.Fatalf("failed to reload Manager: %v", err)
	}

	list2 := mgr2.List()
	if len(list2) != 1 {
		t.Fatalf("expected reloaded manager to have 1 device, got %d", len(list2))
	}

	// 4. Test unregister
	if err := mgr.Unregister("00:11:22:33:44:55"); err != nil {
		t.Fatalf("failed to unregister: %v", err)
	}

	if len(mgr.List()) != 0 {
		t.Errorf("expected 0 devices after unregister, got %d", len(mgr.List()))
	}
}

func TestAutoWakerTrigger(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "wol-test-autowake-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	log := zap.NewNop()
	clusterMgr := cluster.NewManager(nil)

	// Set up our local node as the coordinator
	localNodeID := "coordinator-node"
	clusterMgr.TrustPeer(localNodeID)
	clusterMgr.Upsert(&cluster.NodeInfo{
		ID:         localNodeID,
		Name:       "coordinator",
		Status:     cluster.StatusOnline,
		RAMTotal:   1000,
		RAMUsed:    950, // 95% used, 5% free -> memory pressure
		LastSeen:   time.Now(),
		Addresses:  []string{"/ip4/127.0.0.1/tcp/4893/p2p/coordinator-node"},
	})

	// Add an offline node
	offlineNodeID := "offline-node"
	clusterMgr.TrustPeer(offlineNodeID)
	clusterMgr.Upsert(&cluster.NodeInfo{
		ID:        offlineNodeID,
		Name:      "sleeping-worker",
		Status:    cluster.StatusOffline,
		LastSeen:  time.Now().Add(-10 * time.Minute),
		Addresses: []string{"/ip4/192.168.1.100/tcp/4893/p2p/offline-node"},
	})

	mgr, err := NewManager(tempDir, clusterMgr, log)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	// Register the offline device linked to the offline node
	dev := &Device{
		MAC:          "00:aa:bb:cc:dd:ee",
		Name:         "Worker Card",
		LinkedNodeID: offlineNodeID,
		LastIP:       "192.168.1.100",
	}
	if err := mgr.Register(dev); err != nil {
		t.Fatalf("failed to register device: %v", err)
	}

	// Run memory pressure check (threshold 20% free RAM, i.e. 0.20)
	mgr.checkMemoryPressure(localNodeID, 0.20)

	// Check if device was woken (LastWoken set and WakeCount incremented)
	mgr.mu.RLock()
	d := mgr.devices["00:aa:bb:cc:dd:ee"]
	mgr.mu.RUnlock()

	if d.WakeCount != 1 {
		t.Errorf("expected WakeCount to be 1, got %d", d.WakeCount)
	}
	if d.LastWoken.IsZero() {
		t.Error("expected LastWoken timestamp to be set")
	}

	// Run check again immediately; should not double wake due to cooldown
	mgr.checkMemoryPressure(localNodeID, 0.20)

	mgr.mu.RLock()
	d2 := mgr.devices["00:aa:bb:cc:dd:ee"]
	mgr.mu.RUnlock()

	if d2.WakeCount != 1 {
		t.Errorf("expected WakeCount to remain 1 due to cooldown, got %d", d2.WakeCount)
	}
}

func TestMagicPacketBuilder(t *testing.T) {
	macStr := "00:11:22:33:44:55"
	mac, err := net.ParseMAC(macStr)
	if err != nil {
		t.Fatalf("parse mac: %v", err)
	}

	packet := make([]byte, 102)
	for i := 0; i < 6; i++ {
		packet[i] = 0xFF
	}
	for i := 1; i <= 16; i++ {
		copy(packet[i*6:(i+1)*6], mac)
	}

	// Verify size
	if len(packet) != 102 {
		t.Errorf("packet length = %d; want 102", len(packet))
	}

	// Verify sync stream
	for i := 0; i < 6; i++ {
		if packet[i] != 0xFF {
			t.Errorf("packet[%d] = %x; want 0xFF", i, packet[i])
		}
	}

	// Verify 16 repetitions of MAC
	for rep := 0; rep < 16; rep++ {
		offset := 6 + rep*6
		for i := 0; i < 6; i++ {
			if packet[offset+i] != mac[i] {
				t.Errorf("packet[%d] = %x; want %x", offset+i, packet[offset+i], mac[i])
			}
		}
	}
}
