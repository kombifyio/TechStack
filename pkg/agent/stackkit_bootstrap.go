package agent

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/privatechannel"
)

const (
	maxStackKitRuntimeBundleBytes   int64 = 4 << 30
	stackKitRuntimeBundleDigestFile       = ".techstack-bundle-sha256"
)

type StackKitRuntimeBootstrapConfig struct {
	URL, AgentToken, RuntimeAgentID, TenantID string
	HTTPClient                                *http.Client
	PrivateLANHTTPOrigin                      string
}

// EnsureStackKitRuntime installs only the control-plane-published verified
// release bundle. The typed command still revalidates every cached artifact.
func EnsureStackKitRuntime(ctx context.Context, cfg StackKitRuntimeBootstrapConfig) error {
	executor := NewStackKitExecutorFromEnv()
	return executor.EnsureRuntime(ctx, cfg)
}

// EnsureRuntime converges an enrolled Agent to the exact bundle currently
// published by its authenticated Techstack control plane. A locally valid but
// older release is not sufficient evidence that the Agent is current.
func (executor *StackKitExecutor) EnsureRuntime(ctx context.Context, cfg StackKitRuntimeBootstrapConfig) error {
	if executor == nil {
		return fmt.Errorf("StackKits executor is required")
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if !executor.Available() {
		return fmt.Errorf("StackKits runtime paths are not configured")
	}
	runtimeRoot := filepath.Dir(filepath.Clean(executor.pinPath))
	if strings.TrimSpace(cfg.URL) == "" {
		return fmt.Errorf("StackKits release URL is required")
	}
	parsedURL, err := url.Parse(cfg.URL)
	if err != nil || parsedURL.Hostname() == "" || (parsedURL.Scheme != "https" && !(parsedURL.Scheme == "http" && isLoopbackHost(parsedURL.Hostname())) && !privatechannel.MatchesLANOrigin(cfg.URL, cfg.PrivateLANHTTPOrigin)) {
		return fmt.Errorf("StackKits release URL must use HTTPS or loopback HTTP")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(cfg.AgentToken))
	request.Header.Set("X-Kombify-Runtime-Agent-ID", strings.TrimSpace(cfg.RuntimeAgentID))
	request.Header.Set("X-Kombify-Tenant-ID", strings.TrimSpace(cfg.TenantID))
	if digest, readErr := readInstalledStackKitBundleDigest(runtimeRoot); readErr == nil {
		request.Header.Set("If-None-Match", `"`+digest+`"`)
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotModified {
		if err := executor.validate(); err != nil {
			return fmt.Errorf("control plane reports current StackKits bundle but local runtime is invalid: %w", err)
		}
		return nil
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("StackKits release download returned HTTP %d", response.StatusCode)
	}
	expected := strings.ToLower(strings.TrimSpace(response.Header.Get("X-Kombify-Artifact-SHA256")))
	if len(expected) != sha256.Size*2 {
		return fmt.Errorf("StackKits release checksum is missing")
	}
	tmp, err := os.CreateTemp("", "techstack-stackkit-release-*.tar.gz")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(response.Body, maxStackKitRuntimeBundleBytes+1))
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if written > maxStackKitRuntimeBundleBytes {
		return fmt.Errorf("StackKits release bundle exceeds size limit")
	}
	if hex.EncodeToString(hash.Sum(nil)) != expected {
		return fmt.Errorf("StackKits release bundle checksum mismatch")
	}
	if err := installStackKitRuntimeBundleValidated(tmpPath, runtimeRoot, expected, executor.validate); err != nil {
		return err
	}
	return nil
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(strings.ToLower(host))
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func installStackKitRuntimeBundle(bundlePath, targetRoot string, suppliedBundleDigest ...string) error {
	bundleDigest := ""
	if len(suppliedBundleDigest) > 0 {
		bundleDigest = suppliedBundleDigest[0]
	} else {
		file, err := os.Open(bundlePath)
		if err != nil {
			return err
		}
		hash := sha256.New()
		if _, err := io.Copy(hash, io.LimitReader(file, maxStackKitRuntimeBundleBytes+1)); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		bundleDigest = hex.EncodeToString(hash.Sum(nil))
	}
	return installStackKitRuntimeBundleValidated(bundlePath, targetRoot, bundleDigest, nil)
}

func installStackKitRuntimeBundleValidated(bundlePath, targetRoot, bundleDigest string, validate func() error) error {
	parent := filepath.Dir(targetRoot)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(parent, ".stackkit-stage-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	file, err := os.Open(bundlePath)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	for {
		header, nextErr := reader.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return nextErr
		}
		name := filepath.Clean(filepath.FromSlash(header.Name))
		if name == ".stackkit" {
			continue
		}
		prefix := ".stackkit" + string(filepath.Separator)
		if !strings.HasPrefix(name, prefix) {
			return fmt.Errorf("unsafe StackKits bundle path %q", header.Name)
		}
		rel := strings.TrimPrefix(name, prefix)
		if rel == "" || rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("unsafe StackKits bundle path %q", header.Name)
		}
		destination := filepath.Join(stage, rel)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(destination, 0755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
				return err
			}
			out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(header.Mode)&0755)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, io.LimitReader(reader, header.Size+1))
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("unsupported StackKits bundle entry %q", header.Name)
		}
	}
	if err := os.WriteFile(filepath.Join(stage, stackKitRuntimeBundleDigestFile), []byte(bundleDigest+"\n"), 0600); err != nil {
		return err
	}
	backup := targetRoot + ".previous"
	_ = os.RemoveAll(backup)
	if _, err := os.Stat(targetRoot); err == nil {
		if err := os.Rename(targetRoot, backup); err != nil {
			return err
		}
	}
	if err := os.Rename(stage, targetRoot); err != nil {
		_ = os.Rename(backup, targetRoot)
		return err
	}
	if validate != nil {
		if err := validate(); err != nil {
			_ = os.RemoveAll(targetRoot)
			_ = os.Rename(backup, targetRoot)
			return fmt.Errorf("validate installed StackKits runtime: %w", err)
		}
	}
	_ = os.RemoveAll(backup)
	return nil
}

func readInstalledStackKitBundleDigest(targetRoot string) (string, error) {
	data, err := os.ReadFile(filepath.Join(targetRoot, stackKitRuntimeBundleDigestFile))
	if err != nil {
		return "", err
	}
	digest := strings.ToLower(strings.TrimSpace(string(data)))
	if len(digest) != sha256.Size*2 {
		return "", fmt.Errorf("installed StackKits bundle digest is invalid")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return "", fmt.Errorf("installed StackKits bundle digest is invalid")
	}
	return digest, nil
}
