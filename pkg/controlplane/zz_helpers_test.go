package controlplane

// Helpers of retired unit tests that the remaining boundary tests of this package still use.

import (
	"regexp"
	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

func expectTenantGUC(mock sqlmock.Sqlmock, tenantID string) {
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config($1, $2, true)")).
		WithArgs("app.tenant_id", tenantID).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func workerRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "instance_id", "stack_id", "hostname", "ip", "os", "arch", "token_hash", "status",
		"approved", "approved_at", "last_seen_at", "cpu_cores", "ram_mb", "disk_gb", "gpu", "has_nvme",
		"has_hw_transcode", "docker_version", "type", "provider", "tags_json", "owner_subject_id",
		"capabilities_json", "resources_json", "created_at", "updated_at",
	})
}

