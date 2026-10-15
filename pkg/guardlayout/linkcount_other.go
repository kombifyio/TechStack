//go:build !unix

package guardlayout

import "io/fs"

// linkCount is unknown off Unix, where the layout is never applied.
func linkCount(fs.FileInfo) uint64 { return 1 }

// ownedByCaller is never true off Unix, where nothing is pinned.
func ownedByCaller(fs.FileInfo) bool { return false }
