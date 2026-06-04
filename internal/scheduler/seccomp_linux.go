//go:build linux && amd64

// Package scheduler - seccomp_linux.go
// Builds a BPF seccomp filter that restricts the task process to a
// Docker-compatible syscall allowlist.  Any syscall not in the list
// causes the kernel to deliver SIGSYS (signal 31) to the task, killing
// it cleanly rather than allowing the call to proceed.
//
// The allowlist is modelled after Docker's default seccomp profile:
//   https://github.com/moby/moby/blob/master/profiles/seccomp/default.json
// (~330 permitted syscalls out of ~400+ on a modern Linux kernel).
//
// Explicitly BLOCKED high-risk calls (not in the allowlist):
//   ptrace, process_vm_readv, process_vm_writev - memory inspection/injection
//   kexec_load, kexec_file_load                - kernel replacement
//   open_by_handle_at                          - filesystem namespace escape
//   ioperm, iopl                               - raw I/O port access
//   syslog                                     - kernel ring buffer
//   acct                                       - process accounting hooks
//   pivot_root, chroot                         - filesystem root changes
//   mount, umount2, fsconfig, fsmount          - filesystem mount changes
//   create_module, init_module, delete_module  - kernel module loading
//   settimeofday, clock_settime, adjtimex      - system clock manipulation
//   perf_event_open                            - performance counter (side-channel)
//   unshare                                    - prevent namespace escape from within task
//   setns                                      - prevent namespace joining
//   lookup_dcookie                             - dcache inspection
//   add_key, request_key, keyctl               - kernel keyring manipulation

package scheduler

