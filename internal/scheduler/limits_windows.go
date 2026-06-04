//go:build windows

// Package scheduler - limits_windows.go
// Enforces per-task resource limits on Windows using Job Objects.
// A Job Object is a kernel container that constrains the resource consumption
// of all processes assigned to it, including transitive children.
//
// Limits enforced:
//   - Maximum working set (memory) via JOBOBJECT_EXTENDED_LIMIT_INFORMATION
//   - Active process count (fork-bomb mitigation) via JOBOBJECT_BASIC_LIMIT_INFORMATION
//
// Note: Windows does not support seccomp or Linux namespaces. The Job Object
// provides resource containment only, not syscall filtering.
package scheduler

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// applyRlimitsToAttr is a no-op on Windows; rlimits are applied post-start via Job Objects.
func applyRlimitsToAttr(attr interface{}, lim ResourceLimits) {
	// Windows does not support SysProcAttr.Rlimit - limits applied in applyJobObject.
}

// applyRlimitsToProcess on Windows uses Job Objects instead of POSIX rlimits.
func applyRlimitsToProcess(pid int, lim ResourceLimits) error {
	return applyJobObject(pid, lim)
}

// applyJobObject creates a Windows Job Object, sets resource limits, and assigns
// the process identified by pid to it. Must be called after the process starts.
func applyJobObject(pid int, lim ResourceLimits) error {
	lim = lim.Filled()

	// Open the target process.
	procHandle, err := windows.OpenProcess(
		windows.PROCESS_ALL_ACCESS,
		false,
		uint32(pid),
	)
	if err != nil {
		return fmt.Errorf("OpenProcess: %w", err)
	}
	defer windows.CloseHandle(procHandle)

	// Create a new Job Object.
	jobHandle, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("CreateJobObject: %w", err)
	}
	defer windows.CloseHandle(jobHandle)

	// Set basic limits (active process count).
	type JOBOBJECT_BASIC_LIMIT_INFORMATION struct {
		PerProcessUserTimeLimit int64
		PerJobUserTimeLimit     int64
		LimitFlags              uint32
		MinimumWorkingSetSize   uintptr
		MaximumWorkingSetSize   uintptr
		ActiveProcessLimit      uint32
		Affinity                uintptr
		PriorityClass           uint32
		SchedulingClass         uint32
	}
	const JOB_OBJECT_LIMIT_ACTIVE_PROCESS = 0x00000008

	basicLimits := JOBOBJECT_BASIC_LIMIT_INFORMATION{
		LimitFlags:         JOB_OBJECT_LIMIT_ACTIVE_PROCESS,
		ActiveProcessLimit: uint32(lim.MaxProcs),
	}
	_, err = windows.SetInformationJobObject(
		jobHandle,
		windows.JobObjectBasicLimitInformation,
		uintptr(unsafe.Pointer(&basicLimits)),
		uint32(unsafe.Sizeof(basicLimits)),
	)
	if err != nil {
		return fmt.Errorf("SetInformationJobObject (basic): %w", err)
	}

	// Set extended limits (memory commit limit).
	type JOBOBJECT_EXTENDED_LIMIT_INFORMATION struct {
		BasicLimitInformation JOBOBJECT_BASIC_LIMIT_INFORMATION
		IoInfo                [2]uint64 // IO_COUNTERS
		ProcessMemoryLimit    uintptr
		JobMemoryLimit        uintptr
		PeakProcessMemoryUsed uintptr
		PeakJobMemoryUsed     uintptr
	}
	const JOB_OBJECT_LIMIT_PROCESS_MEMORY = 0x00000100

	extLimits := JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags:         JOB_OBJECT_LIMIT_PROCESS_MEMORY | JOB_OBJECT_LIMIT_ACTIVE_PROCESS,
			ActiveProcessLimit: uint32(lim.MaxProcs),
		},
		ProcessMemoryLimit: uintptr(lim.MaxMemoryBytes),
	}
	_, err = windows.SetInformationJobObject(
		jobHandle,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&extLimits)),
		uint32(unsafe.Sizeof(extLimits)),
	)
	if err != nil {
		return fmt.Errorf("SetInformationJobObject (extended): %w", err)
	}

	// Assign the process to the job.
	if err := windows.AssignProcessToJobObject(jobHandle, procHandle); err != nil {
		return fmt.Errorf("AssignProcessToJobObject: %w", err)
	}

	return nil
}

// writeCgroup is a no-op on Windows (cgroup v2 is Linux-only).
func writeCgroup(pid int, lim ResourceLimits, log interface{}) {}

// cleanupCgroup is a no-op on Windows.
func cleanupCgroup(cgroupDir string, log interface{}) {}

// cgroupDirForPID returns empty string on Windows.
func cgroupDirForPID(pid int) string { return "" }
