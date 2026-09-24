package controlplane

import (
	"regexp"

	"github.com/DATA-DOG/go-sqlmock"
)

func expectTenantGUC(mock sqlmock.Sqlmock, tenantID string) {
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config($1, $2, true)")).
		WithArgs("app.tenant_id", tenantID).
		WillReturnResult(sqlmock.NewResult(0, 1))
}
