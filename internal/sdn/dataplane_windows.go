//go:build windows

package sdn

import (
	"fmt"
	"os/exec"
)

// WindowsDataPlane implements the data plane on Windows using netsh firewall/qos commands.
type WindowsDataPlane struct {
	iface string
}

// NewWindowsDataPlane creates a WindowsDataPlane instance.
func NewWindowsDataPlane(iface string) (*WindowsDataPlane, error) {
	return &WindowsDataPlane{iface: iface}, nil
}

// Apply installs firewall and traffic shaping rules on Windows.
func (d *WindowsDataPlane) Apply(rs *RuleSet) error {
	d.removeAllFilters()

	for _, rule := range rs.Rules {
		if err := d.addFilter(rule); err != nil {
			return err
		}
	}

	d.applyQoS(rs.QoSBands)
	return nil
}

// addFilter adds a Windows firewall rule via netsh.
func (d *WindowsDataPlane) addFilter(rule *KernelRule) error {
	if len(rule.Match.DstPorts) > 0 {
		for _, port := range rule.Match.DstPorts {
			action := "allow"
			if rule.Action == ActionDeny {
				action = "block"
			}
			proto := "tcp"
			if rule.Match.Protocol == "udp" {
				proto = "udp"
			}
			name := fmt.Sprintf("OpenFabric-%s", rule.ID)
			_ = exec.Command("netsh", "advfirewall", "firewall",
				"add", "rule",
				"name="+name,
				"protocol="+proto,
				fmt.Sprintf("localport=%d", port),
				"action="+action,
				"dir=in",
			).Run()
		}
	}
	return nil
}

// applyQoS configures bandwidth limits using netsh qos.
func (d *WindowsDataPlane) applyQoS(bands []*QoSBand) {
	for _, band := range bands {
		if band.MaxBps == 0 {
			continue
		}
		for _, port := range band.Match.DstPorts {
			_ = exec.Command("netsh", "qos", "add", "policy",
				fmt.Sprintf("OpenFabricQoS-%s", band.ID),
				"DSCPValue="+dscp(band.Priority),
				fmt.Sprintf("ThrottleRateKbps=%d", band.MaxBps/1000),
				fmt.Sprintf("DestinationPort=%d", port),
			).Run()
		}
	}
}

// removeAllFilters flushes all OpenFabric firewall rules via netsh.
func (d *WindowsDataPlane) removeAllFilters() {
	_ = exec.Command("netsh", "advfirewall", "firewall", "delete", "rule", "name=all", "description=OpenFabric").Run()
}

// Flush clears all SDN settings.
func (d *WindowsDataPlane) Flush() error {
	d.removeAllFilters()
	return nil
}

// Status returns rule details.
func (d *WindowsDataPlane) Status() (string, error) {
	out, err := exec.Command("netsh", "advfirewall", "firewall", "show", "rule", "name=all").CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// DataPlaneInterface returns interface name.
func (d *WindowsDataPlane) DataPlaneInterface() string {
	return d.iface
}

func dscp(priority int) string {
	switch priority {
	case 1:
		return "46"
	case 2:
		return "34"
	case 3:
		return "0"
	case 4:
		return "8"
	default:
		return "0"
	}
}
