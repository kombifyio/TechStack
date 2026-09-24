package providercontrol

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
	"github.com/google/uuid"
)

const runtimeOfferingMetadataKey = "runtime_offering_id"

var (
	catalogIdentifierPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	custodyRefPattern        = regexp.MustCompile(`^custody://[A-Za-z0-9][A-Za-z0-9._~/-]*$`)
	connectionRefPattern     = regexp.MustCompile(`^provider-connection://[A-Za-z0-9][A-Za-z0-9._~/-]*$`)
)

// PostgresCatalogExecutionProfileResolver resolves immutable execution
// profiles from migration 027's active catalog and tenant-scoped credential
// handles. It reads only opaque references and public capability data; raw
// provider credentials are neither selected nor returned.
type PostgresCatalogExecutionProfileResolver struct {
	db *sql.DB
}

// CatalogCoverageRequirement is one provider/offering pair which must resolve
// to exactly one profile pinned to the adapter build registered by this
// process. Startup must reject stale catalog manifests instead of discovering
// the mismatch only after a paid provisioning request has been admitted.
type CatalogCoverageRequirement struct {
	ProviderID            string
	OfferingID            string
	AdapterID             string
	AdapterManifestHash   string
	ProvisionDispatchMode ProvisionDispatchMode
}

// ValidateActiveCatalogCoverage prevents a runtime from accepting Creation
// Wizard traffic with only a partial product catalog. This is a startup
// contract, independent from tenant credential materialization.
func ValidateActiveCatalogCoverage(
	ctx context.Context,
	db *sql.DB,
	requirements []CatalogCoverageRequirement,
) error {
	if db == nil || len(requirements) == 0 {
		return fmt.Errorf("%w: active catalog coverage requirements are missing", ErrInvalidRequest)
	}
	normalized, err := normalizeCatalogCoverageRequirements(requirements)
	if err != nil {
		return err
	}

	tx, err := db.BeginTx(ctx, &sql.TxOptions{
		Isolation: sql.LevelRepeatableRead,
		ReadOnly:  true,
	})
	if err != nil {
		return unavailableProfile("begin active catalog coverage transaction", err)
	}
	defer func() { _ = tx.Rollback() }()

	version, err := loadActiveCatalogCoverageVersion(ctx, tx)
	if err != nil {
		return err
	}
	if err := validateCatalogCoverageProfiles(ctx, tx, version, normalized); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return unavailableProfile("commit active catalog coverage transaction", err)
	}
	return nil
}

func normalizeCatalogCoverageRequirements(
	requirements []CatalogCoverageRequirement,
) ([]CatalogCoverageRequirement, error) {
	normalized := make([]CatalogCoverageRequirement, 0, len(requirements))
	seen := make(map[string]struct{}, len(requirements))
	for _, requirement := range requirements {
		requirement.ProviderID = strings.TrimSpace(requirement.ProviderID)
		requirement.OfferingID = strings.TrimSpace(requirement.OfferingID)
		requirement.AdapterID = strings.TrimSpace(requirement.AdapterID)
		requirement.AdapterManifestHash = strings.TrimSpace(requirement.AdapterManifestHash)
		if !canonicalCatalogIdentifier(requirement.ProviderID) ||
			!canonicalCatalogIdentifier(requirement.OfferingID) ||
			!canonicalCatalogIdentifier(requirement.AdapterID) ||
			!executionProfileDigestPattern.MatchString(requirement.AdapterManifestHash) ||
			!admittedProvisionDispatchMode(requirement.ProvisionDispatchMode) {
			return nil, fmt.Errorf("%w: catalog coverage identity is invalid", ErrInvalidRequest)
		}
		key := requirement.ProviderID + "\x00" + requirement.OfferingID
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("%w: catalog coverage requirement is duplicated", ErrInvalidRequest)
		}
		seen[key] = struct{}{}
		normalized = append(normalized, requirement)
	}
	return normalized, nil
}

