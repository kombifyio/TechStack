package db

import (
	"fmt"
	"os"
	"strings"

	"github.com/kombifyio/techstack/pkg/config"
)

// EnvReleaseChannel names the release channel a deployment serves
// (CI-CD-PLATFORM-STANDARD §4.6). Only the value "preview" (Nightly) changes
// behavior; every other value, including unset, is stable.
const EnvReleaseChannel = config.EnvReleaseChannel

// ReleaseChannel returns "preview" or "stable".
func ReleaseChannel() string {
	return config.ReleaseChannel()
}

// refusePreviewMigration keeps the Nightly deployment read-only against the
// shared live schema: it runs newer code on the stable database and never
// applies migrations or genesis. Schema changes ship only through the
// governed database delivery, code first and backward compatible. Fail closed:
// the preview must be configured to verify, never to apply.
func refusePreviewMigration() error {
	if ReleaseChannel() != "preview" {
		return nil
	}
	if strings.TrimSpace(os.Getenv(EnvDatabaseMigrationMode)) != "verify" || genesisOptInFromEnv() {
		return fmt.Errorf("%s=preview refuses to migrate: set %s=verify and no genesis opt-in", EnvReleaseChannel, EnvDatabaseMigrationMode)
	}
	return nil
}
