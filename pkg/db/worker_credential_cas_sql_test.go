package db

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkerCredentialCASAndSatelliteSQLPreserveReservedState(t *testing.T) {
	t.Parallel()

	postgresSource := readWorkerCredentialSource(t, "postgres_store.go")
	guardSource := readWorkerCredentialSource(t, "guard_inventory_satellites_postgres.go")

	casStart := strings.Index(postgresSource, "func (s *PostgresStore) CompareAndSwapWorkerCredential")
	if casStart < 0 {
		t.Fatal("Postgres credential CAS implementation is missing")
	}
	casEnd := strings.Index(postgresSource[casStart:], "\nfunc ")
	if casEnd < 0 {
		t.Fatal("could not isolate Postgres credential CAS implementation")
	}
	casSource := postgresSource[casStart : casStart+casEnd]
	if strings.Count(casSource, "UPDATE workers") != 1 ||
		!strings.Contains(casSource, "WHERE tenant_id = $1 AND id = $2") {
		t.Fatal("credential CAS must be one tenant+worker UPDATE")
	}
	for _, predicate := range []string{
		"resources_json->>'credential_generation'",
		"resources_json->>'agent_token_sha256'",
		"resources_json->>'enrollment_idempotency_sha256'",
		"resources_json->>'enrollment_request_sha256'",
	} {
		if !strings.Contains(casSource, predicate) {
			t.Fatalf("credential CAS is missing predicate %q", predicate)
		}
	}

	for name, source := range map[string]string{
		"heartbeat upsert": postgresSource,
		"Guard satellite":  guardSource,
	} {
		for _, key := range []string{
			"agent_token_sha256",
			"enrollment_idempotency_sha256",
			"enrollment_request_sha256",
			"credential_generation",
		} {
			if strings.Count(source, "workers.resources_json->'"+key+"'") == 0 {
				t.Fatalf("%s does not preserve reserved key %q", name, key)
			}
		}
	}
}

func readWorkerCredentialSource(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "controlplane", name)
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(payload)
}
