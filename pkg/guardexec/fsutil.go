package guardexec

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// readHandoff reads one bounded regular file from the handoff directory. The
// directory is writable by the unprivileged daemon, so the name is confined to
// it (no traversal, no symlink out) and the content is treated as untrusted.
func readHandoff(dir, name string, limit int64) ([]byte, error) {
	if name == "" || filepath.Base(name) != name || name == "." || name == ".." {
		return nil, deny(CodeInvalidRequest, "%q is not a file name in the handoff directory", name)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open the handoff directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	file, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open handoff file %s: %w", name, err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, deny(CodeInvalidRequest, "handoff file %s is not a regular file", name)
	}
	if info.Size() > limit {
		return nil, deny(CodeInvalidRequest, "handoff file %s exceeds %d bytes", name, limit)
	}
	return io.ReadAll(io.LimitReader(file, limit+1))
}

// copyHandoff copies one handoff file into a file only root can reach and
// returns its sha256. Everything after this point reads the private copy, so
// the daemon cannot change the bytes between verification and installation.
func copyHandoff(dir, name, destination string, limit int64) (digest string, size int64, err error) {
	if name == "" || filepath.Base(name) != name || name == "." || name == ".." {
		return "", 0, deny(CodeInvalidRequest, "%q is not a file name in the handoff directory", name)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", 0, fmt.Errorf("open the handoff directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	source, err := root.Open(name)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = source.Close() }()
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", 0, deny(CodeInvalidRequest, "handoff file %s is not a regular file", name)
	}
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- a private path below the root-owned state directory.
	if err != nil {
		return "", 0, err
	}
	hash := sha256.New()
	size, err = io.Copy(io.MultiWriter(out, hash), io.LimitReader(source, limit+1))
	if err == nil && size > limit {
		err = deny(CodeInvalidRequest, "handoff file %s exceeds %d bytes", name, limit)
	}
	if err == nil {
		err = out.Sync()
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(destination)
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

// writeFileAtomic replaces path with data through a synced temporary file.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(cause error) error {
		_ = tmp.Close()
		_ = os.Remove(name)
		return cause
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(mode); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	handle, err := os.Open(dir) // #nosec G304 -- a fixed state directory.
	if err != nil {
		return err
	}
	defer func() { _ = handle.Close() }()
	if err := handle.Sync(); err != nil && !errors.Is(err, os.ErrInvalid) {
		return err
	}
	return nil
}

func writeJSONAtomic(path string, value any, mode os.FileMode) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, raw, mode)
}

func fileDigest(path string) (string, error) {
	file, err := os.Open(path) // #nosec G304 -- a path below the root-owned install or state directory.
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// copyExecutable writes src to dst (mode 0755, root) through a synced temp file.
func copyExecutable(src, dst string) error {
	in, err := os.Open(src) // #nosec G304 -- root-owned staged file.
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".tmp-"+filepath.Base(dst)+"-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(cause error) error {
		_ = tmp.Close()
		_ = os.Remove(name)
		return cause
	}
	if _, err := io.Copy(tmp, in); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, dst); err != nil {
		_ = os.Remove(name)
		return err
	}
	return syncDir(filepath.Dir(dst))
}
