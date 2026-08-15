package guardbootstrap

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/pairingtoken"
	"github.com/kombifyio/techstack/pkg/workerauth"
)

// EnvEnrollmentSeed is the dedicated derivation secret. It is kept separate
// from the service-auth secret so that rotating one does not silently
// invalidate in-flight node enrolments derived from the other.
const EnvEnrollmentSeed = "TECHSTACK_GUARD_ENROLLMENT_SEED"

// enrolmentWindow bounds how long a provisioned node may take to redeem its
// capability. It is generous because a first boot can be delayed by image
// pulls and provider queues, but it is finite: a provisioning attempt that was
// abandoned days ago must not still be able to enrol a host.
const enrolmentWindow = 72 * time.Hour

var (
	// ErrEnrollmentSeedUnavailable means no derivation secret is configured.
	// Provisioning must fail rather than create a managed VM that can never
	// enrol, because such a VM becomes a ghost server with a live lease.
	ErrEnrollmentSeedUnavailable = errors.New("guardbootstrap: no enrolment derivation secret is configured")
	// ErrPublicOriginUnavailable means the control plane does not know its own
	// public origin, so a node would have nothing to call home to.
	ErrPublicOriginUnavailable = errors.New("guardbootstrap: control-plane public origin is not configured")
	// ErrStackOwnerUnknown means no server row carries an owner subject for this
	// stack, which the redemption path requires before it accepts an enrolment.
	ErrStackOwnerUnknown = errors.New("guardbootstrap: stack has no owner subject")
)

// EnrollmentRequest identifies the one provisioning attempt whose node is being
// prepared.
type EnrollmentRequest struct {
	TenantID    string
	OperationID string
	LeaseID     string
	StackID     string
	// Hostname optionally names the node at first boot (see CloudInitInput).
	Hostname        string
	HostPrepProfile HostPrepProfile
}

// EnrollmentIssuer mints the node's enrolment capability and renders its
// first-boot payload. It is provider-neutral: any adapter that can pass a
// cloud-init document on create uses it unchanged.
type EnrollmentIssuer struct {
	database *sql.DB
	seed     []byte
	origin   string
	now      func() time.Time
}

// NewEnrollmentIssuer resolves the derivation secret and public origin from the
// environment and fails closed when either is missing.
func NewEnrollmentIssuer(database *sql.DB, now func() time.Time) (*EnrollmentIssuer, error) {
	if database == nil {
		return nil, fmt.Errorf("guardbootstrap: database is required")
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	seed := enrollmentSeed()
	if len(seed) == 0 {
		return nil, ErrEnrollmentSeedUnavailable
	}
	origin, ok := publicOrigin()
	if !ok {
		return nil, ErrPublicOriginUnavailable
	}
	return &EnrollmentIssuer{database: database, seed: seed, origin: origin, now: now}, nil
}

// RenderPayload derives this attempt's pairing capability and returns the
// first-boot document. It performs no I/O at all.
//
// Purity is the contract, not an optimisation: AtMostOnceProvisionExecutor
// requires PrepareProvision to be side-effect-free, and the document is folded
// into the prepared request digest. Deriving rather than generating the
// capability is what makes a provisioning replay reproduce identical bytes; a
// random token would change the digest, the dispatch guard would reject the
// replay, and an already-created VM would be stranded.
//
// The capability is not redeemable until RecordCapability has run.
func (i *EnrollmentIssuer) RenderPayload(request EnrollmentRequest) ([]byte, error) {
	rawToken, _, err := i.derive(request)
	if err != nil {
		return nil, err
	}
	return RenderCloudInit(CloudInitInput{ServerURL: i.origin, PairingToken: rawToken, Hostname: request.Hostname, HostPrepProfile: request.HostPrepProfile})
}

// DeriveNodeHostname builds a provider-neutral, deterministic first-boot
// hostname from the stack name and the lease identity, e.g.
// "demo-cloud-3f9a2b". Without it, provider images report a generic hostname
// ("ubuntu") that becomes the server display name. The suffix keeps two nodes
// of the same stack distinct; determinism keeps provisioning replays stable.
func DeriveNodeHostname(stackName, leaseID string) string {
	base := sanitizeHostnameLabel(stackName)
	if base == "" {
		base = "kombify"
	}
	if len(base) > 52 {
		base = strings.Trim(base[:52], "-")
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(leaseID)))
	return base + "-" + hex.EncodeToString(sum[:3])
}

// sanitizeHostnameLabel lowers a free-form name into RFC-1123 label characters.
func sanitizeHostnameLabel(value string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			builder.WriteRune(r)
		case r == '-' || r == ' ' || r == '_' || r == '.':
			builder.WriteRune('-')
		}
	}
	return strings.Trim(builder.String(), "-")
}