func loadActiveCatalogCoverageVersion(ctx context.Context, tx *sql.Tx) (string, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT catalog_version
FROM provider_catalog_versions
WHERE status = 'active'
ORDER BY catalog_version
LIMIT 2`)
	if err != nil {
		return "", unavailableProfile("active catalog coverage lookup failed", err)
	}
	versions := make([]string, 0, 2)
	for rows.Next() {
		var version string
		if scanErr := rows.Scan(&version); scanErr != nil {
			_ = rows.Close()
			return "", unavailableProfile("active catalog coverage scan failed", scanErr)
		}
		versions = append(versions, strings.TrimSpace(version))
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		_ = rows.Close()
		return "", unavailableProfile("active catalog coverage read failed", rowsErr)
	}
	_ = rows.Close()
	if len(versions) != 1 || !canonicalCatalogIdentifier(versions[0]) {
		return "", unavailableProfile("exactly one active catalog is required for startup", nil)
	}
	return versions[0], nil
}

func validateCatalogCoverageProfiles(
	ctx context.Context,
	tx *sql.Tx,
	version string,
	requirements []CatalogCoverageRequirement,
) error {
	for _, requirement := range requirements {
		var count, pinnedCount int
		if countErr := tx.QueryRowContext(ctx, `
SELECT
  count(*),
  count(*) FILTER (
      WHERE adapter_id = $4
        AND adapter_manifest_hash = $5
        AND provision_dispatch_mode = $6
  )
FROM provider_catalog_profiles
WHERE catalog_version = $1
  AND provider_id = $2
  AND offering_id = $3`,
			version,
			requirement.ProviderID,
			requirement.OfferingID,
			requirement.AdapterID,
			requirement.AdapterManifestHash,
			requirement.ProvisionDispatchMode,
		).Scan(&count, &pinnedCount); countErr != nil {
			return unavailableProfile("catalog coverage profile lookup failed", countErr)
		}
		if count != 1 || pinnedCount != 1 {
			return unavailableProfile(
				fmt.Sprintf(
					"active catalog requires exactly one adapter-pinned %s/%s profile",
					requirement.ProviderID,
					requirement.OfferingID,
				),
				nil,
			)
		}
	}
	return nil
}

type transactionalExecutionProfileResolver interface {
	ExecutionProfileResolver
	ResolveExecutionProfileTx(context.Context, *sql.Tx, ProfileRequest) (ExecutionProfile, error)
}

var _ transactionalExecutionProfileResolver = (*PostgresCatalogExecutionProfileResolver)(nil)

// NewPostgresCatalogExecutionProfileResolver creates a fail-closed profile
// resolver. Catalog, lease selection, and credential custody are read in one
// repeatable-read transaction with tenant RLS set transaction-locally.
func NewPostgresCatalogExecutionProfileResolver(db *sql.DB) (*PostgresCatalogExecutionProfileResolver, error) {
	if db == nil {
		return nil, fmt.Errorf("%w: provider catalog database is required", ErrInvalidRequest)
	}
	return &PostgresCatalogExecutionProfileResolver{db: db}, nil
}

// ResolveExecutionProfile returns one canonical catalog profile and one exact,
// unrevoked credential handle. Missing or ambiguous authority data always
// returns ErrProfileUnavailable.
func (r *PostgresCatalogExecutionProfileResolver) ResolveExecutionProfile(
	ctx context.Context,
	request ProfileRequest,
) (ExecutionProfile, error) {
	request, err := normalizeCatalogProfileRequest(request)
	if err != nil {
		return ExecutionProfile{}, err
	}
	if r == nil || r.db == nil {
		return ExecutionProfile{}, fmt.Errorf("%w: provider catalog resolver is unavailable", ErrProfileUnavailable)
	}

	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return ExecutionProfile{}, unavailableProfile("begin catalog transaction", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, execErr := tx.ExecContext(ctx, `SELECT set_config($1, $2, true)`, tenantContextKey, request.TenantID); execErr != nil {
		return ExecutionProfile{}, unavailableProfile("set tenant context", execErr)
	}
	profile, err := r.resolveExecutionProfileTx(ctx, tx, request)
	if err != nil {
		return ExecutionProfile{}, err
	}
	if err := tx.Commit(); err != nil {
		return ExecutionProfile{}, unavailableProfile("commit catalog transaction", err)
	}
	return profile, nil
}

// ResolveExecutionProfileTx resolves a profile inside the caller's existing
// transaction. It neither begins nor commits a transaction and does not set
// tenant context; admission callers must establish tenant RLS before calling.
func (r *PostgresCatalogExecutionProfileResolver) ResolveExecutionProfileTx(
	ctx context.Context,
	tx *sql.Tx,
	request ProfileRequest,
) (ExecutionProfile, error) {
	request, err := normalizeCatalogProfileRequest(request)
	if err != nil {
		return ExecutionProfile{}, err
	}
	if r == nil || r.db == nil || tx == nil {
		return ExecutionProfile{}, fmt.Errorf("%w: provider catalog transaction is required", ErrInvalidRequest)
	}
	return r.resolveExecutionProfileTx(ctx, tx, request)
}

func (r *PostgresCatalogExecutionProfileResolver) resolveExecutionProfileTx(
	ctx context.Context,
	tx *sql.Tx,
	request ProfileRequest,
) (ExecutionProfile, error) {
	selection, err := loadLeaseProfileSelection(ctx, tx, request)
	if err != nil {
		return ExecutionProfile{}, err
	}
	if selection.ProviderID == "proxmox" {
		return resolveSubstrateExecutionProfileTx(ctx, tx, request, selection)
	}
	catalogVersion, err := loadActiveCatalogVersion(ctx, tx)
	if err != nil {
		return ExecutionProfile{}, err
	}
	profileRow, err := loadCatalogProfile(ctx, tx, catalogVersion, selection)
	if err != nil {
		return ExecutionProfile{}, err
	}
	handle, err := loadCredentialHandle(ctx, tx, request.TenantID, selection, profileRow.CredentialMode)
	if err != nil {
		return ExecutionProfile{}, err
	}
	profile, err := assembleCatalogExecutionProfile(request.TenantID, profileRow, handle)
	if err != nil {
		return ExecutionProfile{}, err
	}
	if _, _, err := normalizeExecutionProfile(profile); err != nil {
		return ExecutionProfile{}, unavailableProfile("catalog profile is invalid", err)
	}
	return profile, nil
}

type leaseProfileSelection struct {
	ProviderID     string
	OwnerSubjectID string
	SubjectKind    string
	SubjectID      string
	OfferingID     string
}

func loadLeaseProfileSelection(
	ctx context.Context,
	tx *sql.Tx,
	request ProfileRequest,
) (leaseProfileSelection, error) {
	var selection leaseProfileSelection
	// Profile resolution performs no provider side effect. The operation insert
	// revalidates the exact lease generation, and the claim trigger owns the
	// final row-locked mutation fence; keeping this lookup SELECT-only preserves
	// the dedicated runtime role's least-privilege boundary.
	err := tx.QueryRowContext(ctx, `
SELECT
    lease.provider_id,
	lease.owner_subject_id,
    lease.lease_json->'subject'->>'kind',
    lease.lease_json->'subject'->>'id',
    lease.lease_json->'metadata'->>$6
FROM techstack_vm_leases AS lease
WHERE lease.tenant_id = $1
  AND lease.id = $2
  AND lease.lease_revision = $3
  AND lease.server_id = $4
  AND lease.resource_generation_id = $5::uuid`,
		request.TenantID,
		request.LeaseID,
		int64(request.LeaseRevision), // #nosec G115 -- normalizeCatalogProfileRequest bounds the revision to MaxJSONSafeInteger.
		request.RuntimeServerID,
		request.ResourceGenerationID,
		runtimeOfferingMetadataKey,
	).Scan(
		&selection.ProviderID,
		&selection.OwnerSubjectID,
		&selection.SubjectKind,
		&selection.SubjectID,
		&selection.OfferingID,
	)
	if err != nil {
		return leaseProfileSelection{}, unavailableProfile("lease profile selection is unavailable", err)
	}
	selection.ProviderID = strings.TrimSpace(selection.ProviderID)
	selection.OwnerSubjectID = strings.TrimSpace(selection.OwnerSubjectID)
	selection.SubjectKind = strings.TrimSpace(selection.SubjectKind)
	selection.SubjectID = strings.TrimSpace(selection.SubjectID)
	selection.OfferingID = strings.TrimSpace(selection.OfferingID)
	if !validLeaseProfileSelection(selection) {
		return leaseProfileSelection{}, unavailableProfile("lease profile selection is invalid", nil)
	}
	if !consistentLeaseCredentialSubject(selection, request) {
		return leaseProfileSelection{}, unavailableProfile("lease credential subject is inconsistent", nil)
	}
	return selection, nil
}

func validLeaseProfileSelection(selection leaseProfileSelection) bool {
	if !canonicalProviderID(selection.ProviderID) || !canonicalCatalogIdentifier(selection.OfferingID) {
		return false
	}
	if selection.SubjectKind != "user" && selection.SubjectKind != "org" {
		return false
	}
	return validLeaseSubjectID(selection.OwnerSubjectID) && validLeaseSubjectID(selection.SubjectID)
}

func validLeaseSubjectID(value string) bool {
	return value != "" && len(value) <= 512 && !strings.ContainsAny(value, "\r\n\x00")
}

func consistentLeaseCredentialSubject(selection leaseProfileSelection, request ProfileRequest) bool {
	if selection.SubjectKind == "org" {
		return selection.SubjectID == request.TenantID
	}
	return selection.SubjectID == selection.OwnerSubjectID
}

func loadActiveCatalogVersion(ctx context.Context, tx *sql.Tx) (string, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT catalog_version
FROM provider_catalog_versions
WHERE status = 'active'
ORDER BY catalog_version
LIMIT 2`)
	if err != nil {
		return "", unavailableProfile("active catalog lookup failed", err)
	}
	defer func() { _ = rows.Close() }()

	versions := make([]string, 0, 2)
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return "", unavailableProfile("active catalog scan failed", err)
		}
		versions = append(versions, strings.TrimSpace(version))
	}
	if err := rows.Err(); err != nil {
		return "", unavailableProfile("active catalog read failed", err)
	}
	if len(versions) != 1 || !canonicalCatalogIdentifier(versions[0]) {
		return "", unavailableProfile("exactly one valid active catalog is required", nil)
	}
	return versions[0], nil
}

