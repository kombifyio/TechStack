package localdb

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
)

// ensureDataDirCompatible refuses to start over data this build cannot use.
// The embedded-postgres library silently deletes and re-initializes a data
// directory whose PG_VERSION differs from the bundled major (a downgrade after
// a newer client, or an unrecognized directory), which would destroy the
// owner's local state. A missing or empty directory is a fresh install.
func ensureDataDirCompatible(dataPath string) error {
	entries, err := os.ReadDir(dataPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect local database directory %s: %w", dataPath, err)
	}
	if len(entries) == 0 {
		return nil
	}
	want := strings.TrimSpace(string(embeddedpostgres.V16))
	raw, err := os.ReadFile(filepath.Join(dataPath, "PG_VERSION"))
	if err != nil {
		return fmt.Errorf("local database directory %s is not a recognized PostgreSQL %s data directory (PG_VERSION unreadable: %v); nothing was changed, restore a copy of the state directory or move this one aside", dataPath, want, err)
	}
	have := strings.TrimSpace(string(raw))
	if have != strings.SplitN(want, ".", 2)[0] {
		return fmt.Errorf("local database in %s was written by PostgreSQL %s but this build bundles %s; nothing was changed, install the newer kombify Techstack client again or restore a copy of the state directory", dataPath, have, want)
	}
	return nil
}
