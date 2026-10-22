package db

import (
	"fmt"
	"os"
	"strings"

	"github.com/kombifyio/techstack/pkg/config"
)

// EnvReleaseChannel names the release channel a deployment serves
// (CI-CD-PLATFORM-STANDARD §4.6). "preview" (Preview) and "early-access"
// (Early Access) change behavior; every other value, including unset, is
// stable.
const EnvReleaseChannel = config.EnvReleaseChannel

// ReleaseChannel returns "preview", "early-access" or "stable".
func ReleaseChannel() string {
	return config.ReleaseChannel()
}

// refusePreviewMigration keeps the Preview and Early Access deployments
// read-only against the shared live schema: they run newer code on the stable
// database and never apply migrations or genesis. Schema changes ship only
// through the governed database delivery, code first and backward compatible.
// Fail closed: both must be configured to verify, never to apply.
func refusePreviewMigration() error {
	if !config.SecondaryChannel() {
		return nil
	}
	if strings.TrimSpace(os.Getenv(EnvDatabaseMigrationMode)) != "verify" || genesisOptInFromEnv() {
		return fmt.Errorf("%s=%s refuses to migrate: set %s=verify and no genesis opt-in", EnvReleaseChannel, ReleaseChannel(), EnvDatabaseMigrationMode)
	}
	return nil
}
