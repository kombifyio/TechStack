package serverregistry

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOutboxClaimNeverSerializesCapabilityToken(t *testing.T) {
	encoded, err := json.Marshal(OutboxClaim{
		OutboxItem: OutboxItem{TenantID: "tenant-1", ServerID: "server-1"},
		ClaimToken: "claim-token-must-not-leak",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "claim-token-must-not-leak") || strings.Contains(string(encoded), "ClaimToken") {
		t.Fatalf("claim token serialized: %s", encoded)
	}
}
