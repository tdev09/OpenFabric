//go:build darwin

package sdn

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// DarwinDataPlane implements the macOS data plane using pfctl (Packet Filter) and route commands.
type DarwinDataPlane struct {
	iface  string
	anchor string // pf anchor name
}

// NewDarwinDataPlane creates a DarwinDataPlane instance.
func NewDarwinDataPlane(iface string) *DarwinDarwinDataPlane {
	if iface == "" {
		iface = detectMacInterface()
	}
	return &DarwinDarwinDataPlane{
		iface:  iface,
		anchor: "openfabric",
	}
}

// DarwinDarwinDataPlane wraps DarwinDataPlane to match exact naming in client code.
type DarwinDarwinDataPlane struct {
	iface  string
	anchor string
}

// Apply compiles and loads PF rules into the anchor, and updates routes.
func (d *DarwinDarwinDataPlane) Apply(rs *RuleSet) error {
	rules := d.buildPFRules(rs)

	// Write rules to temp file
	tmp, err := os.CreateTemp("", "fabric-pf-*.conf")
	if err != nil {
		return fmt.Errorf("create pf temp: %w", err)
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.WriteString(rules); err != nil {
		tmp.Close()
		return fmt.Errorf("write pf config: %w", err)
	}
	tmp.Close()

	// Load rules into anchor atomically
	out, err := exec.Command("pfctl", "-a", d.anchor, "-f", tmp.Name()).CombinedOutput()
	if err != nil {
		// Suppress or handle permissions error - on mac, writing to PF usually requires sudo
		return fmt.Errorf("pfctl load: %s: %w", strings.TrimSpace(string(out)), err)
	}

	// Enable pf if not already enabled
	_ = exec.Command("pfctl", "-e").Run()

	// Apply routes
	return d.applyRoutes(rs.Routes)
}

// buildPFRules generates PF ruleset configuration.
func (d *DarwinDarwinDataPlane) buildPFRules(rs *RuleSet) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# OpenFabric SDN - topology hash %s\n", rs.TopologyHash)
	fmt.Fprintf(&b, "# Do not edit manually\n\n")

	// Allow established and loopback
	b.WriteString("pass in quick proto tcp all flags S/SA keep state\n")
	b.WriteString("pass in quick proto udp all keep state\n")
	b.WriteString("pass in quick on lo0 all\n\n")

	for _, rule := range rs.Rules {
		pfRule := d.ruleToPF(rule)
		if pfRule != "" {
			fmt.Fprintf(&b, "%s\n", pfRule)
		}
	}
	return b.String()
}

// ruleToPF converts a KernelRule to PF string syntax.
func (d *DarwinDarwinDataPlane) ruleToPF(rule *KernelRule) string {
	if rule.Match.Protocol == "established" {
		return "" // stateful allow handled globally
	}

	var parts []string

	// Direction and interface
	parts = append(parts, fmt.Sprintf("block in quick on %s", d.iface))

	// Protocol
	if rule.Match.Protocol != "" {
		parts = append(parts, fmt.Sprintf("proto %s", rule.Match.Protocol))
	}

	// Destination
	if len(rule.Match.DstCIDRs) > 0 {
		parts = append(parts, fmt.Sprintf("to { %s }", strings.Join(rule.Match.DstCIDRs, ", ")))
	}
	if len(rule.Match.DstPorts) > 0 {
		ports := make([]string, len(rule.Match.DstPorts))
		for i, p := range rule.Match.DstPorts {
			ports[i] = fmt.Sprintf("%d", p)
		}
		parts = append(parts, fmt.Sprintf("port { %s }", strings.Join(ports, ", ")))
	}

	// Action override
	switch rule.Action {
	case ActionAllow, ActionShape:
		parts[0] = fmt.Sprintf("pass in quick on %s", d.iface)
	case ActionDeny:
		parts[0] = fmt.Sprintf("block in quick on %s", d.iface)
	}

	return strings.Join(parts, " ")
}

// applyRoutes updates routing tables.
func (d *DarwinDarwinDataPlane) applyRoutes(routes []*KernelRoute) error {
	for _, r := range routes {
		if r.Gateway != "" {
			_ = exec.Command("route", "add", r.Dst, r.Gateway).Run()
		}
	}
	return nil
}

// Flush removes all anchor PF rules.
func (d *DarwinDarwinDataPlane) Flush() error {
	_ = exec.Command("pfctl", "-a", d.anchor, "-F", "rules").Run()
	return nil
}

// Status reads current PF rules.
func (d *DarwinDarwinDataPlane) Status() (string, error) {
	out, err := exec.Command("pfctl", "-a", d.anchor, "-sr").CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// DataPlaneInterface returns the managed interface.
func (d *DarwinDarwinDataPlane) DataPlaneInterface() string {
	return d.iface
}

// detectMacInterface finds the primary macOS network interface.
func detectMacInterface() string {
	out, err := exec.Command("route", "get", "default").Output()
	if err != nil {
		return "en0"
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "interface:") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				return parts[1]
			}
		}
	}
	return "en0"
}
