package providercontrol

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
)

// ManagedCredentialAuthority describes one platform-owned provider credential
// without carrying the credential itself. Native admission materializes the
// tenant/subject-specific opaque handle in the same transaction as the lease.
type ManagedCredentialAuthority struct {
	ProviderID    string
	Version       string
	GrantID       string
	Scope         string
	CustodyRef    string
	ConnectionRef string
	ValidUntil    time.Time
}

// ManagedCredentialAuthoritySet holds at most one platform-owned credential
// authority per managed provider, keyed by provider id. A provider absent from
// the set writes no handle, which makes its managed profile resolution fail
// closed on the "exactly one unrevoked credential handle" rule rather than
// dispatching against unattested custody.
type ManagedCredentialAuthoritySet map[string]ManagedCredentialAuthority

// Authority resolves the authority registered for providerID. A registration
// filed under a key that disagrees with its own ProviderID is treated as
// absent, so a mis-keyed entry cannot mint handles for another provider.
func (set ManagedCredentialAuthoritySet) Authority(providerID string) (ManagedCredentialAuthority, bool) {
	trimmed := strings.TrimSpace(providerID)
	if len(set) == 0 || trimmed == "" {
		return ManagedCredentialAuthority{}, false
	}
	authority, ok := set[trimmed]
	if !ok || strings.TrimSpace(authority.ProviderID) != trimmed {
		return ManagedCredentialAuthority{}, false
	}
	return authority, true
}

func ensureManagedCredentialAuthorityTx(
	ctx context.Context,
	tx *sql.Tx,
	tenantID string,
	lease vmlease.Lease,
	now time.Time,
	authorities ManagedCredentialAuthoritySet,
) error {
	authority, registered := authorities.Authority(lease.Resource.ProviderID)
	if !registered {
		return nil
	}
	handle, err := managedCredentialHandle(tenantID, lease, now, authority)
	if err != nil {
		return err
	}
	custodyHash, err := credentialCustodyHash(tenantID, handle)
	if err != nil {
		return unavailableProfile("hash managed credential custody", err)
	}
	connectionHash, err := credentialConnectionHash(tenantID, handle)
	if err != nil {
		return unavailableProfile("hash managed provider connection", err)
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO provider_credential_handles (
	tenant_id, handle_id, handle_version, provider_id, credential_mode,
	subject_kind, subject_id, grant_id, credential_scope, custody_ref,
	connection_ref, custody_hash, connection_hash, valid_from, valid_until
) VALUES (
	$1, $2, $3, $4, $5,
	$6, $7, $8, $9, $10,
	$11, $12, $13, $14, $15
)
ON CONFLICT (tenant_id, handle_id, handle_version) DO NOTHING`,
		tenantID, handle.HandleID, handle.HandleVersion, handle.ProviderID, string(handle.CredentialMode),
		handle.SubjectKind, handle.SubjectID, handle.GrantID, handle.Scope, handle.CustodyRef,
		handle.ConnectionRef, custodyHash, connectionHash, handle.ValidFrom, handle.ValidUntil,
	)
	if err != nil {
		return unavailableProfile("persist managed credential authority", err)
	}
	var persistedCustodyHash, persistedConnectionHash string
	err = tx.QueryRowContext(ctx, `
SELECT custody_hash, connection_hash
FROM provider_credential_handles
WHERE tenant_id = $1 AND handle_id = $2 AND handle_version = $3
  AND revoked_at IS NULL AND valid_from <= $4 AND valid_until > $4`,
		tenantID, handle.HandleID, handle.HandleVersion, now,
	).Scan(&persistedCustodyHash, &persistedConnectionHash)
	if err != nil || persistedCustodyHash != custodyHash || persistedConnectionHash != connectionHash {
		return unavailableProfile("managed credential authority did not converge", err)
	}
	return nil
}

func managedCredentialHandle(
	tenantID string,
	lease vmlease.Lease,
	now time.Time,
	authority ManagedCredentialAuthority,
) (credentialHandleRow, error) {
	tenantID = strings.TrimSpace(tenantID)
	authority.ProviderID = strings.TrimSpace(authority.ProviderID)
	authority.Version = strings.TrimSpace(authority.Version)
	authority.GrantID = strings.TrimSpace(authority.GrantID)
	authority.Scope = strings.TrimSpace(authority.Scope)
	authority.CustodyRef = strings.TrimSuffix(strings.TrimSpace(authority.CustodyRef), "/")
	authority.ConnectionRef = strings.TrimSuffix(strings.TrimSpace(authority.ConnectionRef), "/")
	subjectKind := strings.TrimSpace(string(lease.Subject.Kind))
	subjectID := strings.TrimSpace(lease.Subject.ID)
	if tenantID == "" || !canonicalProviderID(authority.ProviderID) ||
		!canonicalCatalogIdentifier(authority.Version) ||
		!validCredentialText(authority.GrantID) || !validCredentialText(authority.Scope) ||
		!custodyRefPattern.MatchString(authority.CustodyRef) ||
		!connectionRefPattern.MatchString(authority.ConnectionRef) ||
		(subjectKind != "user" && subjectKind != "org") ||
		!validLeaseSubjectID(subjectID) ||
		!authority.ValidUntil.UTC().After(now.UTC()) {
		return credentialHandleRow{}, unavailableProfile("managed credential authority is invalid", nil)
	}
	subjectDigest := sha256.Sum256([]byte(tenantID + "\x00" + subjectKind + "\x00" + subjectID))
	subjectKey := hex.EncodeToString(subjectDigest[:8])
	version := authority.ValidUntil.UTC().Unix()
	if version <= 0 {
		return credentialHandleRow{}, unavailableProfile("managed credential authority version is invalid", nil)
	}
	versionText := authority.Version + "-" + strconv.FormatInt(version, 10)
	return credentialHandleRow{
		HandleID:       "managed-" + authority.ProviderID + "-" + subjectKey + "-" + versionText,
		HandleVersion:  version,
		ProviderID:     authority.ProviderID,
		CredentialMode: CredentialModeManaged,
		SubjectKind:    subjectKind,
		SubjectID:      subjectID,
		GrantID:        authority.GrantID,
		Scope:          authority.Scope,
		CustodyRef:     authority.CustodyRef + "/" + subjectKey + "/" + versionText,
		ConnectionRef:  authority.ConnectionRef + "/" + subjectKey + "/" + versionText,
		ValidFrom:      now.UTC(),
		ValidUntil:     authority.ValidUntil.UTC(),
	}, nil
}

func (authority ManagedCredentialAuthority) String() string {
	return fmt.Sprintf("providercontrol.ManagedCredentialAuthority{provider=%q, version=%q}", authority.ProviderID, authority.Version)
}
