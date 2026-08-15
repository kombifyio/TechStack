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
)

func digestArchiveExecutable(
	archivePath string,
	archiveName string,
	platform Platform,
) (fileDigest, error) {
	file, info, err := openRegularFile(archivePath, maxReleaseBlobBytes)
	if err != nil {
		return fileDigest{}, err
	}
	defer file.Close()

	if strings.HasSuffix(strings.ToLower(archiveName), ".zip") {
		reader, err := zip.NewReader(file, info.Size())
		if err != nil {
			return fileDigest{}, err
		}
		result, err := digestZipExecutable(reader, platform)
		if err != nil {
			return fileDigest{}, err
		}
		if err := ensureUnchanged(file, info); err != nil {
			return fileDigest{}, err
		}
		return result, nil
	}

	var stream io.Reader = file
	var gzipReader *gzip.Reader
	lowerName := strings.ToLower(archiveName)
	if strings.HasSuffix(lowerName, ".gz") || strings.HasSuffix(lowerName, ".tgz") {
		gzipReader, err = gzip.NewReader(stream)
		if err != nil {
			return fileDigest{}, err
		}
		defer gzipReader.Close()
		stream = gzipReader
	}
	result, err := digestTarExecutable(tar.NewReader(stream), platform)
	if err != nil {
		return fileDigest{}, err
	}
	if err := ensureUnchanged(file, info); err != nil {
		return fileDigest{}, err
	}
	return result, nil
}

func digestZipExecutable(reader *zip.Reader, platform Platform) (fileDigest, error) {
	if len(reader.File) > maxArchiveFiles {
		return fileDigest{}, fmt.Errorf("archive exceeds %d entries", maxArchiveFiles)
	}
	canonical := executableName(platform)
	seen := make(map[string]struct{}, len(reader.File))
	var result fileDigest
	var total uint64
	found := false
	for _, entry := range reader.File {
		relative, err := safeArchivePath(entry.Name, entry.FileInfo().IsDir())
		if err != nil {
			return fileDigest{}, err
		}
		key := strings.ToLower(relative)
		if _, exists := seen[key]; exists {
			return fileDigest{}, fmt.Errorf("duplicate archive path %q", entry.Name)
		}
		seen[key] = struct{}{}
		mode := entry.Mode()
		if mode&os.ModeSymlink != 0 || (!mode.IsRegular() && !mode.IsDir()) {
			return fileDigest{}, fmt.Errorf("archive entry %q has a forbidden type", entry.Name)
		}
		if entry.UncompressedSize64 > uint64(maxArchiveExtractedBytes) ||
			total > uint64(maxArchiveExtractedBytes)-entry.UncompressedSize64 {
			return fileDigest{}, fmt.Errorf("archive exceeds %d uncompressed bytes", maxArchiveExtractedBytes)
		}
		total += entry.UncompressedSize64
		if relative != canonical {
			continue
		}
		if found || !mode.IsRegular() {
			return fileDigest{}, fmt.Errorf("canonical executable %q must be one regular file", canonical)
		}
		opened, err := entry.Open()
		if err != nil {
			return fileDigest{}, err
		}
		result, err = digestArchiveReader(opened, int64(entry.UncompressedSize64))
		closeErr := opened.Close()
		if err != nil {
			return fileDigest{}, err
		}
		if closeErr != nil {
			return fileDigest{}, closeErr
		}
		found = true
	}
	if !found {
		return fileDigest{}, fmt.Errorf("canonical executable %q is missing", canonical)
	}
	return result, nil
}

func digestTarExecutable(reader *tar.Reader, platform Platform) (fileDigest, error) {
	canonical := executableName(platform)
	seen := map[string]struct{}{}
	var result fileDigest
	var total int64
	found := false
	for count := 0; ; count++ {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fileDigest{}, err
		}
		if count >= maxArchiveFiles {
			return fileDigest{}, fmt.Errorf("archive exceeds %d entries", maxArchiveFiles)
		}
		isDirectory := header.Typeflag == tar.TypeDir
		relative, err := safeArchivePath(header.Name, isDirectory)
		if err != nil {
			return fileDigest{}, err
		}
		key := strings.ToLower(relative)
		if _, exists := seen[key]; exists {
			return fileDigest{}, fmt.Errorf("duplicate archive path %q", header.Name)
		}
		seen[key] = struct{}{}
		switch header.Typeflag {
		case tar.TypeDir:
			if header.Size != 0 {
				return fileDigest{}, fmt.Errorf("archive directory entry %q must have zero size", header.Name)
			}
			continue
		case tar.TypeReg, tar.TypeRegA:
		default:
			return fileDigest{}, fmt.Errorf("archive entry %q has a forbidden type", header.Name)
		}
		if header.Size < 0 || header.Size > maxArchiveExtractedBytes-total {
			return fileDigest{}, fmt.Errorf("archive exceeds %d uncompressed bytes", maxArchiveExtractedBytes)
		}
		total += header.Size
		if relative != canonical {
			continue
		}
		if found {
			return fileDigest{}, fmt.Errorf("canonical executable %q is duplicated", canonical)
		}
		result, err = digestArchiveReader(reader, header.Size)
		if err != nil {
			return fileDigest{}, err
		}
		found = true
	}
	if !found {
		return fileDigest{}, fmt.Errorf("canonical executable %q is missing", canonical)
	}
	return result, nil
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
