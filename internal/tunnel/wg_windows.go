//go:build windows
// +build windows

package tunnel

func checkIsRoot() bool {
	// On Windows, let the OS handle administrator access check or let wireguard-go fail if run without admin.
	return true
}
