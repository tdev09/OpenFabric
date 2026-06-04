package wol

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/openfabric/openfabric/internal/cluster"
	"go.uber.org/zap"
)

// Device represents a registered Wake-on-LAN device.
type Device struct {
	MAC          string    `json:"mac"`           // lowercase, colon-separated, e.g. "00:11:22:33:44:55"
	Name         string    `json:"name"`
	BroadcastIP  string    `json:"broadcast_ip"`  // Optional override (e.g. "192.168.1.255")
	LastIP       string    `json:"last_ip"`       // Last known IP address (used for unicast)
	LinkedNodeID string    `json:"linked_node_id"` // Node ID (peer ID) in the cluster
	LastWoken    time.Time `json:"last_woken"`
	WakeCount    int       `json:"wake_count"`
}

// DiscoveredDevice represents a device found during an ARP scan.
type DiscoveredDevice struct {
	IP           string `json:"ip"`
	MAC          string `json:"mac"`
	Interface    string `json:"interface"`
	LinkedNodeID string `json:"linked_node_id,omitempty"` // If matching an online/offline cluster node IP
}

// Manager orchestrates registered WoL devices, waking them, and scanning the network.
type Manager struct {
	mu         sync.RWMutex
	devices    map[string]*Device // key: MAC
	filePath   string
	clusterMgr *cluster.Manager
	log        *zap.Logger
	dataDir    string
}

// NewManager instantiates a WoL Manager.
func NewManager(dataDir string, clusterMgr *cluster.Manager, log *zap.Logger) (*Manager, error) {
	l := log.Named("wol")
	filePath := filepath.Join(dataDir, "wol_devices.json")

	m := &Manager{
		devices:    make(map[string]*Device),
		filePath:   filePath,
		clusterMgr: clusterMgr,
		log:        l,
		dataDir:    dataDir,
	}

	if err := m.load(); err != nil {
		l.Error("failed to load WoL devices, starting clean", zap.Error(err))
	}

	return m, nil
}

func (m *Manager) load() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	f, err := os.Open(m.filePath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	var list []*Device
	if err := json.NewDecoder(f).Decode(&list); err != nil {
		return err
	}

	for _, d := range list {
		d.MAC = normalizeMAC(d.MAC)
		m.devices[d.MAC] = d
	}
	return nil
}

func (m *Manager) save() error {
	list := make([]*Device, 0, len(m.devices))
	for _, d := range m.devices {
		list = append(list, d)
	}

	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}

	tmpFile := m.filePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0600); err != nil {
		return err
	}

	return os.Rename(tmpFile, m.filePath)
}

func normalizeMAC(mac string) string {
	mac = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(mac, "-", ":"), " ", ""))
	if len(mac) == 12 && !strings.Contains(mac, ":") {
		var parts []string
		for i := 0; i < 12; i += 2 {
			parts = append(parts, mac[i:i+2])
		}
		mac = strings.Join(parts, ":")
	}

	parts := strings.Split(mac, ":")
	if len(parts) == 6 {
		for i, part := range parts {
			if len(part) == 1 {
				parts[i] = "0" + part
			}
		}
		mac = strings.Join(parts, ":")
	}
	return mac
}

func validateAndNormalizeMAC(mac string) (string, error) {
	normalized := normalizeMAC(mac)
	_, err := net.ParseMAC(normalized)
	if err != nil {
		return "", fmt.Errorf("invalid MAC address %q: %w", mac, err)
	}
	return normalized, nil
}

// Register adds or updates a WoL device in the registry.
func (m *Manager) Register(d *Device) error {
	mac, err := validateAndNormalizeMAC(d.MAC)
	if err != nil {
		return err
	}
	d.MAC = mac

	m.mu.Lock()
	defer m.mu.Unlock()

	if existing, exists := m.devices[mac]; exists {
		existing.Name = d.Name
		existing.BroadcastIP = d.BroadcastIP
		existing.LinkedNodeID = d.LinkedNodeID
		if d.LastIP != "" {
			existing.LastIP = d.LastIP
		}
	} else {
		m.devices[mac] = d
	}

	return m.save()
}

// Unregister removes a WoL device from the registry.
func (m *Manager) Unregister(mac string) error {
	mac, err := validateAndNormalizeMAC(mac)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.devices[mac]; !exists {
		return fmt.Errorf("device not registered")
	}

	delete(m.devices, mac)
	return m.save()
}

// List returns all registered WoL devices.
func (m *Manager) List() []*Device {
	m.mu.RLock()
	defer m.mu.RUnlock()

	list := make([]*Device, 0, len(m.devices))
	for _, d := range m.devices {
		copy := *d
		list = append(list, &copy)
	}

	sort.Slice(list, func(i, j int) bool {
		return list[i].Name < list[j].Name
	})

	return list
}

