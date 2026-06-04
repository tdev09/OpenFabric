package tunnel

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"

	"go.uber.org/zap"
)

const wgConfigTemplate = `[Interface]
PrivateKey = {{.PrivateKey}}
Address = {{.AssignedIP}}
DNS = 1.1.1.1
MTU = 1420

[Peer]
PublicKey = {{.RelayPublicKey}}
Endpoint = {{.RelayURL}}
AllowedIPs = 10.8.0.0/24
PersistentKeepalive = 25
`

// WireGuardManager handles bring-up, tear-down, and peer management of the
// WireGuard interface. Uses kernel WireGuard on Linux, wireguard-go on Mac/Win.
type WireGuardManager struct {
	cfg         *TunnelConfig
	ifaceName   string
	configPath  string
	useKernel   bool
	available   bool // true if wireguard-go or kernel WireGuard is present
	wgGoProc    *exec.Cmd
	relayPubKey string
	log         *zap.Logger
}

// NewWireGuardManager initializes the manager for the current platform.
func NewWireGuardManager(cfg *TunnelConfig, log *zap.Logger) *WireGuardManager {
	iface := "fabric0"
	useKernel := runtime.GOOS == "linux" && kernelWireGuardAvailable()

	// Check if any WireGuard implementation is available on this system.
	wgAvailable := useKernel
	if !wgAvailable {
		if _, err := exec.LookPath("wireguard-go"); err == nil {
			if checkIsRoot() {
				wgAvailable = true
			}
		}
	}

	return &WireGuardManager{
		cfg:        cfg,
		ifaceName:  iface,
		configPath: filepath.Join(os.TempDir(), "fabric-wg.conf"),
		useKernel:  useKernel,
		available:  wgAvailable,
		log:        log,
	}
}

// Available returns true if a WireGuard implementation (kernel or wireguard-go)
// is present on the system. When false, the tunnel operates in relay-only mode.
func (w *WireGuardManager) Available() bool {
	return w.available
}

// Up renders the WireGuard config and brings up the interface.
func (w *WireGuardManager) Up(ctx context.Context, relayPubKey string) error {
	w.relayPubKey = relayPubKey
	conf, err := w.renderConfig()
	if err != nil {
		return fmt.Errorf("render wg config: %w", err)
	}

	// Write config with 0600 permissions
	if err := os.WriteFile(w.configPath, []byte(conf), 0600); err != nil {
		return fmt.Errorf("write wg config: %w", err)
	}

	w.log.Info("bringing up WireGuard interface", zap.String("iface", w.ifaceName), zap.Bool("kernel", w.useKernel))
	if w.useKernel {
		return w.upKernel(ctx)
	}
	return w.upUserspace(ctx)
}

// upKernel uses wg-quick for Linux kernel WireGuard.
func (w *WireGuardManager) upKernel(ctx context.Context) error {
	dest := fmt.Sprintf("/etc/wireguard/%s.conf", w.ifaceName)
	data, err := os.ReadFile(w.configPath)
	if err == nil {
		if errWrite := os.WriteFile(dest, data, 0600); errWrite != nil {
			w.log.Warn("could not write to /etc/wireguard, falling back to userspace", zap.Error(errWrite))
			return w.upUserspace(ctx)
		}
	} else {
		return err
	}

	out, err := exec.CommandContext(ctx, "wg-quick", "up", w.ifaceName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("wg-quick up: %s: %w", out, err)
	}
	return nil
}

// upUserspace launches wireguard-go for Mac and Windows.
func (w *WireGuardManager) upUserspace(ctx context.Context) error {
	wgGoBin, err := extractWireguardGo()
	if err != nil {
		return fmt.Errorf("extract wireguard-go: %w", err)
	}

	cmd := exec.CommandContext(ctx, wgGoBin, w.ifaceName)
	cmd.Env = append(os.Environ(), "WG_TUN_DEBUG=0")
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start wireguard-go: %w", err)
	}
	w.wgGoProc = cmd

	// Apply config via `wg setconf` after the TUN is up
	// Wait a tiny bit for the TUN interface to register in the kernel
	os.WriteFile(w.configPath, []byte(w.configPath), 0600) // stub to write config
	if err := w.applyConfig(); err != nil {
		cmd.Process.Kill()
		return fmt.Errorf("wg setconf: %w", err)
	}

	return w.setInterfaceIP()
}

