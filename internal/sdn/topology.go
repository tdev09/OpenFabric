// Package sdn implements OpenFabric's Software Defined Networking layer.
// It translates human-readable network intent (YAML) into kernel-level
// rules applied across every node in the cluster simultaneously.
package sdn

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// TopologyVersion is the supported YAML schema version.
const TopologyVersion = "1"

// Topology is the parsed and validated network topology.
// It is the single source of truth for the entire network.
type Topology struct {
	Version  string     `yaml:"version" json:"version"`
	Name     string     `yaml:"name" json:"name"`
	Segments []*Segment `yaml:"segments" json:"segments"`
	Policies []*Policy  `yaml:"policies" json:"policies"`
	Routes   []*Route   `yaml:"routes" json:"routes"`

	// Computed at parse time
	hash      string // SHA-256 of the raw YAML - used for change detection
	parsed    time.Time
	segByName map[string]*Segment
}

// Segment is a logical group of nodes with shared network policies.
// Equivalent to a VLAN but defined by intent, not by hardware configuration.
type Segment struct {
	Name           string   `yaml:"name" json:"name"`
	Description    string   `yaml:"description" json:"description"`
	Color          string   `yaml:"color" json:"color"`
	Nodes          []string `yaml:"nodes" json:"nodes"`
	Internet       string   `yaml:"internet" json:"internet"`               // "allow" | "deny"
	InterSegment   string   `yaml:"inter_segment" json:"inter_segment"`     // "allow" | "deny"
	ClusterAccess  string   `yaml:"cluster_access" json:"cluster_access"`   // "allow" | "deny" | ""
	BandwidthLimit string   `yaml:"bandwidth_limit" json:"bandwidth_limit"` // e.g. "10Mbps"

	// Computed
	cidr    *net.IPNet
	bwBytes int64 // parsed BandwidthLimit in bytes/sec
}

// Policy is a network rule: match traffic + apply action + optional QoS.
type Policy struct {
	Name        string      `yaml:"name" json:"name"`
	Description string      `yaml:"description" json:"description"`
	Match       PolicyMatch `yaml:"match" json:"match"`
	Action      string      `yaml:"action" json:"action"` // "allow" | "deny" | "redirect"
	QoS         *QoSSpec    `yaml:"qos" json:"qos"`
	ApplyTo     []string    `yaml:"apply_to" json:"apply_to"` // segment names, empty = all

	// Computed priority: more specific rules get higher priority
	priority int
}

// PolicyMatch defines what traffic a policy applies to.
type PolicyMatch struct {
	SrcSegment []string `yaml:"src_segment" json:"src_segment"`
	DstSegment []string `yaml:"dst_segment" json:"dst_segment"`
	SrcHost    []string `yaml:"src_host" json:"src_host"`
	DstHost    []string `yaml:"dst_host" json:"dst_host"`
	SrcPort    []int    `yaml:"src_port" json:"src_port"`
	DstPort    []int    `yaml:"dst_port" json:"dst_port"`
	Protocol   string   `yaml:"protocol" json:"protocol"` // "tcp" | "udp" | "icmp" | ""
	DstCIDR    []string `yaml:"dst_cidr" json:"dst_cidr"`
}

// QoSSpec defines traffic shaping parameters.
type QoSSpec struct {
	Priority           string `yaml:"priority" json:"priority"`                       // "critical" | "high" | "normal" | "low"
	MaxBandwidth       string `yaml:"max_bandwidth" json:"max_bandwidth"`             // e.g. "10Mbps"
	BandwidthGuarantee string `yaml:"bandwidth_guarantee" json:"bandwidth_guarantee"` // e.g. "50%"
	Burst              string `yaml:"burst" json:"burst"`                             // e.g. "15Mbps"

	// Computed
	maxBps       int64
	guaranteeBps int64
	burstBps     int64
	tcPriority   int // Linux tc priority class (1=critical, 4=low)
}

// Route is a static routing entry.
type Route struct {
	Dst    string `yaml:"dst" json:"dst"`
	Via    string `yaml:"via" json:"via"` // IP, "auto", or "tunnel"
	Metric int    `yaml:"metric" json:"metric"`
}

// LoadTopology reads and validates a topology YAML file.
func LoadTopology(path string) (*Topology, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read topology file: %w", err)
	}
	return ParseTopology(data)
}

// ParseTopology parses and validates a topology YAML byte slice.
func ParseTopology(data []byte) (*Topology, error) {
	var t Topology
	if err := yaml.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("parse topology YAML: %w", err)
	}

	if err := t.validate(); err != nil {
		return nil, err
	}

	// Compute content hash for change detection
	h := sha256.Sum256(data)
	t.hash = hex.EncodeToString(h[:])
	t.parsed = time.Now()

	// Build index for O(1) segment lookup
	t.segByName = make(map[string]*Segment, len(t.Segments))
	for _, seg := range t.Segments {
		t.segByName[seg.Name] = seg
		// Parse bandwidth limit
		if seg.BandwidthLimit != "" {
			var err error
			seg.bwBytes, err = parseBandwidth(seg.BandwidthLimit)
			if err != nil {
				return nil, fmt.Errorf("segment %q: invalid bandwidth_limit: %w",
					seg.Name, err)
			}
		}
	}

	// Compute policy priorities and parse QoS specs
	for _, p := range t.Policies {
		p.priority = computePriority(&p.Match)
		if p.QoS != nil {
			if err := parseQoS(p.QoS); err != nil {
				return nil, fmt.Errorf("policy %q: %w", p.Name, err)
			}
		}
	}

	return &t, nil
}

