//go:build !windows
// +build !windows

package tunnel

import "os"

func checkIsRoot() bool {
	return os.Getuid() == 0
}