type catalogProfileRow struct {
	CatalogVersion        string
	ProviderID            string
	AdapterID             string
	CredentialMode        CredentialMode
	RuntimeProfileID      string
	OfferingID            string
	CanPause              bool
	StopEffect            string
	CanRecreate           bool
	AdapterManifestHash   string
	ProvisionDispatchMode ProvisionDispatchMode
	CapabilitySnapshot    json.RawMessage
}

func loadCatalogProfile(
	ctx context.Context,
	tx *sql.Tx,
	catalogVersion string,
	selection leaseProfileSelection,
) (catalogProfileRow, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT
    catalog_version,
    provider_id,
    adapter_id,
    credential_mode,
    runtime_profile_id,
    offering_id,
    can_pause,
    stop_effect,
    can_recreate,
	adapter_manifest_hash,
	provision_dispatch_mode,
    capability_snapshot::text
FROM provider_catalog_profiles
WHERE catalog_version = $1
  AND provider_id = $2
  AND offering_id = $3
ORDER BY runtime_profile_id
LIMIT 2`, catalogVersion, selection.ProviderID, selection.OfferingID)
	if err != nil {
		return catalogProfileRow{}, unavailableProfile("catalog profile lookup failed", err)
	}
	defer func() { _ = rows.Close() }()

	profiles := make([]catalogProfileRow, 0, 2)
	for rows.Next() {
		var row catalogProfileRow
		var credentialMode string
		var provisionDispatchMode string
		var capabilitySnapshot string
		if scanErr := rows.Scan(
			&row.CatalogVersion,
			&row.ProviderID,
			&row.AdapterID,
			&credentialMode,
			&row.RuntimeProfileID,
			&row.OfferingID,
			&row.CanPause,
			&row.StopEffect,
			&row.CanRecreate,
			&row.AdapterManifestHash,
			&provisionDispatchMode,
			&capabilitySnapshot,
		); scanErr != nil {
			return catalogProfileRow{}, unavailableProfile("catalog profile scan failed", scanErr)
		}
		row.CredentialMode = CredentialMode(strings.TrimSpace(credentialMode))
		row.ProvisionDispatchMode = ProvisionDispatchMode(strings.TrimSpace(provisionDispatchMode))
		row.CapabilitySnapshot = json.RawMessage(capabilitySnapshot)
		profiles = append(profiles, row)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return catalogProfileRow{}, unavailableProfile("catalog profile read failed", rowsErr)
	}
	if len(profiles) != 1 {
		return catalogProfileRow{}, unavailableProfile("exactly one catalog profile is required", nil)
	}
	row := profiles[0]
	row.CatalogVersion = strings.TrimSpace(row.CatalogVersion)
	row.ProviderID = strings.TrimSpace(row.ProviderID)
	row.AdapterID = strings.TrimSpace(row.AdapterID)
	row.RuntimeProfileID = strings.TrimSpace(row.RuntimeProfileID)
	row.OfferingID = strings.TrimSpace(row.OfferingID)
	row.StopEffect = strings.TrimSpace(row.StopEffect)
	row.AdapterManifestHash = strings.ToLower(strings.TrimSpace(row.AdapterManifestHash))
	if !validCatalogProfileRow(row, catalogVersion, selection) {
		return catalogProfileRow{}, unavailableProfile("catalog profile is invalid", nil)
	}
	canonicalCapabilities, err := canonicalCatalogCapabilities(row.CapabilitySnapshot)
	if err != nil {
		return catalogProfileRow{}, err
	}
	if err := validateManagedBootstrapNetworkAdapter(row.ProviderID, row.AdapterID, canonicalCapabilities); err != nil {
		return catalogProfileRow{}, unavailableProfile("managed bootstrap network contract is invalid", err)
	}
	row.CapabilitySnapshot = canonicalCapabilities
	return row, nil
}

func validCatalogProfileRow(
	row catalogProfileRow,
	catalogVersion string,
	selection leaseProfileSelection,
) bool {
	if row.CatalogVersion != catalogVersion || row.ProviderID != selection.ProviderID ||
		row.OfferingID != selection.OfferingID {
		return false
	}
	if !canonicalCatalogIdentifier(row.AdapterID) || !canonicalCatalogIdentifier(row.RuntimeProfileID) {
		return false
	}
	if row.CredentialMode != CredentialModeManaged && row.CredentialMode != CredentialModeBYOK {
		return false
	}
	if row.StopEffect != "pause" && row.StopEffect != "destroy" {
		return false
	}
	if !admittedProvisionDispatchMode(row.ProvisionDispatchMode) {
		return false
	}
	if !executionProfileDigestPattern.MatchString(row.AdapterManifestHash) {
		return false
	}
	return row.StopEffect != "pause" || row.CanPause
}

type credentialHandleRow struct {
	HandleID                string
	HandleVersion           int64
	ProviderID              string
	CredentialMode          CredentialMode
	SubjectKind             string
	SubjectID               string
	GrantID                 string
	Scope                   string
	CustodyRef              string
	ConnectionRef           string
	PersistedCustodyHash    string
	PersistedConnectionHash string
	ValidFrom               time.Time
	ValidUntil              time.Time
}

func loadCredentialHandle(
	ctx context.Context,
	tx *sql.Tx,
	tenantID string,
	selection leaseProfileSelection,
	credentialMode CredentialMode,
) (credentialHandleRow, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT
    handle_id,
    handle_version,
    provider_id,
    credential_mode,
    subject_kind,
    subject_id,
    grant_id,
    credential_scope,
    custody_ref,
    connection_ref,
    custody_hash,
    connection_hash,
    valid_from,
    valid_until
FROM provider_credential_handles
WHERE tenant_id = $1
  AND provider_id = $2
  AND credential_mode = $3
  AND subject_kind = $4
  AND subject_id = $5
  AND revoked_at IS NULL
  AND valid_from IS NOT NULL
  AND valid_until IS NOT NULL
  AND valid_from <= clock_timestamp()
  AND valid_until > clock_timestamp()
  AND custody_hash IS NOT NULL
  AND connection_hash IS NOT NULL
ORDER BY handle_id, handle_version
LIMIT 2`, tenantID, selection.ProviderID, string(credentialMode), selection.SubjectKind, selection.SubjectID)
	if err != nil {
		return credentialHandleRow{}, unavailableProfile("credential handle lookup failed", err)
	}
	defer func() { _ = rows.Close() }()

	handles := make([]credentialHandleRow, 0, 2)
	for rows.Next() {
		var row credentialHandleRow
		var mode string
		if err := rows.Scan(
			&row.HandleID,
			&row.HandleVersion,
			&row.ProviderID,
			&mode,
			&row.SubjectKind,
			&row.SubjectID,
			&row.GrantID,
			&row.Scope,
			&row.CustodyRef,
			&row.ConnectionRef,
			&row.PersistedCustodyHash,
			&row.PersistedConnectionHash,
			&row.ValidFrom,
			&row.ValidUntil,
		); err != nil {
			return credentialHandleRow{}, unavailableProfile("credential handle scan failed", err)
		}
		row.CredentialMode = CredentialMode(strings.TrimSpace(mode))
		handles = append(handles, row)
	}
	if err := rows.Err(); err != nil {
		return credentialHandleRow{}, unavailableProfile("credential handle read failed", err)
	}
	if len(handles) != 1 {
		return credentialHandleRow{}, unavailableProfile("exactly one unrevoked credential handle is required", nil)
	}
	row := handles[0]
	row.HandleID = strings.TrimSpace(row.HandleID)
	row.ProviderID = strings.TrimSpace(row.ProviderID)
	row.SubjectKind = strings.TrimSpace(row.SubjectKind)
	row.SubjectID = strings.TrimSpace(row.SubjectID)
	row.GrantID = strings.TrimSpace(row.GrantID)
	row.Scope = strings.TrimSpace(row.Scope)
	row.CustodyRef = strings.TrimSpace(row.CustodyRef)
	row.ConnectionRef = strings.TrimSpace(row.ConnectionRef)
	row.PersistedCustodyHash = strings.ToLower(strings.TrimSpace(row.PersistedCustodyHash))
	row.PersistedConnectionHash = strings.ToLower(strings.TrimSpace(row.PersistedConnectionHash))
	if !validCredentialHandleRow(row, selection, credentialMode) {
		return credentialHandleRow{}, unavailableProfile("credential handle is invalid", nil)
	}
	return row, nil
}

