package guardrelease

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// NewConfiguredVerifier uses the same host-owned Sigstore root and signer
// identity for release routes, refresh, and managed-target metadata.
func NewConfiguredVerifier(now func() time.Time) (*Verifier, error) {
	options := Options{Now: now}
	if path := strings.TrimSpace(os.Getenv("TECHSTACK_GUARD_RELEASE_TRUSTED_ROOT")); path != "" {
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("read configured Guard release trusted root: %w", err)
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return nil, errors.New("configured Guard release trusted root is not a bounded regular file")
		}
		root, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
		if err != nil {
			return nil, fmt.Errorf("read configured Guard release trusted root: %w", err)
		}
		if len(root) > 1<<20 {
			return nil, errors.New("configured Guard release trusted root exceeds its bound")
		}
		options.TrustedRootJSON = root
	}
	issuer, san := strings.TrimSpace(os.Getenv("TECHSTACK_GUARD_RELEASE_SIGNER_ISSUER")), strings.TrimSpace(os.Getenv("TECHSTACK_GUARD_RELEASE_SIGNER_SAN_REGEXP"))
	if issuer != "" || san != "" {
		options.Identity = Identity{Issuer: issuer, SANRegexp: san}
	}
	return NewVerifier(options)
}

var observedPolicy struct {
	sync.RWMutex
	manifest *Manifest
}

// ObserveVerifiedPolicy shares an authenticated, newer release policy with
// runtime-target admission. The route refresher calls this only after its
// monotonic and no-policy-rollback checks have succeeded.
func ObserveVerifiedPolicy(raw, bundle []byte, verifier interface {
	VerifyManifest([]byte, []byte) (*Manifest, error)
}) error {
	if verifier == nil {
		return errors.New("Guard release policy verifier is required")
	}
	manifest, err := verifier.VerifyManifest(raw, bundle)
	if err != nil {
		return err
	}
	observedPolicy.Lock()
	if observedPolicy.manifest == nil || manifest.IssuedAt.After(observedPolicy.manifest.IssuedAt) {
		observedPolicy.manifest = manifest
	}
	observedPolicy.Unlock()
	return nil
}

// LatestObservedPolicy returns only an in-process policy previously verified
// with the pinned Guard identity. It cannot downgrade an image-baked policy.
func LatestObservedPolicy() *Manifest {
	observedPolicy.RLock()
	defer observedPolicy.RUnlock()
	return observedPolicy.manifest
}
