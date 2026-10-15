//go:build !unix

package guardlayout

import (
	"errors"
	"io/fs"
	"os"
)

func ownershipIDs(fs.FileInfo) (int, int, error) {
	return 0, 0, errors.New("Guard ownership migration requires Unix inode metadata")
}

func lockLayoutState(*Options) (func(), error) {
	return nil, errors.New("Guard layout migration requires Unix lifecycle locking")
}

func ownershipIdentity(fs.FileInfo) (uint64, uint64, error) {
	return 0, 0, errors.New("Guard inode migration requires Unix metadata")
}

func restoreOwnershipAt(*os.Root, string, fs.FileInfo, int, int, os.FileMode, func(*os.Root, string, int, int) error) error {
	return errors.New("Guard ownership migration requires Unix inode handles")
}
