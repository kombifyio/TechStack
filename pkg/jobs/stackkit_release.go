package jobs

import (
	"fmt"
	"os"
	"strings"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
)

const (
	stackKitReleasePinEnv   = "TECHSTACK_STACKKIT_RELEASE_PIN"
	stackKitReleaseCacheEnv = "TECHSTACK_STACKKIT_RELEASE_CACHE"
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

// ConfiguredPinnedStackKitRelease resolves and verifies the exact published
// StackKits release admitted for product-facing runtime actions.
func ConfiguredPinnedStackKitRelease() (*stackkitrelease.Release, error) {
	return configuredPinnedStackKitRelease()
}
