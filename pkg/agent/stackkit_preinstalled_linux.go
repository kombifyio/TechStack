package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func defaultStackKitPreinstalledManifest() string {
	return "/opt/stackkits-image/neutral-image.json"
}

// The image cache is host provisioning state, never a path supplied by Core or
// an inherited environment override. Missing caches preserve ordinary hosts;
// an installed cache must remain protected all the way to the filesystem root.
// StackKits performs manifest decoding and all release/profile admission.
func stackKitPreinstalledManifest(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", fmt.Errorf("preinstalled image requires an absolute canonical manifest path")
	}
	cache := filepath.Dir(path)
	if _, err := os.Lstat(cache); errors.Is(err, os.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", fmt.Errorf("inspect preinstalled image cache: %w", err)
	}
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return "", fmt.Errorf("inspect preinstalled image path: %w", err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || info.Mode().Perm()&0o022 != 0 || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("preinstalled image paths must be root-owned, protected and free of symlinks")
		}
		if current == path {
			if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 2<<20 {
				return "", fmt.Errorf("preinstalled image manifest requires a bounded regular file")
			}
		} else if !info.IsDir() {
			return "", fmt.Errorf("preinstalled image path requires real directories")
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	return path, nil
}
