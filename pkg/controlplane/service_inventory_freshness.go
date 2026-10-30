package controlplane

import (
	"fmt"
	"strings"
)

// guardInventoryObservedKey marks a service row whose observation time follows
// every accepted Guard inventory: a reported service, or a discovered service
// whose absence the inventory confirmed. The projection rewrites such a row only
// when something other than its freshness changed, so its stored observed_at is
// the last material change, not the last observation.
const guardInventoryObservedKey = "inventory_observed"

// serviceInventorySeenAtSQL is when the inventory that the row is stamped with
// was last observed, for a marked row whose stored time is older; NULL
// otherwise. The snapshot of an unchanged inventory revision is refreshed on
// every observation (insertServerInventoryTx), and a new revision restamps
// every row the projection confirms, so the snapshot of the row's own revision
// carries its true last observation.
const serviceInventorySeenAtSQL = `(SELECT seen.observed_at FROM server_inventory_snapshots seen
	WHERE services.metadata_json->>'` + guardInventoryObservedKey + `' = 'true'
		AND seen.tenant_id = services.tenant_id AND seen.server_id = services.server_id
		AND seen.revision = CASE WHEN services.metadata_json->>'inventory_revision' ~ '^[0-9]{1,18}$'
			THEN (services.metadata_json->>'inventory_revision')::bigint END
		AND seen.observed_at > COALESCE(services.observed_at, '-infinity'::timestamptz))`

// Read projections of the services table: the column lists with observed_at
// (column and metadata key) taken from serviceInventorySeenAtSQL when it is
// newer. Writes keep the stored columns, so a write never persists a derived
// time.
var (
	serviceRuntimeReadColumns = withServiceInventorySeenAt(serviceRuntimeColumns)
	serviceReadColumns        = withServiceInventorySeenAt(serviceColumns)
)

func withServiceInventorySeenAt(columns string) string {
	metadata := `(CASE WHEN ` + serviceInventorySeenAtSQL + ` IS NULL THEN services.metadata_json
		ELSE jsonb_set(services.metadata_json, '{observed_at}', to_jsonb(to_char(` + serviceInventorySeenAtSQL +
		` AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')), false) END)::text AS metadata_json`
	replacements := []struct{ from, to string }{
		{"metadata_json::text", metadata},
		{"observed_at, stackkit_version", "COALESCE(" + serviceInventorySeenAtSQL + ", services.observed_at) AS observed_at, stackkit_version"},
	}
	for index, replacement := range replacements {
		if !strings.Contains(columns, replacement.from) {
			if index > 0 {
				continue // the legacy column list has no observed_at column
			}
			panic(fmt.Sprintf("controlplane: service column list lacks %q", replacement.from))
		}
		columns = strings.Replace(columns, replacement.from, replacement.to, 1)
	}
	return columns
}