// RecordCapability makes the derived capability redeemable. It belongs on the
// dispatch path rather than the preparation path: dispatch is the method
// allowed to have effects, and a node cannot boot and redeem before the create
// call it is part of has been sent.
//
// The write is insert-once. ON CONFLICT DO NOTHING is deliberate — the existing
// UpsertPairingToken path resets used_at and status from the excluded row,
// which on a replay would re-open a capability the node had already spent.
func (i *EnrollmentIssuer) RecordCapability(ctx context.Context, request EnrollmentRequest) error {
	_, tokenHash, err := i.derive(request)
	if err != nil {
		return err
	}
	return i.recordCapability(ctx, request, tokenHash)
}

// derive is the single definition of this attempt's capability, so the payload
// and the recorded hash can never disagree.
func (i *EnrollmentIssuer) derive(request EnrollmentRequest) (rawToken, tokenHash string, err error) {
	if i == nil || i.database == nil {
		return "", "", fmt.Errorf("guardbootstrap: enrolment issuer is not configured")
	}
	tenantID := strings.TrimSpace(request.TenantID)
	operationID := strings.TrimSpace(request.OperationID)
	if tenantID == "" || operationID == "" || strings.TrimSpace(request.StackID) == "" {
		return "", "", fmt.Errorf("guardbootstrap: tenant, operation and stack are required")
	}
	rawToken, tokenHash, deriveErr := pairingtoken.Derive(i.seed, tenantID, "provision:"+operationID)
	if deriveErr != nil {
		// Never wrap: a derive error must not carry partial key material.
		return "", "", fmt.Errorf("guardbootstrap: derive enrolment capability")
	}
	return rawToken, tokenHash, nil
}

func (i *EnrollmentIssuer) recordCapability(
	ctx context.Context, request EnrollmentRequest, tokenHash string,
) error {
	metadata, err := json.Marshal(map[string]string{
		"lease_id":     strings.TrimSpace(request.LeaseID),
		"operation_id": strings.TrimSpace(request.OperationID),
		"source":       "managed-provision-cloud-init",
	})
	if err != nil {
		return fmt.Errorf("guardbootstrap: encode capability metadata: %w", err)
	}

	tx, err := i.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT set_config($1, $2, true)`, "app.tenant_id", request.TenantID); err != nil {
		return err
	}

	// Read the owner from servers, not stacks. The provider-control runtime
	// role holds a deliberately narrow, explicitly enumerated grant list that
	// includes servers but not stacks, and widening it for one column would
	// hand the provisioning path read access to every stack in the database.
	var owner sql.NullString
	err = tx.QueryRowContext(ctx,
		`SELECT owner_subject_id FROM servers
		 WHERE tenant_id = $1 AND stack_id = $2
		 ORDER BY id LIMIT 1`,
		request.TenantID, request.StackID,
	).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrStackOwnerUnknown
	}
	if err != nil {
		return fmt.Errorf("guardbootstrap: resolve server owner: %w", err)
	}
	if !owner.Valid || strings.TrimSpace(owner.String) == "" {
		return ErrStackOwnerUnknown
	}

	issuedAt := i.now().UTC()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO pairing_tokens (
    id, tenant_id, stack_id, owner_subject_id, name, token_hash,
    status, expires_at, metadata_json
) VALUES ($1, $2, $3, $4, $5, $6, 'active', $7, $8::jsonb)
ON CONFLICT (tenant_id, token_hash) DO NOTHING`,
		capabilityID(tokenHash), request.TenantID, request.StackID, strings.TrimSpace(owner.String),
		"managed-provision "+request.OperationID, tokenHash,
		issuedAt.Add(enrolmentWindow), string(metadata),
	); err != nil {
		return fmt.Errorf("guardbootstrap: record enrolment capability: %w", err)
	}
	return tx.Commit()
}

// capabilityID derives a stable primary key so a replay collides on the row it
// already wrote rather than on an unrelated one.
func capabilityID(tokenHash string) string {
	if len(tokenHash) < 24 {
		return "pt_" + tokenHash
	}
	return "pt_" + tokenHash[:24]
}

func enrollmentSeed() []byte {
	if raw := strings.TrimSpace(os.Getenv(EnvEnrollmentSeed)); raw != "" {
		return []byte(raw)
	}
	// Fall back to the worker-token secret so an existing deployment keeps
	// working; the derivation domain separates the two uses.
	return workerauth.SecretFromEnv()
}

// publicOrigin mirrors the probe order documented on the tunnel resolver: an
// explicitly configured hostname always wins over anything Render injects.
func publicOrigin() (string, bool) {
	for _, key := range []string{"TECHSTACK_PUBLIC_ORIGIN", "TECHSTACK_PUBLIC_URL", "RENDER_EXTERNAL_URL"} {
		if raw := strings.TrimSpace(os.Getenv(key)); raw != "" {
			if origin, err := normalizeOrigin(raw); err == nil {
				return origin, true
			}
		}
	}
	if host := strings.TrimSpace(os.Getenv("RENDER_EXTERNAL_HOSTNAME")); host != "" {
		if origin, err := normalizeOrigin("https://" + host); err == nil {
			return origin, true
		}
	}
	return "", false
}
