//go:build unix

package nfsexport

import (
	"os"
	"syscall"
)

func fileOwner(info os.FileInfo) (int, bool) {
	status, found := info.Sys().(*syscall.Stat_t)
	if !found {
		return 0, false
	}
	return int(status.Uid), true
}
