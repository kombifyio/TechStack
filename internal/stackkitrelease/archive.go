package stackkitrelease

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	maxArchiveFiles          = 20_000
	maxArchiveExtractedBytes = int64(1 << 30)
	legacyTarRegularFileType = byte(0) // NUL is the legacy regular-file type flag.
)

// digestArchiveExecutable digests the canonical executable of a verified
// release archive and extracts its stackkit.advanced-operations/v1 catalog,
// which is nil when the release predates the catalog.
func digestArchiveExecutable(
	archivePath string,
	archiveName string,
	platform Platform,
) (fileDigest, []byte, error) {
	file, info, err := openRegularFile(archivePath, maxReleaseBlobBytes)
	if err != nil {
		return fileDigest{}, nil, err
	}
	defer file.Close()

	if strings.HasSuffix(strings.ToLower(archiveName), ".zip") {
		reader, err := zip.NewReader(file, info.Size())
		if err != nil {
			return fileDigest{}, nil, err
		}
		result, catalog, err := digestZipExecutable(reader, platform)
		if err != nil {
			return fileDigest{}, nil, err
		}
		if err := ensureUnchanged(file, info); err != nil {
			return fileDigest{}, nil, err
		}
		return result, catalog, nil
	}

	var stream io.Reader = file
	var gzipReader *gzip.Reader
	lowerName := strings.ToLower(archiveName)
	if strings.HasSuffix(lowerName, ".gz") || strings.HasSuffix(lowerName, ".tgz") {
		gzipReader, err = gzip.NewReader(stream)
		if err != nil {
			return fileDigest{}, nil, err
		}
		defer gzipReader.Close()
		stream = gzipReader
	}
	result, catalog, err := digestTarExecutable(tar.NewReader(stream), platform)
	if err != nil {
		return fileDigest{}, nil, err
	}
	if err := ensureUnchanged(file, info); err != nil {
		return fileDigest{}, nil, err
	}
	return result, catalog, nil
}

// readZipMember opens one zip entry, reads it with read and closes it.
func readZipMember[T any](entry *zip.File, read func(io.Reader, int64) (T, error)) (T, error) {
	var zero T
	opened, err := entry.Open()
	if err != nil {
		return zero, err
	}
	value, readErr := read(opened, int64(entry.UncompressedSize64))
	closeErr := opened.Close()
	if readErr != nil {
		return zero, readErr
	}
	if closeErr != nil {
		return zero, closeErr
	}
	return value, nil
}

