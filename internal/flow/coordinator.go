package flow

import (
	"github.com/openfabric/openfabric/internal/cluster"
)

// IsCoordinator determines if the local node is the elected coordinator.
func IsCoordinator(clusterMgr *cluster.Manager, selfID string) bool {
	coordID := clusterMgr.CoordinatorID()
	if coordID == "" {
		return true // Solo node or empty cluster fallback
	}
	return coordID == selfID
}
