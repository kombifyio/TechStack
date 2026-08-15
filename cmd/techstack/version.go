package main

import (
	"regexp"
	"strings"
	"time"
)

// version is the product version shown in public UI and health/info responses.
// buildRevision is the deployment revision used by deploy smoke checks and
// operational correlation. Keep them separate so production never presents a
// raw git SHA as the product version.
const developmentBuildRevision = "dev"

var version = defaultProductVersion
var buildRevision = developmentBuildRevision
var startTime = time.Now()

var productVersionPattern = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
var fullBuildRevisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func init() {
	version = compiledProductVersion(version)
	buildRevision = compiledBuildRevision(buildRevision)
}

// compiledProductVersion deliberately ignores runtime environment variables.
// VERSION and the linker-injected main.version identify the reviewed artifact;
// allowing a running container to override them would make old code appear to
// be a newer product release.
func compiledProductVersion(value string) string {
	if normalized := normalizeProductVersion(value); normalized != "" {
		return normalized
	}
	return defaultProductVersion
}

// compiledBuildRevision deliberately ignores runtime environment variables.
// A deployed revision must identify the linked artifact, never mutable service
// configuration. Development builds retain "dev", which public APIs omit.
func compiledBuildRevision(value string) string {
	revision := strings.ToLower(strings.TrimSpace(value))
	if fullBuildRevisionPattern.MatchString(revision) {
		return revision
	}
	return developmentBuildRevision
}

func normalizeProductVersion(value string) string {
	value = strings.TrimSpace(value)
	switch strings.ToLower(value) {
	case "", "null", "undefined":
		return ""
	}
	if len(value) > 1 && value[0] == 'v' && value[1] >= '0' && value[1] <= '9' {
		value = value[1:]
	}
	if isLikelyGitRevision(value) {
		return ""
	}
	if !productVersionPattern.MatchString(value) {
		return ""
	}
	return value
}

func isLikelyGitRevision(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 7 {
		return false
	}
	for _, r := range value {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') {
			continue
		}
		return false
	}
	return true
}
