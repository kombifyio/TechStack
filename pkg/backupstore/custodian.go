package backupstore

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const repositoryPasswordBytes = 32

type StoreProvisioner interface {
	Provision(ctx context.Context, stackID string) (*Store, error)
	RevokeToken(ctx context.Context, tokenID string) error
}

type CustodyRepository interface {
	Upsert(ctx context.Context, credentials Credentials) (CustodyEvidence, error)
	Evidence(ctx context.Context, tenantID, stackID string) (CustodyEvidence, error)
}

// Custodian makes provisioning and durable encrypted custody one fail-closed
// admission step. It never returns raw credentials to Inventory callers.
type Custodian struct {
	provisioner StoreProvisioner
	repository  CustodyRepository
	password    func() (string, error)
}

func NewCustodian(provisioner StoreProvisioner, repository CustodyRepository) (*Custodian, error) {
	if provisioner == nil || repository == nil {
		return nil, errors.New("managed backup custodian requires provisioner and custody repository")
	}
	return &Custodian{provisioner: provisioner, repository: repository, password: generateRepositoryPassword}, nil
}

func (c *Custodian) Ensure(ctx context.Context, tenantID, stackID string) (CustodyEvidence, error) {
	tenantID, stackID = strings.TrimSpace(tenantID), strings.TrimSpace(stackID)
	if tenantID == "" || stackID == "" {
		return CustodyEvidence{}, fmt.Errorf("%w: tenant id and stack id are required", ErrInvalidCustody)
	}
	existing, err := c.repository.Evidence(ctx, tenantID, stackID)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, ErrCustodyNotFound) {
		return CustodyEvidence{}, fmt.Errorf("inspect managed backup custody: %w", err)
	}

	provisioned, err := c.provisioner.Provision(ctx, stackID)
	if err != nil {
		return CustodyEvidence{}, fmt.Errorf("provision managed backup target: %w", err)
	}
	if provisioned == nil {
		return CustodyEvidence{}, errors.New("provision managed backup target: provider returned no store")
	}
	password, err := c.password()
	if err != nil {
		revokeErr := c.provisioner.RevokeToken(ctx, provisioned.TokenID)
		return CustodyEvidence{}, errors.Join(fmt.Errorf("generate managed backup repository password: %w", err), revokeFailure(revokeErr))
	}
	evidence, err := c.repository.Upsert(ctx, Credentials{
		TenantID: tenantID, StackID: stackID, Bucket: provisioned.Bucket, Endpoint: provisioned.Endpoint,
		TokenID: provisioned.TokenID, AccessKeyID: provisioned.AccessKeyID,
		SecretAccessKey: provisioned.SecretAccessKey, RepositoryPassword: password,
	})
	if err == nil {
		return evidence, nil
	}
	revokeErr := c.provisioner.RevokeToken(ctx, provisioned.TokenID)
	return CustodyEvidence{}, errors.Join(fmt.Errorf("persist managed backup custody: %w", err), revokeFailure(revokeErr))
}

func generateRepositoryPassword() (string, error) {
	random := make([]byte, repositoryPasswordBytes)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(random), nil
}

func revokeFailure(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("revoke unpersisted managed backup token: %w", err)
}

var _ StoreProvisioner = (*Client)(nil)
var _ CustodyRepository = (*PostgresCustodyStore)(nil)