func validCredentialHandleRow(
	row credentialHandleRow,
	selection leaseProfileSelection,
	credentialMode CredentialMode,
) bool {
	if !validCredentialHandleIdentity(row) {
		return false
	}
	if row.ProviderID != selection.ProviderID || row.CredentialMode != credentialMode {
		return false
	}
	if row.SubjectKind != selection.SubjectKind || row.SubjectID != selection.SubjectID {
		return false
	}
	return executionProfileDigestPattern.MatchString(row.PersistedCustodyHash) &&
		executionProfileDigestPattern.MatchString(row.PersistedConnectionHash) &&
		!row.ValidFrom.IsZero() && row.ValidUntil.After(row.ValidFrom)
}

func validCredentialHandleIdentity(row credentialHandleRow) bool {
	if row.HandleID == "" || len(row.HandleID) > 256 || row.HandleVersion <= 0 {
		return false
	}
	if !validCredentialText(row.GrantID) || !validCredentialText(row.Scope) {
		return false
	}
	return validCredentialReferences(row)
}

func validCredentialText(value string) bool {
	return value != "" && len(value) <= 512
}

func validCredentialReferences(row credentialHandleRow) bool {
	if len(row.CustodyRef) > 512 || !custodyRefPattern.MatchString(row.CustodyRef) {
		return false
	}
	return len(row.ConnectionRef) <= 512 && connectionRefPattern.MatchString(row.ConnectionRef)
}

