package jobs

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// notPersistedJobTypes are the job types deliberately absent from
// jobs_type_check, each for a recorded reason. Adding a type here is a
// decision; forgetting one is the bug this test exists to prevent.
var notPersistedJobTypes = map[JobType]string{
	JobTypeCommand:        "in-memory dispatch only; never written to the jobs table",
	JobTypeReconcileLease: "stays fail-closed until DurableReconciliationReady can truthfully return true (migration 113)",
}

// TestEveryPersistedJobTypeIsAdmittedByTheCheckConstraint closes the gap that
// broke connect-remote before migration 113: a JobType existed in Go, the
// route wrote it, and jobs_type_check rejected the insert. The failure surfaced
// as a database error rather than as the real cause, so it was diagnosed as the
// wrong problem by the wrong team.
func TestEveryPersistedJobTypeIsAdmittedByTheCheckConstraint(t *testing.T) {
	admitted := admittedJobTypes(t)

	declared := []JobType{
		JobTypeProvision, JobTypeDeploy, JobTypeDestroy, JobTypeCommand,
		JobTypeDriftCheck, JobTypeDriftResolve, JobTypeStackKitLifecycle,
		JobTypeReconcileLease, JobTypeRemoteEnrollment, JobTypeBackup,
	}

	for _, jobType := range declared {
		reason, excluded := notPersistedJobTypes[jobType]
		_, present := admitted[string(jobType)]
		switch {
		case excluded && present:
			t.Errorf("job type %q is admitted by jobs_type_check but listed as not persisted (%s); remove it from notPersistedJobTypes or from the constraint", jobType, reason)
		case !excluded && !present:
			t.Errorf("job type %q is declared in Go but missing from jobs_type_check; every insert of it fails closed with a constraint violation", jobType)
		}
	}
}

// admittedJobTypes reads the newest migration that rebuilds jobs_type_check.
func admittedJobTypes(t *testing.T) map[string]struct{} {
	t.Helper()
	dir := filepath.Join("..", "db", "migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)

	constraint := regexp.MustCompile(`(?s)ADD CONSTRAINT\s+jobs_type_check\s+CHECK\s*\(\s*type IN \((.*?)\)`)
	var newest string
	for _, name := range names {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if match := constraint.FindStringSubmatch(string(body)); match != nil {
			newest = match[1]
		}
	}
	if newest == "" {
		t.Fatal("no migration defines jobs_type_check; the constraint this test guards has moved")
	}

	admitted := map[string]struct{}{}
	for _, raw := range strings.Split(newest, ",") {
		value := strings.Trim(strings.TrimSpace(raw), "'")
		if value != "" {
			admitted[value] = struct{}{}
		}
	}
	return admitted
}