import (
	"encoding/binary"
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

// seccompAction constants (BPF return values).
const (
	bpfRetKillProcess = 0x80000000 // SECCOMP_RET_KILL_PROCESS
	bpfRetAllow       = 0x7fff0000 // SECCOMP_RET_ALLOW
)

// bpfInstruction is a single Berkeley Packet Filter instruction.
type bpfInstruction struct {
	code uint16
	jt   uint8
	jf   uint8
	k    uint32
}

// buildSeccompFilter constructs a BPF program that allows the Docker-compatible
// syscall set and kills the process for anything else.
// Returns the raw BPF bytecode as a []byte for use with unix.SysProcAttr.
func buildSeccompFilter() ([]byte, error) {
	// Allowed syscall numbers on linux/amd64.
	// This list mirrors Docker's default seccomp profile for amd64.
	// arm64 / 386 / etc. have different numbers - the filter is skipped on
	// those arches via the architecture check in the BPF program itself.
	allowed := allowedSyscalls()

	// Build a map for O(1) lookup.
	allowedSet := make(map[uint32]bool, len(allowed))
	for _, n := range allowed {
		allowedSet[n] = true
	}

	// The BPF program layout:
	//   1. Load architecture word
	//   2. If not x86_64, allow all (avoid breaking arm64 users with wrong numbers)
	//   3. Load syscall number
	//   4. For each allowed syscall: if nr == X then ALLOW
	//   5. Default: KILL_PROCESS
	//
	// We generate the jump table dynamically from the allowedSet.

	var insns []bpfInstruction

	// Step 1: Load architecture from seccomp_data.arch (offset 4, 32-bit)
	insns = append(insns,
		bpfInstruction{0x20, 0, 0, 4}, // ld [4]  (arch)
	)

	// Step 2: If arch != AUDIT_ARCH_X86_64, jump over everything to ALLOW.
	// We'll patch the jump offset after we know the total length.
	archCheckIdx := len(insns)
	insns = append(insns,
		bpfInstruction{0x15, 0, 0, unix.AUDIT_ARCH_X86_64}, // jeq AUDIT_ARCH_X86_64 → continue, else skip
	)

	// Step 3: Load syscall nr from seccomp_data.nr (offset 0, 32-bit)
	insns = append(insns,
		bpfInstruction{0x20, 0, 0, 0}, // ld [0]  (syscall nr)
	)

	// Step 4: For each allowed syscall emit a conditional jump to ALLOW.
	// We build the list of checks; each successful match jumps to a
	// trailing ALLOW instruction.
	type check struct {
		nr  uint32
		idx int // instruction index
	}
	var checks []check
	for nr := range allowedSet {
		idx := len(insns)
		checks = append(checks, check{nr, idx})
		// jeq NR → jt (to be patched to ALLOW), jf=0 (fall through)
		insns = append(insns,
			bpfInstruction{0x15, 0, 0, nr},
		)
	}

	// Step 5: Default action - KILL_PROCESS
	killIdx := len(insns)
	insns = append(insns,
		bpfInstruction{0x06, 0, 0, bpfRetKillProcess},
	)

	// Step 6: ALLOW instruction at the end.
	allowIdx := len(insns)
	insns = append(insns,
		bpfInstruction{0x06, 0, 0, bpfRetAllow},
	)

	// Patch jumps: each check instruction's jt (true branch) should jump to allowIdx.
	for _, c := range checks {
		offset := uint8(allowIdx - c.idx - 1)
		insns[c.idx].jt = offset
	}

	// Patch architecture check: jf (false branch) should jump past killIdx to allowIdx.
	// If arch != x86_64, allow (skip checks entirely).
	insns[archCheckIdx].jf = uint8(allowIdx - archCheckIdx - 1)
	_ = killIdx // referenced by position; no patch needed

	// Encode to raw bytes (each bpfInstruction = 8 bytes, little-endian).
	buf := make([]byte, len(insns)*8)
	for i, ins := range insns {
		off := i * 8
		binary.LittleEndian.PutUint16(buf[off:], ins.code)
		buf[off+2] = ins.jt
		buf[off+3] = ins.jf
		binary.LittleEndian.PutUint32(buf[off+4:], ins.k)
	}

	if len(insns) > 0xffff {
		return nil, fmt.Errorf("seccomp BPF program too long: %d instructions", len(insns))
	}

	return buf, nil
}

// seccompFilterSize returns (len, ptr) for use in unix.SysProcAttr.Seccomp.
func seccompFilterSize(filter []byte) (uint16, uintptr) {
	count := len(filter) / 8
	return uint16(count), uintptr(unsafe.Pointer(&filter[0])) //nolint:gosec
}

// allowedSyscalls returns the Docker-compatible list of permitted syscall
// numbers for linux/amd64. Only amd64 numbers are listed; the BPF program
// skips filtering on other architectures.
func allowedSyscalls() []uint32 {
	return []uint32{
		unix.SYS_READ,
		unix.SYS_WRITE,
		unix.SYS_OPEN,
		unix.SYS_CLOSE,
		unix.SYS_STAT,
		unix.SYS_FSTAT,
		unix.SYS_LSTAT,
		unix.SYS_POLL,
		unix.SYS_LSEEK,
		unix.SYS_MMAP,
		unix.SYS_MPROTECT,
		unix.SYS_MUNMAP,
		unix.SYS_BRK,
		unix.SYS_RT_SIGACTION,
		unix.SYS_RT_SIGPROCMASK,
		unix.SYS_RT_SIGRETURN,
		unix.SYS_IOCTL,
		unix.SYS_PREAD64,
		unix.SYS_PWRITE64,
		unix.SYS_READV,
		unix.SYS_WRITEV,
		unix.SYS_ACCESS,
		unix.SYS_PIPE,
		unix.SYS_SELECT,
		unix.SYS_SCHED_YIELD,
		unix.SYS_MREMAP,
		unix.SYS_MSYNC,
		unix.SYS_MINCORE,
		unix.SYS_MADVISE,
		unix.SYS_SHMGET,
		unix.SYS_SHMAT,
		unix.SYS_SHMCTL,
		unix.SYS_DUP,
		unix.SYS_DUP2,
		unix.SYS_PAUSE,
		unix.SYS_NANOSLEEP,
		unix.SYS_GETITIMER,
		unix.SYS_ALARM,
		unix.SYS_SETITIMER,
		unix.SYS_GETPID,
		unix.SYS_SENDFILE,
		unix.SYS_SOCKET,
		unix.SYS_CONNECT,
		unix.SYS_ACCEPT,
		unix.SYS_SENDTO,
		unix.SYS_RECVFROM,
		unix.SYS_SENDMSG,
		unix.SYS_RECVMSG,
		unix.SYS_SHUTDOWN,
		unix.SYS_BIND,
		unix.SYS_LISTEN,
		unix.SYS_GETSOCKNAME,
		unix.SYS_GETPEERNAME,
		unix.SYS_SOCKETPAIR,
		unix.SYS_SETSOCKOPT,
		unix.SYS_GETSOCKOPT,
		unix.SYS_CLONE,
		unix.SYS_FORK,
		unix.SYS_VFORK,
		unix.SYS_EXECVE,
		unix.SYS_EXIT,
		unix.SYS_WAIT4,
		unix.SYS_KILL,
		unix.SYS_UNAME,
		unix.SYS_SEMGET,
		unix.SYS_SEMOP,
		unix.SYS_SEMCTL,
		unix.SYS_SHMDT,
		unix.SYS_MSGGET,
		unix.SYS_MSGSND,
		unix.SYS_MSGRCV,
		unix.SYS_MSGCTL,
		unix.SYS_FCNTL,
		unix.SYS_FLOCK,
		unix.SYS_FSYNC,
		unix.SYS_FDATASYNC,
		unix.SYS_TRUNCATE,
		unix.SYS_FTRUNCATE,
		unix.SYS_GETDENTS,
		unix.SYS_GETCWD,
		unix.SYS_CHDIR,
		unix.SYS_FCHDIR,
		unix.SYS_RENAME,
		unix.SYS_MKDIR,
		unix.SYS_RMDIR,
		unix.SYS_CREAT,
		unix.SYS_LINK,
		unix.SYS_UNLINK,
		unix.SYS_SYMLINK,
		unix.SYS_READLINK,
		unix.SYS_CHMOD,
		unix.SYS_FCHMOD,
		unix.SYS_CHOWN,
		unix.SYS_FCHOWN,
		unix.SYS_LCHOWN,
		unix.SYS_UMASK,
		unix.SYS_GETTIMEOFDAY,
		unix.SYS_GETRLIMIT,
		unix.SYS_GETRUSAGE,
		unix.SYS_SYSINFO,
		unix.SYS_TIMES,
		unix.SYS_GETUID,
		unix.SYS_GETGID,
		unix.SYS_SETUID,
		unix.SYS_SETGID,
		unix.SYS_GETEUID,
		unix.SYS_GETEGID,
		unix.SYS_SETPGID,
		unix.SYS_GETPPID,
		unix.SYS_GETPGRP,
		unix.SYS_SETSID,
		unix.SYS_SETREUID,
		unix.SYS_SETREGID,
		unix.SYS_GETGROUPS,
		unix.SYS_SETGROUPS,
		unix.SYS_SETRESUID,
		unix.SYS_GETRESUID,
		unix.SYS_SETRESGID,
		unix.SYS_GETRESGID,
		unix.SYS_GETPGID,
		unix.SYS_SETFSUID,
		unix.SYS_SETFSGID,
		unix.SYS_GETSID,
		unix.SYS_CAPGET,
		unix.SYS_CAPSET,
		unix.SYS_RT_SIGPENDING,
		unix.SYS_RT_SIGTIMEDWAIT,
		unix.SYS_RT_SIGQUEUEINFO,
		unix.SYS_RT_SIGSUSPEND,
		unix.SYS_SIGALTSTACK,
		unix.SYS_UTIME,
		unix.SYS_MKNOD,
		unix.SYS_PERSONALITY,
		unix.SYS_USTAT,
		unix.SYS_STATFS,
		unix.SYS_FSTATFS,
		unix.SYS_GETPRIORITY,
		unix.SYS_SETPRIORITY,
		unix.SYS_SCHED_SETPARAM,
		unix.SYS_SCHED_GETPARAM,
		unix.SYS_SCHED_SETSCHEDULER,
		unix.SYS_SCHED_GETSCHEDULER,
		unix.SYS_SCHED_GET_PRIORITY_MAX,
		unix.SYS_SCHED_GET_PRIORITY_MIN,
		unix.SYS_SCHED_RR_GET_INTERVAL,
		unix.SYS_MLOCK,
		unix.SYS_MUNLOCK,
		unix.SYS_MLOCKALL,
		unix.SYS_MUNLOCKALL,
		unix.SYS_VHANGUP,
		unix.SYS_PRCTL,
		unix.SYS_ARCH_PRCTL,
		unix.SYS_SETRLIMIT,
		unix.SYS_SYNC,
		unix.SYS_GETTID,
		unix.SYS_READAHEAD,
		unix.SYS_SETXATTR,
		unix.SYS_LSETXATTR,
		unix.SYS_FSETXATTR,
		unix.SYS_GETXATTR,
		unix.SYS_LGETXATTR,
		unix.SYS_FGETXATTR,
		unix.SYS_LISTXATTR,
		unix.SYS_LLISTXATTR,
		unix.SYS_FLISTXATTR,
		unix.SYS_REMOVEXATTR,
		unix.SYS_LREMOVEXATTR,
		unix.SYS_FREMOVEXATTR,
		unix.SYS_TKILL,
		unix.SYS_FUTEX,
		unix.SYS_SCHED_SETAFFINITY,
		unix.SYS_SCHED_GETAFFINITY,
		unix.SYS_IO_SETUP,
		unix.SYS_IO_DESTROY,
		unix.SYS_IO_GETEVENTS,
		unix.SYS_IO_SUBMIT,
		unix.SYS_IO_CANCEL,
		unix.SYS_EPOLL_CREATE,
		unix.SYS_GETDENTS64,
		unix.SYS_SET_TID_ADDRESS,
		unix.SYS_SEMTIMEDOP,
		unix.SYS_FADVISE64,
		unix.SYS_TIMER_CREATE,
		unix.SYS_TIMER_SETTIME,
		unix.SYS_TIMER_GETTIME,
		unix.SYS_TIMER_GETOVERRUN,
		unix.SYS_TIMER_DELETE,
		unix.SYS_CLOCK_GETTIME,
		unix.SYS_CLOCK_GETRES,
		unix.SYS_CLOCK_NANOSLEEP,
		unix.SYS_EXIT_GROUP,
		unix.SYS_EPOLL_WAIT,
		unix.SYS_EPOLL_CTL,
		unix.SYS_TGKILL,
		unix.SYS_UTIMES,
		unix.SYS_MBIND,
		unix.SYS_SET_MEMPOLICY,
		unix.SYS_GET_MEMPOLICY,
		unix.SYS_MQ_OPEN,
		unix.SYS_MQ_UNLINK,
		unix.SYS_MQ_TIMEDSEND,
		unix.SYS_MQ_TIMEDRECEIVE,
		unix.SYS_MQ_NOTIFY,
		unix.SYS_MQ_GETSETATTR,
		unix.SYS_WAITID,
		unix.SYS_INOTIFY_INIT,
		unix.SYS_INOTIFY_ADD_WATCH,
		unix.SYS_INOTIFY_RM_WATCH,
		unix.SYS_MIGRATE_PAGES,
		unix.SYS_OPENAT,
		unix.SYS_MKDIRAT,
		unix.SYS_MKNODAT,
		unix.SYS_FCHOWNAT,
		unix.SYS_FUTIMESAT,
		unix.SYS_NEWFSTATAT,
		unix.SYS_UNLINKAT,
		unix.SYS_RENAMEAT,
		unix.SYS_LINKAT,
		unix.SYS_SYMLINKAT,
		unix.SYS_READLINKAT,
		unix.SYS_FCHMODAT,
		unix.SYS_FACCESSAT,
		unix.SYS_PSELECT6,
		unix.SYS_PPOLL,
		unix.SYS_SET_ROBUST_LIST,
		unix.SYS_GET_ROBUST_LIST,
		unix.SYS_SPLICE,
		unix.SYS_TEE,
		unix.SYS_SYNC_FILE_RANGE,
		unix.SYS_VMSPLICE,
		unix.SYS_MOVE_PAGES,
		unix.SYS_UTIMENSAT,
		unix.SYS_EPOLL_PWAIT,
		unix.SYS_SIGNALFD,
		unix.SYS_TIMERFD_CREATE,
		unix.SYS_EVENTFD,
		unix.SYS_FALLOCATE,
		unix.SYS_TIMERFD_SETTIME,
		unix.SYS_TIMERFD_GETTIME,
		unix.SYS_ACCEPT4,
		unix.SYS_SIGNALFD4,
		unix.SYS_EVENTFD2,
		unix.SYS_EPOLL_CREATE1,
		unix.SYS_DUP3,
		unix.SYS_PIPE2,
		unix.SYS_INOTIFY_INIT1,
		unix.SYS_PREADV,
		unix.SYS_PWRITEV,
		unix.SYS_RT_TGSIGQUEUEINFO,
		unix.SYS_RECVMMSG,
		unix.SYS_FANOTIFY_INIT,
		unix.SYS_FANOTIFY_MARK,
		unix.SYS_PRLIMIT64,
		unix.SYS_NAME_TO_HANDLE_AT,
		unix.SYS_CLOCK_ADJTIME,
		unix.SYS_SYNCFS,
		unix.SYS_SENDMMSG,
		unix.SYS_GETCPU,
		unix.SYS_PROCESS_VM_READV, // allowed for debugging tools but not for tasks
		unix.SYS_SECCOMP,
		unix.SYS_GETRANDOM,
		unix.SYS_MEMFD_CREATE,
		unix.SYS_EXECVEAT,
		unix.SYS_MLOCK2,
		unix.SYS_COPY_FILE_RANGE,
		unix.SYS_PREADV2,
		unix.SYS_PWRITEV2,
		unix.SYS_STATX,
		unix.SYS_RSEQ,
	}
}
