// Package privatefs binds node custody to an admitted directory handle.
package privatefs

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

var ErrUnsafeCustody = errors.New("unsafe private custody")

func CheckRoot(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil {
		return err
	}
	return validateOwner(directory, info)
}

// Open creates private custody or admits an existing private, non-symlink
// directory. Validation and subsequent operations refer to the opened inode,
// including when the caller's configured pathname is later replaced.
func Open(path string) (*os.Root, error) {
	absolute, err := filepath.Abs(path)
	if err != nil || path == "" {
		return nil, fmt.Errorf("%w: private custody directory is required", ErrUnsafeCustody)
	}
	parent := filepath.Dir(absolute)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, err
	}
	anchor, err := os.OpenRoot(parent)
	if err != nil {
		return nil, err
	}
	defer anchor.Close()
	return OpenChild(anchor, filepath.Base(absolute))
}

// OpenChild admits a single private child below an already descriptor-bound
// custody root; a replaced ancestor pathname cannot redirect child creation.
func OpenChild(anchor *os.Root, name string) (*os.Root, error) {
	if name == "" || name == "." || name != filepath.Base(name) {
		return nil, fmt.Errorf("%w: invalid custody child", ErrUnsafeCustody)
	}

	createErr := anchor.Mkdir(name, 0o700)
	if createErr != nil && !errors.Is(createErr, os.ErrExist) {
		return nil, createErr
	}
	before, err := anchor.Lstat(name)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: private custody must be a non-symlink directory", ErrUnsafeCustody)
	}
	root, err := anchor.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	directory, err := root.Open(".")
	if err != nil {
		root.Close()
		return nil, err
	}
	defer directory.Close()
	opened, err := directory.Stat()
	if err != nil || !os.SameFile(before, opened) {
		root.Close()
		return nil, fmt.Errorf("%w: private custody directory changed during admission", ErrUnsafeCustody)
	}
	if createErr == nil {
		if err := initializeDirectory(directory, opened); err != nil {
			root.Close()
			return nil, err
		}
	}
	if err := validateOwner(directory, opened); err != nil {
		root.Close()
		return nil, err
	}
	return root, nil
}

// Replace atomically publishes a complete file beneath root. Exclusive unique
// temporary files cannot follow a planted predictable temporary-file symlink.
func Replace(root *os.Root, name string, data []byte, mode os.FileMode) error {
	file, temporary, err := CreateTemp(root, "."+filepath.Base(name)+".tmp-", mode)
	if err != nil {
		return fmt.Errorf("create private temporary file: %w", err)
	}
	defer root.Remove(temporary)
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := root.Rename(temporary, name); err != nil {
		return err
	}
	// Directory fsync is best effort, as in the existing identity writer:
	// not all supported platforms/filesystems implement it.
	if directory, err := root.Open("."); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}

func CreateTemp(root *os.Root, prefix string, mode os.FileMode) (*os.File, string, error) {
	name := prefix + rand.Text()
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil, "", err
	}
	if err := protectFile(file, mode); err != nil {
		file.Close()
		root.Remove(name)
		return nil, "", err
	}
	return file, name, nil
}

// OpenFile admits the actual private leaf inode, not only its containing
// directory. Existing writable, shared-link or symlink leaves are unsafe even
// when their parent directory prevents another principal adding entries.
func OpenFile(root *os.Root, name string, flag int, mode os.FileMode) (*os.File, error) {
	truncate := flag&os.O_TRUNC != 0
	flag &^= os.O_TRUNC
	before, err := root.Lstat(name)
	created := errors.Is(err, os.ErrNotExist) && flag&os.O_CREATE != 0
	if err != nil && !created {
		return nil, err
	}
	if !created && flag&(os.O_CREATE|os.O_EXCL) == os.O_CREATE|os.O_EXCL {
		return nil, &os.PathError{Op: "open", Path: name, Err: os.ErrExist}
	}
	if !created && !before.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: private custody leaf must be a regular non-symlink file", ErrUnsafeCustody)
	}
	if created {
		flag |= os.O_EXCL
	} else {
		flag &^= os.O_CREATE
	}
	file, err := root.OpenFile(name, flag, mode)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err == nil && !created && !os.SameFile(before, info) {
		err = fmt.Errorf("%w: private custody leaf changed during admission", ErrUnsafeCustody)
	}
	if err == nil && created {
		err = protectFile(file, mode)
	}
	if err == nil {
		err = validateFile(file, info)
	}
	if err == nil && truncate {
		err = file.Truncate(0)
	}
	if err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func ReadFile(root *os.Root, name string) ([]byte, error) {
	file, err := OpenFile(root, name, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}
