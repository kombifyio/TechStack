package vmleases

import (
	"context"
	"errors"
	"strings"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"golang.org/x/crypto/ssh"
)

const MetadataKeySSHHostKey = "runtime_ssh_host_key"

var (
	ErrSSHHostKeyBinding   = errors.New("vmleases: SSH host key binding does not match current runtime")
	ErrSSHHostKeyImmutable = errors.New("vmleases: SSH host key is immutable")
)

// SSHHostKeyPinRequest binds one authenticated Guard observation to the exact
// current server and provider-resource generation behind a managed lease.
type SSHHostKeyPinRequest struct {
	TenantID                         string
	LeaseID                          vmlease.LeaseID
	OwnerSubjectID                   string
	ServerID                         string
	StackID                          string
	ServerGeneration                 int64
	ExpectedResourceGenerationDigest string
	HostKey                          string
}

type sshHostKeyPinStore interface {
	PinSSHHostKey(context.Context, SSHHostKeyPinRequest) (*vmlease.Lease, error)
}

// PinSSHHostKey atomically preserves the first host key admitted through
// authenticated Guard inventory. A later observation cannot rotate the pin.
func (s *Service) PinSSHHostKey(ctx context.Context, request SSHHostKeyPinRequest) (*vmlease.Lease, error) {
	request.TenantID = strings.TrimSpace(request.TenantID)
	request.LeaseID = vmlease.LeaseID(strings.TrimSpace(string(request.LeaseID)))
	request.OwnerSubjectID = strings.TrimSpace(request.OwnerSubjectID)
	request.ServerID = strings.TrimSpace(request.ServerID)
	request.StackID = strings.TrimSpace(request.StackID)
	request.ExpectedResourceGenerationDigest = strings.TrimSpace(request.ExpectedResourceGenerationDigest)
	request.HostKey = strings.TrimSpace(request.HostKey)
	if request.TenantID == "" || request.LeaseID == "" || request.OwnerSubjectID == "" ||
		request.ServerID == "" || request.StackID == "" || request.ServerGeneration == 0 ||
		!validResourceGenerationDigest(request.ExpectedResourceGenerationDigest) || request.HostKey == "" ||
		len(request.HostKey) > 16*1024 {
		return nil, ErrSSHHostKeyBinding
	}
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(request.HostKey)); err != nil {
		return nil, ErrSSHHostKeyBinding
	}
	store, ok := s.store.(sshHostKeyPinStore)
	if !ok {
		return nil, ErrSSHHostKeyBinding
	}
	return store.PinSSHHostKey(ctx, request)
}

func ensureSSHHostKeyUnchanged(existing, updated vmlease.Lease) error {
	pinned := strings.TrimSpace(existing.Metadata[MetadataKeySSHHostKey])
	if strings.TrimSpace(updated.Metadata[MetadataKeySSHHostKey]) != pinned {
		return ErrSSHHostKeyImmutable
	}
	return nil
}
