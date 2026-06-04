//go:build linux && !amd64

// Package scheduler - seccomp_linux_other.go
// Stub for non-amd64 Linux architectures (arm64, 386, etc.).
// The seccomp BPF filter uses amd64 syscall numbers and cannot be applied
// directly on other architectures. Namespace isolation still applies.
// Future work: add architecture-specific syscall lists for arm64.
package scheduler

// buildSeccompFilter returns nil on non-amd64 Linux.
// The process will run without syscall filtering but with full namespace isolation.
func buildSeccompFilter() ([]byte, error) {
	return nil, nil
}
