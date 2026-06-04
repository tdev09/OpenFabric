// Package scheduler - resource_limits.go
// ResourceLimits defines per-task resource caps enforced at the OS level
// (rlimits on Linux/macOS, Job Objects on Windows, cgroup v2 on Linux).
//
// Zero values in any field mean "use the platform default".
package scheduler

import "time"

// ResourceLimits defines the maximum resources a single task process may consume.
type ResourceLimits struct {
	// MaxMemoryBytes caps virtual address space (RLIMIT_AS on Linux).
	// Default: 2 GiB. Set to 0 to use default.
	MaxMemoryBytes int64

	// MaxCPUSecs caps total CPU time in seconds (RLIMIT_CPU on Linux).
	// Default: derived from task timeout. Set to 0 to use timeout duration.
	MaxCPUSecs int

	// MaxOpenFiles caps simultaneous open file descriptors (RLIMIT_NOFILE).
	// Default: 256. Set to 0 to use default.
	MaxOpenFiles int

	// MaxProcs caps the number of child processes/threads (RLIMIT_NPROC).
	// This is the primary fork-bomb mitigation. Default: 64. Set to 0 to use default.
	MaxProcs int

	// MaxFileSizeBytes caps the size of any file the process may create (RLIMIT_FSIZE).
	// Default: 100 MiB. Set to 0 to use default.
	MaxFileSizeBytes int64
}

// DefaultResourceLimits returns sane conservative defaults.
// These are intentionally tight - operators can relax them via Settings.
func DefaultResourceLimits() ResourceLimits {
	return ResourceLimits{
		MaxMemoryBytes:   2 * 1024 * 1024 * 1024, // 2 GiB
		MaxCPUSecs:       0,                      // derived from task timeout
		MaxOpenFiles:     256,
		MaxProcs:         64,
		MaxFileSizeBytes: 100 * 1024 * 1024, // 100 MiB
	}
}

// WithTimeout fills MaxCPUSecs from a task timeout duration if unset.
func (r ResourceLimits) WithTimeout(d time.Duration) ResourceLimits {
	if r.MaxCPUSecs == 0 && d > 0 {
		r.MaxCPUSecs = int(d.Seconds()) + 5 // +5s grace
	}
	return r
}

// Filled returns a copy of r with all zero fields replaced by defaults.
func (r ResourceLimits) Filled() ResourceLimits {
	def := DefaultResourceLimits()
	if r.MaxMemoryBytes <= 0 {
		r.MaxMemoryBytes = def.MaxMemoryBytes
	}
	if r.MaxOpenFiles <= 0 {
		r.MaxOpenFiles = def.MaxOpenFiles
	}
	if r.MaxProcs <= 0 {
		r.MaxProcs = def.MaxProcs
	}
	if r.MaxFileSizeBytes <= 0 {
		r.MaxFileSizeBytes = def.MaxFileSizeBytes
	}
	return r
}