// Wake sends magic packets to trigger a device boot.
func (m *Manager) Wake(mac string) error {
	mac, err := validateAndNormalizeMAC(mac)
	if err != nil {
		return err
	}

	m.mu.Lock()
	d, exists := m.devices[mac]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("device with MAC %s is not registered", mac)
	}
	d.LastWoken = time.Now()
	d.WakeCount++
	_ = m.save()
	m.mu.Unlock()

	m.log.Info("waking device", zap.String("name", d.Name), zap.String("mac", mac))
	return SendMagicPacket(mac, d.BroadcastIP, d.LastIP, m.log)
}

// SendMagicPacket constructs the 102-byte frame and transmits it via multiple vectors.
func SendMagicPacket(macStr, overrideBroadcastIP, lastIP string, log *zap.Logger) error {
	mac, err := net.ParseMAC(macStr)
	if err != nil {
		return fmt.Errorf("parse mac: %w", err)
	}

	if len(mac) != 6 {
		return fmt.Errorf("mac must be 6 bytes, got %d", len(mac))
	}

	packet := make([]byte, 102)
	for i := 0; i < 6; i++ {
		packet[i] = 0xFF
	}
	for i := 1; i <= 16; i++ {
		copy(packet[i*6:(i+1)*6], mac)
	}

	var targets []string

	if overrideBroadcastIP != "" {
		targets = append(targets, overrideBroadcastIP)
	}
	targets = append(targets, "255.255.255.255")

	subnets, err := getSubnetBroadcasts()
	if err == nil {
		for _, bcast := range subnets {
			targets = append(targets, bcast)
		}
	} else {
		log.Warn("failed to detect local subnets", zap.Error(err))
	}

	if lastIP != "" {
		targets = append(targets, lastIP)
	}

	uniqueTargets := make(map[string]bool)
	for _, ip := range targets {
		if ip != "" {
			uniqueTargets[ip] = true
		}
	}

	var sendErrors []string
	for ip := range uniqueTargets {
		for _, port := range []int{7, 9} {
			addr := fmt.Sprintf("%s:%d", ip, port)
			if err := writeUDPPacket(packet, addr); err != nil {
				log.Debug("failed to send magic packet to target", zap.String("addr", addr), zap.Error(err))
				sendErrors = append(sendErrors, fmt.Sprintf("%s: %v", addr, err))
			} else {
				log.Info("sent magic packet successfully", zap.String("addr", addr))
			}
		}
	}

	if len(sendErrors) == len(uniqueTargets)*2 {
		return fmt.Errorf("failed to send magic packet to all targets: %s", strings.Join(sendErrors, "; "))
	}

	return nil
}

func writeUDPPacket(payload []byte, addrStr string) error {
	raddr, err := net.ResolveUDPAddr("udp", addrStr)
	if err != nil {
		return err
	}

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return err
	}
	defer conn.Close()

	_, err = conn.WriteTo(payload, raddr)
	return err
}

func getSubnetBroadcasts() ([]string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	var broadcasts []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}

			ip := ipnet.IP.To4()
			if ip == nil {
				continue
			}

			mask := ipnet.Mask
			if len(mask) != 4 {
				continue
			}

			broadcast := make(net.IP, 4)
			for i := 0; i < 4; i++ {
				broadcast[i] = ip[i] | ^mask[i]
			}
			broadcasts = append(broadcasts, broadcast.String())
		}
	}

	return broadcasts, nil
}

// Scan sweeps the local subnets to populate the OS ARP table, then parses it.
func (m *Manager) Scan(ctx context.Context) ([]*DiscoveredDevice, error) {
	m.log.Info("starting active network scan and ARP sweep...")

	subnets, err := getActiveSubnets()
	if err != nil {
		m.log.Error("failed to get active subnets for scan", zap.Error(err))
	}

	if len(subnets) > 0 {
		m.sweepSubnets(ctx, subnets)
	}

	discovered, err := parseARPTable(m.log)
	if err != nil {
		return nil, fmt.Errorf("parse arp table: %w", err)
	}

	if m.clusterMgr != nil {
		nodes := m.clusterMgr.List()
		nodeMap := make(map[string]string) // IP -> NodeID
		for _, n := range nodes {
			for _, addr := range n.Addresses {
				if strings.HasPrefix(addr, "/ip4/") {
					parts := strings.Split(addr, "/")
					if len(parts) >= 3 {
						nodeMap[parts[2]] = n.ID
					}
				}
			}
		}

		for _, d := range discovered {
			if nodeID, exists := nodeMap[d.IP]; exists {
				d.LinkedNodeID = nodeID
				m.mu.Lock()
				if reg, ok := m.devices[d.MAC]; ok {
					reg.LastIP = d.IP
					_ = m.save()
				}
				m.mu.Unlock()
			}
		}
	}

	m.log.Info("completed local network scan", zap.Int("discovered_count", len(discovered)))
	return discovered, nil
}

func getActiveSubnets() ([]*net.IPNet, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	var subnets []*net.IPNet
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}

			ip := ipnet.IP.To4()
			if ip == nil {
				continue
			}
			subnets = append(subnets, ipnet)
		}
	}
	return subnets, nil
}

