package routes

import (
	"errors"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/guardidentity"
	"github.com/kombifyio/techstack/pkg/workerauth"
)

func TestGuardAccessTokenAcceptsSuccessorKeyDuringServiceSecretRotation(t *testing.T) {
	t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", "")
	t.Setenv("TECHSTACK_WORKER_TOKEN_SECRET", "")
	t.Setenv("TECHSTACK_WORKER_SECRET_STRICT", "")
	t.Setenv("SERVICE_AUTH_SECRET", "primary-secret")
	t.Setenv("SERVICE_AUTH_SECRET_NEXT", "next-secret")
	h := workerRouteHandlers{credentialSecret: workerauth.SecretFromEnv()}
	now := time.Now().UTC()
	claims := guardidentity.AccessClaims{TenantID: "tenant-1", WorkerID: "worker-1", JKT: "jkt-1"}
	mint := func(base string) string {
		token, _, err := guardidentity.IssueAccessToken(workerauth.PurposeKey([]byte(base), guardAccessPurpose), claims, now)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}

	for _, base := range []string{"primary-secret", "next-secret"} {
		if _, err := h.verifyGuardAccessToken(mint(base), now); err != nil {
			t.Fatalf("access token minted under %s: %v", base, err)
		}
	}
	_, err := h.verifyGuardAccessToken(mint("unknown-secret"), now)
	var denied *guardidentity.Error
	if !errors.As(err, &denied) || denied.Code != guardidentity.CodeProofInvalid {
		t.Fatalf("access token minted under an unknown key = %v, want %s", err, guardidentity.CodeProofInvalid)
	}
}
