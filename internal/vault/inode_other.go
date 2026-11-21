//go:build !unix

package vault

import "io/fs"

func fileInode(fs.FileInfo) uint64 {
	return 0
}
