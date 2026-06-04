package sdn

// DataPlane represents a platform-specific data plane adapter (e.g. nftables, pf, WFP, or userspace stub).
type DataPlane interface {
	// Apply atomically replaces all OpenFabric firewall rules, QoS classes, and routes.
	Apply(rs *RuleSet) error
	// Flush clears all OpenFabric SDN configuration from the host kernel.
	Flush() error
	// Status returns a diagnostic string of the currently applied rules.
	Status() (string, error)
	// DataPlaneInterface returns the primary network interface managed by this adapter.
	DataPlaneInterface() string
}
