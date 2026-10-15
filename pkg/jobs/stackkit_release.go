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
