package stackkitrelease

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"strings"
)

const (
	linuxRuntimePinPath    = ".stackkit/stackkits-release-pin.json"
	linuxRuntimeBinaryPath = ".stackkit/bin/stackkit"
	// linuxRuntimeCatalogPath is the Advanced operations catalog the image
	// build places beside the pin; absent for releases before the catalog.
	linuxRuntimeCatalogPath = ".stackkit/" + AdvancedOperationsFileName
)

// ResolveLinuxRuntimeBundle returns the immutable release identity carried by
// the exact Linux bundle that the Windows control plane serves to its Agent.
// The controller cannot execute that Linux binary, so this path verifies the
// inner pin against the bundled binary instead of comparing it to GOOS.
func ResolveLinuxRuntimeBundle(bundlePath string) (Release, error) {
	return resolveLinuxRuntimeBundle(bundlePath, nil)
}
func resolveLinuxRuntimeBundle(bundlePath string, admit func(Pin) error) (Release, error) {
	file, info, err := openRegularFile(bundlePath, maxReleaseBlobBytes)
	if err != nil {
		return Release{}, fmt.Errorf("open StackKits Linux runtime bundle: %w", err)
	}
	defer file.Close()
	release, err := resolveLinuxRuntimeBundleReader(bundlePath, file, admit)
	if err != nil {
		return Release{}, err
	}
	if err := ensureUnchanged(file, info); err != nil {
		return Release{}, err
	}
	return release, nil
}

// The Stage candidate path parses this same stream after signature verification;
// it never reopens a pathname to obtain a different pin or operations catalog.
func resolveLinuxRuntimeBundleReader(bundlePath string, input io.Reader, admit func(Pin) error) (Release, error) {
	gz, err := gzip.NewReader(input)
	if err != nil {
		return Release{}, fmt.Errorf("open StackKits Linux runtime bundle gzip: %w", err)
	}
	defer gz.Close()

	reader := tar.NewReader(gz)
	var pinData []byte
	var catalog []byte
	var candidateCatalog, candidateIndex []byte
	var binary fileDigest
	var binaryFound bool
	var total int64
	seen := map[string]struct{}{}
	for count := 0; ; count++ {
		header, nextErr := reader.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return Release{}, fmt.Errorf("read StackKits Linux runtime bundle: %w", nextErr)
		}
		if count >= maxArchiveFiles {
			return Release{}, fmt.Errorf("StackKits Linux runtime bundle exceeds %d entries", maxArchiveFiles)
		}
		isDirectory := header.Typeflag == tar.TypeDir
		name, pathErr := safeArchivePath(header.Name, isDirectory)
		if pathErr != nil {
			return Release{}, pathErr
		}
		key := strings.ToLower(name)
		if _, exists := seen[key]; exists {
			return Release{}, fmt.Errorf("duplicate archive path %q", header.Name)
		}
		seen[key] = struct{}{}
		switch header.Typeflag {
		case tar.TypeDir:
			if header.Size != 0 {
				return Release{}, fmt.Errorf("archive directory entry %q must have zero size", header.Name)
			}
			continue
		case tar.TypeReg, legacyTarRegularFileType:
		default:
			return Release{}, fmt.Errorf("archive entry %q has a forbidden type", header.Name)
		}
		if header.Size < 0 || header.Size > maxArchiveExtractedBytes-total {
			return Release{}, fmt.Errorf("StackKits Linux runtime bundle exceeds %d uncompressed bytes", maxArchiveExtractedBytes)
		}
		total += header.Size
		switch name {
		case linuxRuntimePinPath:
			if header.Size <= 0 || header.Size > maxPinBytes {
				return Release{}, fmt.Errorf("StackKits Linux runtime pin must be bounded and non-empty")
			}
			pinData, err = io.ReadAll(io.LimitReader(reader, header.Size+1))
			if err != nil || int64(len(pinData)) != header.Size {
				return Release{}, fmt.Errorf("read StackKits Linux runtime pin: %w", err)
			}
		case linuxRuntimeCatalogPath:
			if catalog, err = readArchiveCatalog(reader, header.Size); err != nil {
				return Release{}, fmt.Errorf("read StackKits Linux runtime Advanced operations catalog: %w", err)
			}
		case ".stackkit/candidate-source/stackkits-preview-use-case-catalog-v1.json":
			if candidateCatalog, err = readArchiveCatalog(reader, header.Size); err != nil {
				return Release{}, err
			}
		case ".stackkit/candidate-source/stackkits-preview-candidate-index-v1.json":
			if candidateIndex, err = readArchiveCatalog(reader, header.Size); err != nil {
				return Release{}, err
			}
		case linuxRuntimeBinaryPath:
			binary, err = digestArchiveReader(reader, header.Size)
			if err != nil {
				return Release{}, fmt.Errorf("digest StackKits Linux runtime binary: %w", err)
			}
			binaryFound = true
		}
	}
	if len(pinData) == 0 || !binaryFound {
		return Release{}, fmt.Errorf("StackKits Linux runtime bundle is missing its pin or executable")
	}
	var pin Pin
	if err := decodeStrict(pinData, &pin); err != nil {
		return Release{}, fmt.Errorf("decode StackKits Linux runtime pin: %w", err)
	}
	expected := Platform{OS: "linux", Arch: "amd64"}
	if err := pin.validateForPlatform(expected); err != nil {
		return Release{}, err
	}
	if pin.SchemaVersion == CandidatePinSchemaVersion {
		if admit == nil {
			return Release{}, fmt.Errorf("internal candidate bundle requires typed Stage admission")
		}
		if err := admit(pin); err != nil {
			return Release{}, err
		}
	} else if pin.SchemaVersion != PinSchemaVersion {
		return Release{}, fmt.Errorf("unsupported public runtime pin")
	}
	if pin.BinaryPath != "/app/.stackkit/bin/stackkit" {
		return Release{}, fmt.Errorf("StackKits Linux runtime bundle has an unsupported artifact pin")
	}
	if binary.sha256 != pin.BinarySHA256 {
		return Release{}, fmt.Errorf("StackKits Linux runtime binary differs from its immutable artifact pin")
	}
	channel := channelForVersion(pin.Version)
	if pin.Candidate != nil {
		channel = "internal-preview"
	}
	var useCases *UseCaseCatalog
	if pin.Candidate != nil {
		decoded, err := decodeCandidateUseCaseCatalog(candidateCatalog, candidateIndex, pin)
		if err != nil {
			return Release{}, err
		}
		useCases = &decoded
	}
	return Release{
		useCaseCatalog:     useCases,
		advancedOperations: catalog,
		binaryPath:         linuxRuntimeBinaryPath,
		receiptPath:        bundlePath,
		receipt: Receipt{
			Candidate:     pin.Candidate,
			SchemaVersion: pin.SchemaVersion,
			Kit:           pin.Kit,
			Version:       pin.Version,
			Channel:       channel,
			Platform:      pin.Platform,
			ArchiveSHA256: pin.ArchiveSHA256,
			IndexSHA256:   pin.IndexSHA256,
		},
	}, nil
}
