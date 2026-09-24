package routes

import (
	"net/http"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/internal/guardbootstrap"
	"github.com/kombifyio/techstack/pkg/grpcserver"
	"github.com/kombifyio/techstack/pkg/pairingtoken"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
)

type recordingWorkerRuntimeLogWriter struct {
	entries []grpcserver.AgentLogEntry
}

func (w *recordingWorkerRuntimeLogWriter) AppendRuntimeLog(entry grpcserver.AgentLogEntry) grpcserver.AgentLogEntry {
	entry.ID = "log-1"
	w.entries = append(w.entries, entry)
	return entry
}

func TestBootstrapLogBindsManagedOperationWithoutPersistingPairingToken(t *testing.T) {
	rawToken, tokenHash, err := pairingtoken.Generate("tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	const leaseID = "lease-1"
	const operationID = "operation-1"
	digest := "sha256:" + strings.Repeat("a", 64)
	token := activePairingToken("tenant-1", tokenHash)
	token.StackID = "stack-1"
	token.Metadata = map[string]any{
		"lease_id":                             leaseID,
		guardbootstrap.MetadataOperationID:     operationID,
		guardbootstrap.MetadataCloudInitSHA256: digest,
	}
	logs := &recordingWorkerRuntimeLogWriter{}
	handler := workerRouteHandlers{
		wst:         &pairingLookupRecordingStore{result: token},
		runtimeLogs: logs,
	}
	event, recorder := workerRouteTestEvent(http.MethodPost, "/api/v1/workers/bootstrap/logs", "installer failed with "+rawToken)
	event.Request.Header.Set("Authorization", "Bearer "+rawToken)
	event.Request.Header.Set("X-Kombify-Log-Phase", "guard-start")

	if routeErr := handler.ingestBootstrapLog(event); routeErr != nil {
		t.Fatalf("ingest bootstrap log: %v", routeErr)
	}
	if recorder.Code != http.StatusAccepted || len(logs.entries) != 1 {
		t.Fatalf("response=%d entries=%d body=%s", recorder.Code, len(logs.entries), recorder.Body.String())
	}
	entry := logs.entries[0]
	if strings.Contains(entry.Message, rawToken) {
		t.Fatalf("bootstrap log retained pairing token: %q", entry.Message)
	}
	if entry.LeaseID != leaseID || entry.ServerID != runtimeidentity.LeaseServerID(leaseID) || entry.AgentID != runtimeidentity.LeaseRuntimeAgentID("tenant-1", leaseID) {
		t.Fatalf("runtime binding = %+v", entry)
	}
	if entry.RuntimeActionID != operationID || entry.Fields["provider_operation_id"] != operationID || entry.Fields[guardbootstrap.MetadataCloudInitSHA256] != digest {
		t.Fatalf("bootstrap evidence = %+v", entry)
	}
}
