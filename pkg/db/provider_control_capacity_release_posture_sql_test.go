package db

import (
	"strings"
	"testing"
)

func TestCapacityReleaseValidatorPostureRepairIsAdditiveAndExact(t *testing.T) {
	content := readDBFile(t, "migrations/069_reharden_capacity_release_validator.sql")
	for _, required := range []string{
		"active_schema text := current_schema()",
		"ALTER FUNCTION %I.managed_runtime_capacity_release_validate_insert() SECURITY DEFINER",
		"ALTER FUNCTION %I.managed_runtime_capacity_release_validate_insert() SET search_path TO pg_catalog, %I, pg_temp",
		"REVOKE ALL ON FUNCTION %I.managed_runtime_capacity_release_validate_insert() FROM PUBLIC",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("capacity release validator posture repair missing %q", required)
		}
	}

	for _, forbidden := range []string{
		"CREATE OR REPLACE FUNCTION",
		"GRANT EXECUTE",
		"SET search_path FROM CURRENT",
	} {
		if strings.Contains(strings.ToUpper(content), strings.ToUpper(forbidden)) {
			t.Fatalf("capacity release validator posture repair broadens behavior via %q", forbidden)
		}
	}
}