func assembleCatalogExecutionProfile(
	tenantID string,
	row catalogProfileRow,
	handle credentialHandleRow,
) (ExecutionProfile, error) {
	capabilityHash := sha256Digest(row.CapabilitySnapshot)
	custodyHash, err := credentialCustodyHash(tenantID, handle)
	if err != nil || custodyHash != handle.PersistedCustodyHash {
		return ExecutionProfile{}, unavailableProfile("credential custody digest mismatch", err)
	}
	connectionHash, err := credentialConnectionHash(tenantID, handle)
	if err != nil || connectionHash != handle.PersistedConnectionHash {
		return ExecutionProfile{}, unavailableProfile("provider connection digest mismatch", err)
	}
	managedBootstrapRequirements, declared, err := normalizeManagedBootstrapNetworkFromSnapshot(row.CapabilitySnapshot)
	if err != nil {
		return ExecutionProfile{}, unavailableProfile("managed bootstrap network capability is invalid", err)
	}
	var managedBootstrapNetwork *ManagedBootstrapNetworkProfile
	if declared {
		resolved, resolveErr := ManagedBootstrapNetworkProfileFor(row.ProviderID, row.AdapterID, managedBootstrapRequirements)
		if resolveErr != nil {
			return ExecutionProfile{}, unavailableProfile("managed bootstrap network execution profile is unavailable", resolveErr)
		}
		managedBootstrapNetwork = &resolved
	}
	executionProfileHash, err := stableProfileHash(struct {
		Version                string                `json:"version"`
		CatalogVersion         string                `json:"catalog_version"`
		ProviderID             string                `json:"provider_id"`
		AdapterID              string                `json:"adapter_id"`
		CredentialMode         CredentialMode        `json:"credential_mode"`
		RuntimeProfileID       string                `json:"runtime_profile_id"`
		OfferingID             string                `json:"offering_id"`
		CanPause               bool                  `json:"can_pause"`
		StopEffect             string                `json:"stop_effect"`
		CanRecreate            bool                  `json:"can_recreate"`
		CapabilitySnapshotHash string                `json:"capability_snapshot_hash"`
		AdapterManifestHash    string                `json:"adapter_manifest_hash"`
		ProvisionDispatchMode  ProvisionDispatchMode `json:"provision_dispatch_mode"`
		CustodyHash            string                `json:"custody_hash"`
		ConnectionHash         string                `json:"connection_hash"`
	}{
		Version: "providercontrol/execution-profile/v2", CatalogVersion: row.CatalogVersion,
		ProviderID: row.ProviderID, AdapterID: row.AdapterID, CredentialMode: row.CredentialMode,
		RuntimeProfileID: row.RuntimeProfileID, OfferingID: row.OfferingID,
		CanPause: row.CanPause, StopEffect: row.StopEffect, CanRecreate: row.CanRecreate,
		CapabilitySnapshotHash: capabilityHash, AdapterManifestHash: row.AdapterManifestHash,
		CustodyHash: custodyHash, ConnectionHash: connectionHash,
		ProvisionDispatchMode: row.ProvisionDispatchMode,
	})
	if err != nil {
		return ExecutionProfile{}, unavailableProfile("hash execution profile", err)
	}
	return ExecutionProfile{
		ProviderID: row.ProviderID, AdapterID: row.AdapterID,
		CredentialMode: row.CredentialMode, RuntimeProfileID: row.RuntimeProfileID,
		OfferingID: row.OfferingID, CatalogVersion: row.CatalogVersion,
		CapabilitySnapshotHash: capabilityHash,
		AdapterManifestHash:    row.AdapterManifestHash,
		ProvisionDispatchMode:  row.ProvisionDispatchMode,
		CustodyRef:             handle.CustodyRef, CustodyHash: custodyHash,
		ConnectionRef: handle.ConnectionRef, ConnectionHash: connectionHash,
		ManagedBootstrapNetwork: managedBootstrapNetwork,
		ExecutionProfileHash:    executionProfileHash,
	}, nil
}

