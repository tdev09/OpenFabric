//go:build !windows

package renameio

import "os"

func renameFile(src, dst string) error {
	return os.Rename(src, dst)
}
