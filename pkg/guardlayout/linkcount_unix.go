//go:build unix

package guardlayout

import (
	"io/fs"
	"os"
	"syscall"
)

// linkCount is the number of hard links of the inode behind info.
func linkCount(info fs.FileInfo) uint64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(stat.Nlink) // #nosec G115 -- a link count is never negative.
	}
	return 1
}

// ownedByCaller reports whether the inode behind info belongs to this
// process's effective user.
func ownedByCaller(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Geteuid()
}