func credentialCustodyHash(tenantID string, handle credentialHandleRow) (string, error) {
	return stableProfileHash(struct {
		Version        string         `json:"version"`
		TenantID       string         `json:"tenant_id"`
		HandleID       string         `json:"handle_id"`
		HandleVersion  int64          `json:"handle_version"`
		ProviderID     string         `json:"provider_id"`
		CredentialMode CredentialMode `json:"credential_mode"`
		SubjectKind    string         `json:"subject_kind"`
		SubjectID      string         `json:"subject_id"`
		GrantID        string         `json:"grant_id"`
		Scope          string         `json:"credential_scope"`
		CustodyRef     string         `json:"custody_ref"`
	}{
		Version: "providercontrol/custody-handle/v1", TenantID: tenantID,
		HandleID: handle.HandleID, HandleVersion: handle.HandleVersion,
		ProviderID: handle.ProviderID, CredentialMode: handle.CredentialMode,
		SubjectKind: handle.SubjectKind, SubjectID: handle.SubjectID,
		GrantID: handle.GrantID, Scope: handle.Scope, CustodyRef: handle.CustodyRef,
	})
}

func credentialConnectionHash(tenantID string, handle credentialHandleRow) (string, error) {
	return stableProfileHash(struct {
		Version        string         `json:"version"`
		TenantID       string         `json:"tenant_id"`
		HandleID       string         `json:"handle_id"`
		HandleVersion  int64          `json:"handle_version"`
		ProviderID     string         `json:"provider_id"`
		CredentialMode CredentialMode `json:"credential_mode"`
		ConnectionRef  string         `json:"connection_ref"`
	}{
		Version: "providercontrol/connection-handle/v1", TenantID: tenantID,
		HandleID: handle.HandleID, HandleVersion: handle.HandleVersion,
		ProviderID: handle.ProviderID, CredentialMode: handle.CredentialMode,
		ConnectionRef: handle.ConnectionRef,
	})
}