// Hash returns the SHA-256 hash of the topology YAML content.
// Used to detect changes between deployments.
func (t *Topology) Hash() string { return t.hash }

// SegmentForNode returns the segment a node belongs to, or nil.
func (t *Topology) SegmentForNode(nodeID string) *Segment {
	for _, seg := range t.Segments {
		for _, n := range seg.Nodes {
			if n == nodeID {
				return seg
			}
		}
	}
	return nil
}

// validate checks the topology for logical errors and security issues.
func (t *Topology) validate() error {
	if t.Version != TopologyVersion {
		return fmt.Errorf(
			"unsupported topology version %q - this OpenFabric supports version %q",
			t.Version, TopologyVersion,
		)
	}
	if t.Name == "" {
		return fmt.Errorf("topology name is required")
	}

	// Validate segment names are unique
	names := make(map[string]bool)
	for _, seg := range t.Segments {
		if seg.Name == "" {
			return fmt.Errorf("segment name cannot be empty")
		}
		if names[seg.Name] {
			return fmt.Errorf("duplicate segment name: %q", seg.Name)
		}
		names[seg.Name] = true

		// Validate action values
		if seg.Internet != "" && seg.Internet != "allow" && seg.Internet != "deny" {
			return fmt.Errorf("segment %q: internet must be 'allow' or 'deny'",
				seg.Name)
		}
		if seg.InterSegment != "" &&
			seg.InterSegment != "allow" && seg.InterSegment != "deny" {
			return fmt.Errorf("segment %q: inter_segment must be 'allow' or 'deny'",
				seg.Name)
		}
	}

	// Validate policies reference known segments
	for _, p := range t.Policies {
		if p.Action != "allow" && p.Action != "deny" && p.Action != "redirect" {
			return fmt.Errorf("policy %q: action must be 'allow', 'deny', or 'redirect'",
				p.Name)
		}
		for _, s := range p.ApplyTo {
			if !names[s] {
				return fmt.Errorf("policy %q: apply_to references unknown segment %q",
					p.Name, s)
			}
		}
		// Validate CIDR ranges
		for _, cidr := range p.Match.DstCIDR {
			if _, _, err := net.ParseCIDR(cidr); err != nil {
				return fmt.Errorf("policy %q: invalid dst_cidr %q: %w",
					p.Name, cidr, err)
			}
		}
		// Validate port ranges
		for _, port := range append(p.Match.SrcPort, p.Match.DstPort...) {
			if port < 1 || port > 65535 {
				return fmt.Errorf("policy %q: port %d out of range 1-65535",
					p.Name, port)
			}
		}
	}

	// Validate routes
	for _, r := range t.Routes {
		if r.Dst != "0.0.0.0/0" && r.Dst != "::/0" {
			if _, _, err := net.ParseCIDR(r.Dst); err != nil {
				return fmt.Errorf("route dst %q: %w", r.Dst, err)
			}
		}
	}

	return nil
}

// computePriority assigns a numeric priority to a policy based on specificity.
// More specific matches get higher priority (lower number = higher priority).
func computePriority(m *PolicyMatch) int {
	score := 1000
	if len(m.DstHost) > 0 {
		score -= 400
	}
	if len(m.DstPort) > 0 {
		score -= 200
	}
	if len(m.SrcSegment) > 0 || len(m.DstSegment) > 0 {
		score -= 100
	}
	if m.Protocol != "" {
		score -= 50
	}
	if len(m.DstCIDR) > 0 {
		score -= 30
	}
	return score
}

// parseBandwidth converts "10Mbps", "1Gbps", "500Kbps" to bytes/sec.
func parseBandwidth(s string) (int64, error) {
	re := regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*(K|M|G)?bps$`)
	m := re.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, fmt.Errorf("invalid bandwidth format %q - use e.g. '10Mbps', '1Gbps'", s)
	}
	val, _ := strconv.ParseFloat(m[1], 64)
	switch m[2] {
	case "K":
		val *= 1000.0 / 8
	case "M":
		val *= 1000000.0 / 8
	case "G":
		val *= 1000000000.0 / 8
	default:
		val /= 8
	}
	return int64(val), nil
}

// parseQoS fills computed fields on a QoSSpec.
func parseQoS(q *QoSSpec) error {
	switch q.Priority {
	case "critical":
		q.tcPriority = 1
	case "high":
		q.tcPriority = 2
	case "normal", "":
		q.tcPriority = 3
	case "low":
		q.tcPriority = 4
	default:
		return fmt.Errorf("invalid priority %q - use critical/high/normal/low", q.Priority)
	}
	var err error
	if q.MaxBandwidth != "" {
		q.maxBps, err = parseBandwidth(q.MaxBandwidth)
		if err != nil {
			return fmt.Errorf("max_bandwidth: %w", err)
		}
	}
	if q.Burst != "" {
		q.burstBps, err = parseBandwidth(q.Burst)
		if err != nil {
			return fmt.Errorf("burst: %w", err)
		}
	}
	return nil
}
