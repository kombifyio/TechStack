package guardrelease

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const treeDigestHeader = "kombify.guard.runtime-tree/v1\n"

// TreeDigest returns the canonical digest of a StackKits runtime bundle tree:
// the SHA-256 over the sorted slash paths of every regular file with its
// executable bit (only that bit; other permission bits and umask do not change
// the digest), size and content digest. Directories carry no content and are
// not part of it. Anything that is not a regular file or a directory (a symlink,
// a device, a socket) is refused, so the digest cannot hide an indirection.
//
// The release workflow computes it over the bundle tree it builds and signs it
// into the manifest; the privileged executor computes it over the extracted
// bundle before root reads or runs any file of it.
func TreeDigest(root string) (string, error) {
	return treeDigest(root, "")
}

// InstalledTreeDigest checks an activated runtime. The installer adds exactly
// one local archive-digest marker after authenticating the extracted tree; it
// is not a release member and cannot be included in the signed tree digest.
func InstalledTreeDigest(root string) (string, error) {
	const marker = ".techstack-bundle-sha256"
	info, err := os.Lstat(filepath.Join(root, marker))
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() != 65 {
		return "", errors.New("installed runtime archive marker is invalid")
	}
	file, err := os.Open(filepath.Join(root, marker))
	if err != nil {
		return "", err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", errors.New("installed runtime archive marker changed")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 66))
	if err != nil || len(raw) != 65 || raw[64] != '\n' || !digestPattern.Match(raw[:64]) {
		return "", errors.New("installed runtime archive marker is invalid")
	}
	return treeDigest(root, marker)
}

func treeDigest(root, ignoredRootFile string) (string, error) {
	var entries []treeEntry
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p == root {
			return nil
		}
		if ignoredRootFile != "" && p == filepath.Join(root, ignoredRootFile) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			return nil
		case !info.Mode().IsRegular():
			return fmt.Errorf("%s is not a regular file or directory", p)
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		sum, size, err := hashRegular(p, info)
		if err != nil {
			return err
		}
		entries = append(entries, treeEntry{path: filepath.ToSlash(rel), exec: info.Mode().Perm()&0o111 != 0, size: size, sum: sum})
		return nil
	})
	if err != nil {
		return "", err
	}
	return digestEntries(entries)
}

// maxBundleTreeFileBytes bounds one file while a bundle archive is hashed.
const maxBundleTreeFileBytes int64 = 2 << 30

// VerifyRuntimeBundle binds the served archive to an already authenticated
// manifest's runtime identity. Compiler pins are deliberately not an input:
// a published Guard runtime remains immutable when the compiler advances.
func VerifyRuntimeBundle(runtime StackKitsArchRuntime, archive io.Reader) error {
	if !digestPattern.MatchString(runtime.BundleSHA256) {
		return errors.New("the signed runtime has no valid tree digest")
	}
	digest, err := BundleTreeDigest(archive)
	if err != nil {
		return err
	}
	if digest != runtime.BundleSHA256 {
		return &Denial{CodeDigestMismatch, "the runtime bundle differs from the signed tree"}
	}
	return nil
}

// BundleTreeDigest returns the TreeDigest of a StackKits runtime bundle archive
// (a gzip tar whose members live below `.stackkit/`) without extracting it. It
// applies the same rules as the extraction the privileged executor performs, so
// it equals TreeDigest of the extracted tree.
func BundleTreeDigest(archive io.Reader) (string, error) {
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return "", err
	}
	defer func() { _ = gz.Close() }()
	reader := tar.NewReader(gz)
	var entries []treeEntry
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		name := path.Clean(header.Name)
		if name == ".stackkit" {
			continue
		}
		rel, ok := strings.CutPrefix(name, ".stackkit/")
		if !ok || rel == "" || rel == "." || strings.HasPrefix(rel, "../") || path.IsAbs(rel) {
			return "", fmt.Errorf("unsafe StackKits bundle path %q", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg, '\x00':
		default:
			return "", fmt.Errorf("unsupported StackKits bundle entry %q", header.Name)
		}
		if header.Size < 0 || header.Size > maxBundleTreeFileBytes {
			return "", fmt.Errorf("StackKits bundle entry %q has an invalid size", header.Name)
		}
		hash := sha256.New()
		size, err := io.Copy(hash, io.LimitReader(reader, header.Size+1))
		if err != nil {
			return "", err
		}
		if size != header.Size {
			return "", fmt.Errorf("StackKits bundle entry %q is truncated", header.Name)
		}
		entries = append(entries, treeEntry{path: rel, exec: header.FileInfo().Mode().Perm()&0o111 != 0, size: size, sum: hex.EncodeToString(hash.Sum(nil))})
	}
	return digestEntries(entries)
}

type treeEntry struct {
	path string
	exec bool
	size int64
	sum  string
}

func digestEntries(entries []treeEntry) (string, error) {
	if len(entries) == 0 {
		return "", errors.New("the runtime tree has no files")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	for i := 1; i < len(entries); i++ {
		if entries[i].path == entries[i-1].path {
			return "", fmt.Errorf("the runtime tree lists %q twice", entries[i].path)
		}
	}
	hash := sha256.New()
	_, _ = io.WriteString(hash, treeDigestHeader)
	for _, e := range entries {
		mode := "0644"
		if e.exec {
			mode = "0755"
		}
		// %q keeps a path with a separator character unambiguous.
		_, _ = fmt.Fprintf(hash, "file %q %s %d %s\n", e.path, mode, e.size, e.sum)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func hashRegular(path string, before fs.FileInfo) (string, int64, error) {
	file, err := os.Open(path) // #nosec G304 -- a path found by walking the caller's own tree.
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return "", 0, fmt.Errorf("%s changed while it was read", path)
	}
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}
