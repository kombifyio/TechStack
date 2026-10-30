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

// refusePreviewMigration keeps secondary serving processes verify-only with
// genesis disabled. Preview uses the non-production Stage database, migrated
// first by the governed exact-source main path; Early Access migrations remain
// owned by the release path. Neither serving process holds migration custody.
func refusePreviewMigration() error {
	if !config.SecondaryChannel() {
		return nil
	}
	if strings.TrimSpace(os.Getenv(EnvDatabaseMigrationMode)) != "verify" || genesisOptInFromEnv() {
		return fmt.Errorf("%s=%s refuses to migrate: set %s=verify and no genesis opt-in", EnvReleaseChannel, ReleaseChannel(), EnvDatabaseMigrationMode)
	}
	return nil
}
