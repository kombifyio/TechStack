package remoteguard

import (
	"context"
	"errors"
	"testing"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/pairingtoken"
)

type fakeRemoteEnrollmentWorkers struct {
	controlplane.MemoryStore
	tokenHash string
	used      bool
}

func (f *fakeRemoteEnrollmentWorkers) GetPairingTokenByHash(_ context.Context, _, tokenHash string) (*controlplane.PairingToken, error) {
	if tokenHash != f.tokenHash {
		return nil, controlplane.ErrNotFound
	}
	status := "active"
	if f.used {
		status = "used"
	}
	return &controlplane.PairingToken{TokenHash: tokenHash, Status: status}, nil
}

func TestEnrollerWaitsForTokenRedemption(t *testing.T) {
	_, hash, err := pairingtoken.Generate("tenant-1")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	workers := &fakeRemoteEnrollmentWorkers{tokenHash: hash, used: true}
	enroller := NewEnroller(Config{Workers: workers})
	if err := enroller.waitForTokenRedemption(context.Background(), "tenant-1", hash); err != nil {
		t.Fatalf("waitForTokenRedemption: %v", err)
	}
}

func TestClassifyEnrollmentErrorMapsSSHAuth(t *testing.T) {
	reason, message, retryable := ClassifyEnrollmentError(errors.New("ssh: unable to authenticate, attempted methods [none password]"))
	if reason != ReasonSSHAuth || message == "" || !retryable {
		t.Fatalf("ClassifyEnrollmentError() = (%q, %q, %v)", reason, message, retryable)
	}
}