// readArchiveCatalog reads the bounded Advanced operations catalog member.
func readArchiveCatalog(reader io.Reader, size int64) ([]byte, error) {
	if size <= 0 || size > maxAdvancedOperationsBytes {
		return nil, fmt.Errorf("archive %s must be a bounded non-empty file", AdvancedOperationsArchivePath)
	}
	data, err := io.ReadAll(io.LimitReader(reader, size+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != size {
		return nil, fmt.Errorf("archive %s changed or was truncated while read", AdvancedOperationsArchivePath)
	}
	return data, nil
}

func digestZipExecutable(reader *zip.Reader, platform Platform) (fileDigest, []byte, error) {
	if len(reader.File) > maxArchiveFiles {
		return fileDigest{}, nil, fmt.Errorf("archive exceeds %d entries", maxArchiveFiles)
	}
	canonical := executableName(platform)
	seen := make(map[string]struct{}, len(reader.File))
	var result fileDigest
	var catalog []byte
	var total uint64
	found := false
	for _, entry := range reader.File {
		relative, err := admitZipEntry(entry, seen, total)
		if err != nil {
			return fileDigest{}, nil, err
		}
		mode := entry.Mode()
		total += entry.UncompressedSize64
		if relative != canonical {
			if relative == AdvancedOperationsArchivePath && mode.IsRegular() {
				catalog, err = readZipMember(entry, readArchiveCatalog)
			}
			if err != nil {
				return fileDigest{}, nil, err
			}
			continue
		}
		if found || !mode.IsRegular() {
			return fileDigest{}, nil, fmt.Errorf("canonical executable %q must be one regular file", canonical)
		}
		if result, err = readZipMember(entry, digestArchiveReader); err != nil {
			return fileDigest{}, nil, err
		}
		found = true
	}
	if !found {
		return fileDigest{}, nil, fmt.Errorf("canonical executable %q is missing", canonical)
	}
	return result, catalog, nil
}

func digestTarExecutable(reader *tar.Reader, platform Platform) (fileDigest, []byte, error) {
	canonical := executableName(platform)
	seen := map[string]struct{}{}
	var result fileDigest
	var catalog []byte
	var total int64
	found := false
	for count := 0; ; count++ {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fileDigest{}, nil, err
		}
		if count >= maxArchiveFiles {
			return fileDigest{}, nil, fmt.Errorf("archive exceeds %d entries", maxArchiveFiles)
		}
		relative, regular, err := admitTarEntry(header, seen, total)
		if err != nil {
			return fileDigest{}, nil, err
		}
		if !regular {
			continue
		}
		total += header.Size
		if relative != canonical {
			if relative == AdvancedOperationsArchivePath {
				catalog, err = readArchiveCatalog(reader, header.Size)
			}
			if err != nil {
				return fileDigest{}, nil, err
			}
			continue
		}
		if found {
			return fileDigest{}, nil, fmt.Errorf("canonical executable %q is duplicated", canonical)
		}
		result, err = digestArchiveReader(reader, header.Size)
		if err != nil {
			return fileDigest{}, nil, err
		}
		found = true
	}
	if !found {
		return fileDigest{}, nil, fmt.Errorf("canonical executable %q is missing", canonical)
	}
	return result, catalog, nil
}

// admitZipEntry validates one zip entry: a safe unique path, a regular file
// or directory, within the extracted-size budget.
func admitZipEntry(entry *zip.File, seen map[string]struct{}, total uint64) (string, error) {
	relative, err := safeArchivePath(entry.Name, entry.FileInfo().IsDir())
	if err != nil {
		return "", err
	}
	key := strings.ToLower(relative)
	if _, exists := seen[key]; exists {
		return "", fmt.Errorf("duplicate archive path %q", entry.Name)
	}
	seen[key] = struct{}{}
	mode := entry.Mode()
	if mode&os.ModeSymlink != 0 || (!mode.IsRegular() && !mode.IsDir()) {
		return "", fmt.Errorf("archive entry %q has a forbidden type", entry.Name)
	}
	if entry.UncompressedSize64 > uint64(maxArchiveExtractedBytes) ||
		total > uint64(maxArchiveExtractedBytes)-entry.UncompressedSize64 {
		return "", fmt.Errorf("archive exceeds %d uncompressed bytes", maxArchiveExtractedBytes)
	}
	return relative, nil
}

// admitTarEntry validates one tar header: a safe unique path, a directory of
// zero size or a regular file within the extracted-size budget. It reports
// whether the entry is a regular file.
func admitTarEntry(header *tar.Header, seen map[string]struct{}, total int64) (string, bool, error) {
	relative, err := safeArchivePath(header.Name, header.Typeflag == tar.TypeDir)
	if err != nil {
		return "", false, err
	}
	key := strings.ToLower(relative)
	if _, exists := seen[key]; exists {
		return "", false, fmt.Errorf("duplicate archive path %q", header.Name)
	}
	seen[key] = struct{}{}
	switch header.Typeflag {
	case tar.TypeDir:
		if header.Size != 0 {
			return "", false, fmt.Errorf("archive directory entry %q must have zero size", header.Name)
		}
		return relative, false, nil
	case tar.TypeReg, legacyTarRegularFileType:
	default:
		return "", false, fmt.Errorf("archive entry %q has a forbidden type", header.Name)
	}
	if header.Size < 0 || header.Size > maxArchiveExtractedBytes-total {
		return "", false, fmt.Errorf("archive exceeds %d uncompressed bytes", maxArchiveExtractedBytes)
	}
	return relative, true, nil
}

func digestArchiveReader(reader io.Reader, size int64) (fileDigest, error) {
	if size <= 0 || size > maxExecutableBytes {
		return fileDigest{}, fmt.Errorf("canonical executable must be a bounded non-empty file")
	}
	digest := sha256.New()
	written, err := io.Copy(digest, io.LimitReader(reader, size+1))
	if err != nil {
		return fileDigest{}, err
	}
	if written != size {
		return fileDigest{}, fmt.Errorf("canonical executable changed or was truncated while read")
	}
	return fileDigest{sha256: hex.EncodeToString(digest.Sum(nil)), size: written}, nil
}

func executableName(platform Platform) string {
	if platform.OS == "windows" {
		return "stackkit.exe"
	}
	return "stackkit"
}

func safeArchivePath(name string, directory bool) (string, error) {
	if directory {
		name = strings.TrimRight(name, "/")
	}
	if name == "" || strings.ContainsRune(name, 0) || strings.Contains(name, `\`) ||
		path.IsAbs(name) || filepath.IsAbs(name) {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") ||
		clean != name || (len(clean) > 1 && clean[1] == ':') {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	return clean, nil
}