func (m *Manager) sweepSubnets(ctx context.Context, subnets []*net.IPNet) {
	var wg sync.WaitGroup
	ipChan := make(chan string, 1024)

	numWorkers := 50
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
			if err != nil {
				return
			}
			defer conn.Close()

			payload := []byte{}
			for ip := range ipChan {
				select {
				case <-ctx.Done():
					return
				default:
					raddr, err := net.ResolveUDPAddr("udp", ip+":9")
					if err == nil {
						_, _ = conn.WriteTo(payload, raddr)
					}
				}
			}
		}()
	}

	for _, sub := range subnets {
		ones, bits := sub.Mask.Size()
		if bits-ones > 10 { // larger than /22
			m.log.Debug("skipping sweep of large subnet", zap.String("subnet", sub.String()))
			continue
		}

		ip := sub.IP.To4()
		if ip == nil {
			continue
		}

		m.log.Debug("sweeping subnet", zap.String("subnet", sub.String()))
		iterateIPs(sub, func(targetIP string) {
			select {
			case <-ctx.Done():
			case ipChan <- targetIP:
			}
		})
	}

	close(ipChan)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		m.log.Debug("subnet sweep finished")
	case <-time.After(2 * time.Second):
		m.log.Warn("subnet sweep timed out")
	case <-ctx.Done():
		m.log.Debug("subnet sweep cancelled")
	}
}

func iterateIPs(ipnet *net.IPNet, callback func(string)) {
	ip := ipnet.IP.To4()
	if ip == nil {
		return
	}

	mask := ipnet.Mask
	network := make(net.IP, 4)
	broadcast := make(net.IP, 4)
	for i := 0; i < 4; i++ {
		network[i] = ip[i] & mask[i]
		broadcast[i] = ip[i] | ^mask[i]
	}

	current := make(net.IP, 4)
	copy(current, network)

	for {
		for i := 3; i >= 0; i-- {
			current[i]++
			if current[i] > 0 {
				break
			}
		}

		if current.Equal(broadcast) {
			break
		}

		callback(current.String())
	}
}

func parseARPTable(log *zap.Logger) ([]*DiscoveredDevice, error) {
	if runtime.GOOS == "linux" {
		return parseLinuxARP(log)
	} else if runtime.GOOS == "darwin" {
		return parseMacOSARP(log)
	} else if runtime.GOOS == "windows" {
		return parseWindowsARP(log)
	}
	return nil, fmt.Errorf("unsupported OS: %s", runtime.GOOS)
}

func parseLinuxARP(log *zap.Logger) ([]*DiscoveredDevice, error) {
	data, err := os.ReadFile("/proc/net/arp")
	if err != nil {
		return nil, err
	}

	var devices []*DiscoveredDevice
	lines := strings.Split(string(data), "\n")
	if len(lines) <= 1 {
		return nil, nil
	}

	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}

		ip := fields[0]
		mac := fields[3]
		iface := fields[5]

		if mac == "00:00:00:00:00:00" || mac == "<incomplete>" {
			continue
		}

		normMAC := normalizeMAC(mac)
		devices = append(devices, &DiscoveredDevice{
			IP:        ip,
			MAC:       normMAC,
			Interface: iface,
		})
	}
	return devices, nil
}

func parseMacOSARP(log *zap.Logger) ([]*DiscoveredDevice, error) {
	cmd := exec.Command("arp", "-a")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var devices []*DiscoveredDevice
	lines := strings.Split(string(output), "\n")
	re := regexp.MustCompile(`\(([^)]+)\)\s+at\s+([0-9a-fA-F:-]+)\s+.*on\s+([a-zA-Z0-9]+)`)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		matches := re.FindStringSubmatch(line)
		if len(matches) < 4 {
			continue
		}

		ip := matches[1]
		mac := matches[2]
		iface := matches[3]

		if mac == "incomplete" || strings.HasPrefix(mac, "ff:ff:ff:ff:ff:ff") {
			continue
		}

		normMAC := normalizeMAC(mac)
		devices = append(devices, &DiscoveredDevice{
			IP:        ip,
			MAC:       normMAC,
			Interface: iface,
		})
	}
	return devices, nil
}

func parseWindowsARP(log *zap.Logger) ([]*DiscoveredDevice, error) {
	cmd := exec.Command("arp", "-a")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var devices []*DiscoveredDevice
	lines := strings.Split(string(output), "\n")
	re := regexp.MustCompile(`^\s*([0-9.]+)\s+([0-9a-fA-F:-]+)\s+([a-zA-Z]+)`)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		matches := re.FindStringSubmatch(line)
		if len(matches) < 4 {
			continue
		}

		ip := matches[1]
		mac := matches[2]
		macType := strings.ToLower(matches[3])

		if macType == "static" || strings.HasPrefix(mac, "ff-ff") || strings.HasPrefix(mac, "01-00-5e") {
			continue
		}

		normMAC := normalizeMAC(mac)
		devices = append(devices, &DiscoveredDevice{
			IP:        ip,
			MAC:       normMAC,
			Interface: "windows",
		})
	}
	return devices, nil
}
