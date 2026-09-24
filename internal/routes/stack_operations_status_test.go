package routes

import (
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/runtimehealth"
)

func TestStackOperationServerConnectedAtRequiresCanonicalFreshHealthyGuardEvidence(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-runtimehealth.FreshHeartbeatWindow / 2)
	stale := now.Add(-runtimehealth.FreshHeartbeatWindow - time.Second)
	future := now.Add(31 * time.Second)
	base := stackOperationServer{
		Assignment:  "stack",
		Approved:    true,
		Status:      "connected",
		Health:      stackServerHealth{State: "healthy"},
		heartbeatAt: &fresh,
	}
	tests := []struct {
		name   string
		server stackOperationServer
		want   bool
	}{
		{name: "fresh healthy canonical heartbeat", server: base, want: true},
		{name: "fresh degraded canonical heartbeat", server: func() stackOperationServer {
			server := base
			server.Status = "degraded"
			server.Health.State = "degraded"
			return server
		}(), want: true},
		{name: "worker last seen text is not canonical evidence", server: func() stackOperationServer {
			server := base
			server.heartbeatAt = nil
			server.LastSeen = fresh.Format(time.RFC3339Nano)
			return server
		}()},
		{name: "missing heartbeat", server: func() stackOperationServer {
			server := base
			server.heartbeatAt = nil
			return server
		}()},
		{name: "stale heartbeat", server: func() stackOperationServer {
			server := base
			server.heartbeatAt = &stale
			return server
		}()},
		{name: "implausible future heartbeat", server: func() stackOperationServer {
			server := base
			server.heartbeatAt = &future
			return server
		}()},
		{name: "unassigned", server: func() stackOperationServer {
			server := base
			server.Assignment = "available"
			return server
		}()},
		{name: "not approved", server: func() stackOperationServer {
			server := base
			server.Approved = false
			return server
		}()},
		{name: "invalid connection", server: func() stackOperationServer {
			server := base
			server.Status = "provisioned"
			return server
		}()},
		{name: "invalid health", server: func() stackOperationServer {
			server := base
			server.Health.State = "unknown"
			return server
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := stackOperationServerConnectedAt(test.server, now); got != test.want {
				t.Fatalf("stackOperationServerConnectedAt() = %v, want %v for %#v", got, test.want, test.server)
			}
		})
	}

	boundaryHeartbeat := now.Add(-runtimehealth.FreshHeartbeatWindow + time.Millisecond)
	boundary := base
	boundary.heartbeatAt = &boundaryHeartbeat
	boundary.observedAt = now
	if !stackOperationServerConnectedAt(boundary, now.Add(time.Second)) {
		t.Fatal("readiness must use the canonical projection timestamp instead of aging within one response")
	}
}
