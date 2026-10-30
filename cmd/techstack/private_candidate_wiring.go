package main

import (
	"database/sql"

	"github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/jobs"
)

// This follows the real Stage preparation in each native composition path.
// It introduces no owner switch, alternate database pool or admission writer.
func preparePrivateCandidateRelease(binding *db.StagingRuntimeBinding, database *sql.DB, expectedTenant string) error {
	return jobs.PrepareStageCandidateRelease(binding, database, expectedTenant)
}
