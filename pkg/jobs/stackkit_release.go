package jobs

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
)

const (
	stackKitReleasePinEnv    = "TECHSTACK_STACKKIT_RELEASE_PIN"
	stackKitReleaseCacheEnv  = "TECHSTACK_STACKKIT_RELEASE_CACHE"
	stackKitReleaseBundleEnv = "TECHSTACK_STACKKIT_RELEASE_BUNDLE"
)

func configuredPinnedStackKitRelease() (*stackkitrelease.Release, error) {
	pinPath := strings.TrimSpace(os.Getenv(stackKitReleasePinEnv))
	cacheRoot := strings.TrimSpace(os.Getenv(stackKitReleaseCacheEnv))
	if pinPath == "" && cacheRoot == "" {
		return nil, nil
	}
	if pinPath == "" || cacheRoot == "" {
		return nil, fmt.Errorf(
			"%s and %s must be configured together",
			stackKitReleasePinEnv,
			stackKitReleaseCacheEnv,
		)
	}
	release, err := (stackkitrelease.Cache{Root: cacheRoot}).ResolvePin(pinPath)
	if err != nil {
		return nil, fmt.Errorf("admit pinned published StackKits release: %w", err)
	}
	return &release, nil
}

// configuredTargetStackKitRelease resolves the release identity expected on
// the managed Linux Agent. The served bundle carries that identity independently
// of the controller's compiler pin, which may have advanced since publication.
func configuredTargetStackKitRelease() (*stackkitrelease.Release, error) {
	bundlePath := strings.TrimSpace(os.Getenv(stackKitReleaseBundleEnv))
	if bundlePath == "" {
		return configuredPinnedStackKitRelease()
	}
	bundled, err := stackkitrelease.ResolveLinuxRuntimeBundle(bundlePath)
	if err != nil {
		return nil, fmt.Errorf("admit bundled Linux StackKits release: %w", err)
	}
	return &bundled, nil
}

// ConfiguredPinnedStackKitRelease resolves and verifies the exact published
// StackKits release admitted for controller-local compilation and execution.
func ConfiguredPinnedStackKitRelease() (*stackkitrelease.Release, error) {
	return configuredPinnedStackKitRelease()
}

// ConfiguredTargetStackKitRelease resolves the exact release admitted on a
// managed Linux agent, including the bundled Windows-client runtime pin.
func ConfiguredTargetStackKitRelease() (*stackkitrelease.Release, error) {
	return configuredTargetStackKitRelease()
}

// releaseReadTTL bounds how long a read projection reuses one admitted
// release before resolving it again, even when the files look unchanged.
const releaseReadTTL = 5 * time.Minute

// releaseReadFailureTTL keeps a failed resolution from being retried on every
// read while still noticing a repaired release quickly.
const releaseReadFailureTTL = 30 * time.Second

var releaseRead struct {
	mu      sync.Mutex
	key     string
	release *stackkitrelease.Release
	err     error
	expires time.Time
}

// CachedTargetStackKitRelease is ConfiguredTargetStackKitRelease for read
// projections (inventory lists, action advertisement). Resolving the release
// gunzips the bundle and digests the StackKits executable, which costs seconds
// on a small instance; a list read does that once per file identity instead
// of once per request or per row. Execution paths keep calling
// ConfiguredTargetStackKitRelease and admit afresh.
func CachedTargetStackKitRelease() (*stackkitrelease.Release, error) {
	key := releaseFileIdentity()
	now := time.Now()
	releaseRead.mu.Lock()
	defer releaseRead.mu.Unlock()
	if releaseRead.key == key && now.Before(releaseRead.expires) {
		return releaseRead.release, releaseRead.err
	}
	release, err := configuredTargetStackKitRelease()
	ttl := releaseReadTTL
	if err != nil {
		ttl = releaseReadFailureTTL
	}
	releaseRead.key, releaseRead.release, releaseRead.err, releaseRead.expires = key, release, err, now.Add(ttl)
	return release, err
}

// releaseFileIdentity names the configured release files by path, size and
// modification time, so a replaced bundle or pin is resolved again at once.
func releaseFileIdentity() string {
	var parts []string
	for _, name := range []string{stackKitReleaseBundleEnv, stackKitReleasePinEnv, stackKitReleaseCacheEnv} {
		path := strings.TrimSpace(os.Getenv(name))
		part := name + "=" + path
		if path != "" {
			if info, err := os.Stat(path); err == nil {
				part += fmt.Sprintf("|%d|%d", info.Size(), info.ModTime().UnixNano())
			}
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, ";")
}
