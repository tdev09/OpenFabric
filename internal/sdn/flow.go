package sdn

import "time"

// FlowRecord models network connection meta-statistics exported by the data plane.
type FlowRecord struct {
	SrcIP        string    `json:"src_ip"`
	DstIP        string    `json:"dst_ip"`
	SrcPort      int       `json:"src_port"`
	DstPort      int       `json:"dst_port"`
	Proto        string    `json:"proto"`
	BytesTrans   int64     `json:"bytes_trans"`
	PacketsTrans int64     `json:"packets_trans"`
	PolicyMatch  string    `json:"policy_match"`
	FirstSeen    time.Time `json:"first_seen"`
	LastSeen     time.Time `json:"last_seen"`
}