func normalizeCatalogProfileRequest(request ProfileRequest) (ProfileRequest, error) {
	request.TenantID = strings.TrimSpace(request.TenantID)
	request.LeaseID = strings.TrimSpace(request.LeaseID)
	request.RuntimeServerID = strings.TrimSpace(request.RuntimeServerID)
	request.ResourceGenerationID = strings.TrimSpace(request.ResourceGenerationID)
	if request.TenantID == "" || request.LeaseID == "" || request.LeaseRevision == 0 ||
		request.LeaseRevision > providerexecutor.MaxJSONSafeInteger ||
		request.RuntimeServerID == "" || !validOperation(request.Operation) {
		return ProfileRequest{}, fmt.Errorf("%w: complete lease authority context is required", ErrInvalidRequest)
	}
	generationID, err := uuid.Parse(request.ResourceGenerationID)
	if err != nil || generationID == uuid.Nil || generationID.String() != request.ResourceGenerationID {
		return ProfileRequest{}, fmt.Errorf("%w: resource generation must be a canonical UUID", ErrInvalidRequest)
	}
	return request, nil
}

func stableProfileHash(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return sha256Digest(payload), nil
}

func canonicalCatalogCapabilities(payload json.RawMessage) (json.RawMessage, error) {
	canonical, err := canonicalJSON(payload)
	if err != nil || len(canonical) > 16*1024 {
		return nil, unavailableProfile("capability snapshot is invalid", err)
	}
	var snapshot map[string]json.RawMessage
	if err := json.Unmarshal(canonical, &snapshot); err != nil || snapshot == nil {
		return nil, unavailableProfile("capability snapshot is invalid", err)
	}
	for key, value := range snapshot {
		switch key {
		case "regions", "zones", "instance_types", "architectures":
			if !validCapabilityStringArray(value) {
				return nil, unavailableProfile("capability snapshot is invalid", nil)
			}
		case "network":
			if !validCapabilityNetworkObject(value) {
				return nil, unavailableProfile("capability snapshot is invalid", nil)
			}
		case "storage":
			if !validStorageCapabilities(value) {
				return nil, unavailableProfile("capability snapshot is invalid", nil)
			}
		default:
			return nil, unavailableProfile("capability snapshot is invalid", nil)
		}
	}
	return canonical, nil
}

func validCapabilityStringArray(payload json.RawMessage) bool {
	var values []string
	if err := json.Unmarshal(payload, &values); err != nil || values == nil || len(values) > 32 {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !canonicalCatalogIdentifier(value) {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validStorageCapabilities(payload json.RawMessage) bool {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(payload, &values); err != nil || values == nil {
		return false
	}
	for key, raw := range values {
		switch key {
		case "volume_types":
			if !validCapabilityStringArray(raw) {
				return false
			}
		case "snapshots", "online_resize":
			if string(raw) == "null" {
				return false
			}
			var value bool
			if err := json.Unmarshal(raw, &value); err != nil {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func canonicalCatalogIdentifier(value string) bool {
	return catalogIdentifierPattern.MatchString(value) && value == strings.TrimSpace(value)
}

func canonicalProviderID(value string) bool {
	if !canonicalCatalogIdentifier(value) {
		return false
	}
	_, disallowed := disallowedCompositeProviderIDs[value]
	return !disallowed
}

func unavailableProfile(reason string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if err == nil {
		return fmt.Errorf("%w: %s", ErrProfileUnavailable, reason)
	}
	return fmt.Errorf("%w: %s: %v", ErrProfileUnavailable, reason, err)
}