// applyConfig uses `wg setconf` to load keys and peer config into the interface.
func (w *WireGuardManager) applyConfig() error {
	out, err := exec.Command("wg", "setconf", w.ifaceName, w.configPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w", out, err)
	}
	return nil
}

// setInterfaceIP assigns the tunnel IP to the interface.
func (w *WireGuardManager) setInterfaceIP() error {
	ip := strings.Split(w.cfg.AssignedIP, "/")[0]
	switch runtime.GOOS {
	case "darwin":
		exec.Command("ifconfig", w.ifaceName, ip, ip).Run()
		exec.Command("route", "add", "-net", "10.8.0.0/24", "-interface", w.ifaceName).Run()
	case "linux":
		exec.Command("ip", "addr", "add", w.cfg.AssignedIP, "dev", w.ifaceName).Run()
		exec.Command("ip", "link", "set", "up", "dev", w.ifaceName).Run()
		exec.Command("ip", "route", "add", "10.8.0.0/24", "dev", w.ifaceName).Run()
	case "windows":
		exec.Command("netsh", "interface", "ip", "set", "address",
			w.ifaceName, "static", ip, "255.255.255.0").Run()
	}
	return nil
}

// Down tears down the WireGuard interface.
func (w *WireGuardManager) Down() {
	if w.useKernel {
		exec.Command("wg-quick", "down", w.ifaceName).Run()
	} else {
		if w.wgGoProc != nil && w.wgGoProc.Process != nil {
			w.wgGoProc.Process.Kill()
		}
		switch runtime.GOOS {
		case "darwin":
			exec.Command("ifconfig", w.ifaceName, "destroy").Run()
		case "linux":
			exec.Command("ip", "link", "del", w.ifaceName).Run()
		}
	}
	os.Remove(w.configPath)
}

// AddPeer dynamically adds a new WireGuard peer without restarting the interface.
func (w *WireGuardManager) AddPeer(publicKey, tunnelIP string) error {
	ip := strings.TrimSuffix(tunnelIP, "/32")
	out, err := exec.Command("wg", "set", w.ifaceName,
		"peer", publicKey,
		"allowed-ips", ip+"/32",
		"persistent-keepalive", "25",
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("wg set peer %s: %s: %w", publicKey[:8], out, err)
	}
	return nil
}

// RemovePeer removes a WireGuard peer.
func (w *WireGuardManager) RemovePeer(publicKey string) error {
	out, err := exec.Command("wg", "set", w.ifaceName,
		"peer", publicKey, "remove").CombinedOutput()
	if err != nil {
		return fmt.Errorf("wg remove peer: %s: %w", out, err)
	}
	return nil
}

// WireGuardConfig returns the config file representation.
func (w *WireGuardManager) WireGuardConfig() (string, error) {
	return w.renderConfig()
}

func (w *WireGuardManager) renderConfig() (string, error) {
	tmpl, err := template.New("wg").Parse(wgConfigTemplate)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	err = tmpl.Execute(&buf, struct {
		PrivateKey     string
		AssignedIP     string
		RelayPublicKey string
		RelayURL       string
	}{
		PrivateKey:     w.cfg.PrivateKey,
		AssignedIP:     w.cfg.AssignedIP,
		RelayPublicKey: w.relayPubKey,
		RelayURL:       w.cfg.RelayURL,
	})
	return buf.String(), err
}

func kernelWireGuardAvailable() bool {
	_, err := os.Stat("/sys/module/wireguard")
	return err == nil
}

func extractWireguardGo() (string, error) {
	// For cross-platform runtime, we try to see if 'wireguard-go' is in PATH.
	// If not, we fall back to searching standard paths.
	if path, err := exec.LookPath("wireguard-go"); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("wireguard-go not found in $PATH - install it or use relay-only mode")
}
