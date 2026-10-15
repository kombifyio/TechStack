package hostidentity

import (
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"

	"github.com/kombifyio/techstack/internal/privatefs"
)

// WriteFileAtomic replaces path with data so a reader or a crash observes
// either the old or the new complete file, never a partial one: write a
// sibling temp file with the final mode, fsync it, rename it over the target
// and fsync the directory. The temp file is removed on every failure path.
func WriteFileAtomic(path string, data []byte, mode os.FileMode) error {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer root.Close()
	return privatefs.Replace(root, filepath.Base(path), data, mode)
}

// wipeRootedSecretFile destroys only a file within admitted identity custody.
func wipeRootedSecretFile(root *os.Root, name string) error {
	file, err := privatefs.OpenFile(root, name, os.O_WRONLY, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if errors.Is(err, os.ErrPermission) {
		// A private owner-read-only inode is still removable by its custody
		// owner. Admit it read-only before unlinking, without following a link
		// or bypassing the same owner/mode/link checks.
		admitted, readErr := privatefs.OpenFile(root, name, os.O_RDONLY, 0)
		if readErr != nil {
			return readErr
		}
		if closeErr := admitted.Close(); closeErr != nil {
			return closeErr
		}
		return root.Remove(name)
	}
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("identity secret must be a regular file")
	}
	if info.Size() > 0 && info.Size() <= 1<<20 {
		noise := make([]byte, info.Size())
		if _, err := rand.Read(noise); err != nil {
			return err
		}
		if _, err := file.WriteAt(noise, 0); err != nil {
			return err
		}
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// removeSecretFile overwrites a small secret file with random bytes before it
// is unlinked, so the capability does not linger in a recoverable block when
// the filesystem rewrites in place. A missing file is not an error.
func removeSecretFile(path string) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode().IsRegular() && info.Size() > 0 && info.Size() <= 1<<20 {
		noise := make([]byte, info.Size())
		if _, readErr := rand.Read(noise); readErr == nil {
			if handle, openErr := os.OpenFile(path, os.O_WRONLY, 0); openErr == nil {
				_, _ = handle.Write(noise)
				_ = handle.Sync()
				_ = handle.Close()
			}
		}
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// wipeSecretFile destroys a secret file for good: its content is overwritten
// and the file unlinked. When the directory is not writable for this user (the
// enrollment file lives in a root-owned directory and is only owned by the
// Guard), the emptied file stays but holds nothing.
func wipeSecretFile(path string) error {
	err := removeSecretFile(path)
	if err == nil {
		return nil
	}
	if info, statErr := os.Stat(path); statErr != nil || !info.Mode().IsRegular() {
		return err
	}
	if truncateErr := os.Truncate(path, 0); truncateErr != nil {
		return err
	}
	return nil
}
