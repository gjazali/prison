//go:build unix

package vault

import (
	"io/fs"
	"syscall"
)

func fileInode(info fs.FileInfo) uint64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat != nil {
		return stat.Ino
	}
	return 0
}
