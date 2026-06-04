package sdn

import (
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Reconciler synchronises the desired SDN RuleSet onto the platform DataPlane.
type Reconciler struct {
	mu            sync.RWMutex
	dp            DataPlane
	log           *zap.Logger
	currentTarget *RuleSet
	lastApplied   *RuleSet
	lastError     error
	lastUpdate    time.Time
}

// NewReconciler creates a Reconciler.
func NewReconciler(dp DataPlane, log *zap.Logger) *Reconciler {
	return &Reconciler{
		dp:  dp,
		log: log,
	}
}

// Reconcile applies the given RuleSet if the version or hash differs from the current configuration.
func (r *Reconciler) Reconcile(rs *RuleSet) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.currentTarget = rs
	r.lastUpdate = time.Now()

	// Check if we already applied this specific config version
	if r.lastApplied != nil && r.lastApplied.TopologyHash == rs.TopologyHash && r.lastApplied.Version == rs.Version {
		r.log.Debug("ruleset version already converged, skipping", zap.Uint64("version", rs.Version))
		return nil
	}

	r.log.Info("reconciling data plane state", zap.String("node_id", rs.NodeID), zap.Uint64("version", rs.Version))

	if err := r.dp.Apply(rs); err != nil {
		r.lastError = err
		r.log.Error("failed to apply ruleset to host kernel", zap.Error(err))
		return fmt.Errorf("data plane apply: %w", err)
	}

	r.lastApplied = rs
	r.lastError = nil
	r.log.Info("data plane rules successfully reconciled", zap.Uint64("version", rs.Version))
	return nil
}

// Status returns the current platform-specific firewall configuration rules list.
func (r *Reconciler) Status() (string, error) {
	return r.dp.Status()
}

// LastStatus returns status fields describing convergence state.
func (r *Reconciler) LastStatus() (uint64, string, string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var hash string
	var version uint64
	if r.lastApplied != nil {
		hash = r.lastApplied.TopologyHash
		version = r.lastApplied.Version
	}
	return version, hash, r.dp.DataPlaneInterface(), r.lastError
}
