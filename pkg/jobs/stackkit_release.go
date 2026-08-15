package jobs

import (
	"fmt"
	"os"
	"strings"

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
// the managed Linux Agent. Windows clients carry that identity inside the
// exact bundle they serve, while Linux controllers use their normal local pin.
func configuredTargetStackKitRelease() (*stackkitrelease.Release, error) {
	release, err := configuredPinnedStackKitRelease()
	if err != nil || release != nil {
		return release, err
	}
	bundlePath := strings.TrimSpace(os.Getenv(stackKitReleaseBundleEnv))
	if bundlePath == "" {
		return nil, nil
	}
	bundled, err := stackkitrelease.ResolveLinuxRuntimeBundle(bundlePath)
	if err != nil {
		return nil, fmt.Errorf("admit bundled Linux StackKits release: %w", err)
	}
	return &bundled, nil
}

// ConfiguredPinnedStackKitRelease resolves and verifies the exact published
// StackKits release admitted for product-facing runtime actions.
func ConfiguredPinnedStackKitRelease() (*stackkitrelease.Release, error) {
	return configuredPinnedStackKitRelease()
}
