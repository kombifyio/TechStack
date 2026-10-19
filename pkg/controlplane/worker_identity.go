package controlplane

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/guardcompat"
)

// Worker identity states. A redeemed enrollment starts issued_unconfirmed and
// becomes bound with the first request authenticated by a valid proof of
// possession; detach, retire and revoke end in revoked.
const (
	WorkerIdentityIssuedUnconfirmed = "issued_unconfirmed"
	WorkerIdentityBound             = "bound"
	WorkerIdentityRevoked           = "revoked"
	// WorkerIdentityUpgradePending is a key that a legacy bearer worker asked to
	// bind. It authenticates nothing until the owner approves that exact key
	// (a bearer credential alone never decides which key speaks for a server).
	WorkerIdentityUpgradePending = "upgrade_pending"
)

// IdentityBinding values published in worker and server state.
const (
	IdentityBindingHostKey      = "host_key"
	IdentityBindingLegacyBearer = "legacy_bearer"
)

const (
	// WorkerIdentityRedemptionWindow bounds how long after the first issue the
	// same pairing capability and attempt may still replace an unconfirmed
	// credential. The pairing token's own expiry may close it sooner.
	WorkerIdentityRedemptionWindow = guardcompat.BootstrapRedemptionWindow
	// WorkerIdentityRotationOverlap bounds how long the predecessor key stays
	// valid after a proven key rotation. It only has to cover requests already in
	// flight when the Guard switches keys; an emergency rotation uses no overlap.
	WorkerIdentityRotationOverlap = guardcompat.KeyRotationOverlap
	// WorkerProofPruneSkew is the clock-skew allowance between verifying replicas
	// and the database before a replay-ledger entry may be dropped.
	WorkerProofPruneSkew = 10 * time.Minute
	// WorkerIdentityUpgradeWindow is how long a pending key bind of a legacy
	// worker waits for the owner; afterwards it is replaceable and cannot be
	// approved.
	WorkerIdentityUpgradeWindow = guardcompat.LegacyKeyUpgradeWindow
)

var (
	ErrWorkerIdentityRevoked      = errors.New("controlplane: worker identity revoked")
	ErrWorkerIdentityBound        = errors.New("controlplane: worker identity is bound to another key")
	ErrWorkerIdentityWindowClosed = errors.New("controlplane: worker identity redemption window closed")
	ErrWorkerProofReplayed        = errors.New("controlplane: worker proof was already used")
	// ErrWorkerIdentityUpgradePending reports a bind of another key that still
	// waits for the owner.
	ErrWorkerIdentityUpgradePending = errors.New("controlplane: another key bind is awaiting owner approval")
)

// WorkerIdentity is the host-key binding of one worker. It holds only public
// material: the thumbprint and public JWK of the Guard key, never a secret.
type WorkerIdentity struct {
	TenantID             string
	WorkerID             string
	State                string
	Thumbprint           string
	PublicKeyJWK         string
	AttemptID            string
	PairingTokenHash     string
	CredentialEpoch      int64
	PreviousThumbprint   string
	PreviousPublicKeyJWK string
	PreviousValidUntil   *time.Time
	IssuedAt             time.Time
	ConfirmedAt          *time.Time
	RevokedAt            *time.Time
	UpdatedAt            time.Time
	// ApprovedBy and ApprovedAt record the owner who approved a legacy key
	// bind; they are empty for a key bound at pairing.
	ApprovedBy string
	ApprovedAt *time.Time
}

// AcceptsThumbprint reports whether a proof signed by the key with this
// thumbprint may authenticate the worker at now.
func (i WorkerIdentity) AcceptsThumbprint(thumbprint string, now time.Time) bool {
	if i.State == WorkerIdentityRevoked || i.State == WorkerIdentityUpgradePending || thumbprint == "" {
		return false
	}
	if thumbprint == i.Thumbprint {
		return true
	}
	return i.PreviousThumbprint != "" && thumbprint == i.PreviousThumbprint &&
		i.PreviousValidUntil != nil && now.Before(*i.PreviousValidUntil)
}

// WorkerIdentityIssue binds a Guard key at a freshly claimed pairing
// redemption. A newer owner-approved redemption replaces any earlier binding.
type WorkerIdentityIssue struct {
	TenantID         string
	WorkerID         string
	AttemptID        string
	PairingTokenHash string
	Thumbprint       string
	PublicKeyJWK     string
	// CredentialSHA256 is the hash of the credential returned with this
	// redemption; it becomes the worker's only valid credential.
	CredentialSHA256 string
	IssuedAt         time.Time
}

// WorkerIdentityReissue atomically replaces the unconfirmed credential of one
// redemption that the same pairing capability, attempt and key repeat.
type WorkerIdentityReissue struct {
	TenantID            string
	WorkerID            string
	AttemptID           string
	PairingTokenHash    string
	Thumbprint          string
	NewCredentialSHA256 string
	// Now is the decision time; Deadline is the latest moment the repetition
	// may still be accepted (the earlier of the redemption window and the
	// pairing capability's expiry).
	Now      time.Time
	Deadline time.Time
}

// WorkerIdentityBind asks to bind a key to a legacy bearer worker. The caller
// authenticated with the current credential and proved key possession, which
// only shows that it holds the bearer: the key stays pending until the owner
// approves it.
type WorkerIdentityBind struct {
	TenantID                 string
	WorkerID                 string
	AttemptID                string
	Thumbprint               string
	PublicKeyJWK             string
	ExpectedCredentialSHA256 string
	Now                      time.Time
}

// WorkerIdentityUpgradeDecision is the owner's decision on a pending key bind.
type WorkerIdentityUpgradeDecision struct {
	TenantID   string
	WorkerID   string
	Thumbprint string
	ActorID    string
	Now        time.Time
}

// WorkerIdentityRotation swaps the key of a bound worker. The caller proved
// possession of the current key and of the new key.
type WorkerIdentityRotation struct {
	TenantID      string
	WorkerID      string
	OldThumbprint string
	NewThumbprint string
	NewPublicJWK  string
	Now           time.Time
	Overlap       time.Duration
}

// WorkerIdentityStore persists Guard host-key bindings and the proof replay
// ledger. Implementations are tenant scoped and atomic per command.
type WorkerIdentityStore interface {
	// GetWorkerIdentity returns ErrNotFound for a worker without a binding.
	GetWorkerIdentity(ctx context.Context, tenantID, workerID string) (*WorkerIdentity, error)
	// GetWorkerIdentityByPairing resolves the redemption of a pairing capability.
	GetWorkerIdentityByPairing(ctx context.Context, tenantID, pairingTokenHash string) (*WorkerIdentity, error)
	IssueWorkerIdentity(ctx context.Context, command WorkerIdentityIssue) (*WorkerIdentity, error)
	ReissueUnconfirmedWorkerIdentity(ctx context.Context, command WorkerIdentityReissue) (*WorkerIdentity, error)
	// GetUnconfirmedWorkerIdentityByKey returns the redemption that issued this
	// host key to a worker of the stack and never reached confirmation: the
	// identity is still issued_unconfirmed, so no proof-authenticated request
	// (and therefore no heartbeat) ever arrived. It is how
	// a new capability redeemed from the same host recognizes the worker an
	// interrupted enrollment left behind. ErrNotFound when there is none.
	GetUnconfirmedWorkerIdentityByKey(ctx context.Context, tenantID, stackID, thumbprint string) (*WorkerIdentity, error)
	// BindWorkerIdentity records a legacy worker's key as pending. An identical
	// repeat returns the pending (or already bound) row; another key while one
	// is pending returns ErrWorkerIdentityUpgradePending.
	BindWorkerIdentity(ctx context.Context, command WorkerIdentityBind) (*WorkerIdentity, error)
	// ApproveWorkerIdentityUpgrade binds the pending key the owner named;
	// ErrNotFound when no such unexpired pending key exists.
	ApproveWorkerIdentityUpgrade(ctx context.Context, command WorkerIdentityUpgradeDecision) (*WorkerIdentity, error)
	// RejectWorkerIdentityUpgrade discards a pending key bind.
	RejectWorkerIdentityUpgrade(ctx context.Context, tenantID, workerID string) error
	// ListWorkerIdentityUpgrades returns the tenant's pending key binds.
	ListWorkerIdentityUpgrades(ctx context.Context, tenantID string) ([]WorkerIdentity, error)
	ConfirmWorkerIdentity(ctx context.Context, tenantID, workerID, thumbprint string, now time.Time) (*WorkerIdentity, error)
	RotateWorkerIdentity(ctx context.Context, command WorkerIdentityRotation) (*WorkerIdentity, error)
	// RevokeWorkerIdentity ends the binding; a worker without one is a no-op.
	RevokeWorkerIdentity(ctx context.Context, tenantID, workerID string, now time.Time) error
	// ConsumeWorkerProof records a proof identifier until replayUntil and
	// returns ErrWorkerProofReplayed when it was recorded before.
	ConsumeWorkerProof(ctx context.Context, tenantID, workerID, jti string, replayUntil time.Time) error
}

func workerCredentialHash(worker Worker) string {
	if hash := strings.TrimSpace(stringValue(worker.Resources[WorkerCredentialTokenSHA256ResourceKey])); hash != "" {
		return hash
	}
	return strings.TrimSpace(worker.TokenHash)
}

func normalizeIdentityScope(tenantID, workerID string) (string, string, error) {
	tenantID, workerID = strings.TrimSpace(tenantID), strings.TrimSpace(workerID)
	if tenantID == "" || workerID == "" {
		return "", "", fmt.Errorf("%w: tenant and worker are required", ErrConflict)
	}
	return tenantID, workerID, nil
}

func validateIdentityKey(thumbprint, jwk string) error {
	if strings.TrimSpace(thumbprint) == "" || strings.TrimSpace(jwk) == "" {
		return fmt.Errorf("%w: key thumbprint and public key are required", ErrConflict)
	}
	return nil
}

func cloneWorkerIdentity(identity WorkerIdentity) *WorkerIdentity {
	identity.PreviousValidUntil = cloneTime(identity.PreviousValidUntil)
	identity.ConfirmedAt = cloneTime(identity.ConfirmedAt)
	identity.RevokedAt = cloneTime(identity.RevokedAt)
	identity.ApprovedAt = cloneTime(identity.ApprovedAt)
	return &identity
}

func (s *MemoryStore) GetWorkerIdentity(_ context.Context, tenantID, workerID string) (*WorkerIdentity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	identity, ok := s.workerIdentities[workerKey(tenantID, workerID)]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneWorkerIdentity(identity), nil
}

func (s *MemoryStore) GetWorkerIdentityByPairing(_ context.Context, tenantID, pairingTokenHash string) (*WorkerIdentity, error) {
	pairingTokenHash = strings.TrimSpace(pairingTokenHash)
	s.mu.RLock()
	defer s.mu.RUnlock()
	if pairingTokenHash != "" {
		for _, identity := range s.workerIdentities {
			if identity.TenantID == tenantID && identity.PairingTokenHash == pairingTokenHash {
				return cloneWorkerIdentity(identity), nil
			}
		}
	}
	return nil, ErrNotFound
}

func (s *MemoryStore) GetUnconfirmedWorkerIdentityByKey(_ context.Context, tenantID, stackID, thumbprint string) (*WorkerIdentity, error) {
	tenantID, stackID, thumbprint = strings.TrimSpace(tenantID), strings.TrimSpace(stackID), strings.TrimSpace(thumbprint)
	if tenantID == "" || stackID == "" || thumbprint == "" {
		return nil, ErrNotFound
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var newest *WorkerIdentity
	for key, identity := range s.workerIdentities {
		worker, ok := s.worker[key]
		if identity.TenantID != tenantID || identity.State != WorkerIdentityIssuedUnconfirmed || identity.Thumbprint != thumbprint ||
			!ok || worker.StackID != stackID {
			continue
		}
		if newest == nil || identity.IssuedAt.After(newest.IssuedAt) {
			newest = cloneWorkerIdentity(identity)
		}
	}
	if newest == nil {
		return nil, ErrNotFound
	}
	return newest, nil
}

func (s *MemoryStore) IssueWorkerIdentity(_ context.Context, command WorkerIdentityIssue) (*WorkerIdentity, error) {
	tenantID, workerID, err := normalizeIdentityScope(command.TenantID, command.WorkerID)
	if err != nil {
		return nil, err
	}
	if err := validateIdentityKey(command.Thumbprint, command.PublicKeyJWK); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := workerKey(tenantID, workerID)
	worker, ok := s.worker[key]
	if !ok {
		return nil, ErrNotFound
	}
	if credential := strings.TrimSpace(command.CredentialSHA256); credential != "" {
		worker.Resources = cloneMap(worker.Resources)
		if worker.Resources == nil {
			worker.Resources = map[string]any{}
		}
		worker.Resources[WorkerCredentialTokenSHA256ResourceKey] = credential
		worker.UpdatedAt = s.now()
		s.worker[key] = worker
	}
	epoch := int64(1)
	if existing, ok := s.workerIdentities[key]; ok {
		epoch = existing.CredentialEpoch + 1
	}
	identity := WorkerIdentity{
		TenantID: tenantID, WorkerID: workerID, State: WorkerIdentityIssuedUnconfirmed,
		Thumbprint: command.Thumbprint, PublicKeyJWK: command.PublicKeyJWK,
		AttemptID: command.AttemptID, PairingTokenHash: command.PairingTokenHash,
		CredentialEpoch: epoch, IssuedAt: command.IssuedAt.UTC(), UpdatedAt: s.now(),
	}
	s.workerIdentities[key] = identity
	return cloneWorkerIdentity(identity), nil
}

func (s *MemoryStore) ReissueUnconfirmedWorkerIdentity(_ context.Context, command WorkerIdentityReissue) (*WorkerIdentity, error) {
	tenantID, workerID, err := normalizeIdentityScope(command.TenantID, command.WorkerID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := workerKey(tenantID, workerID)
	identity, ok := s.workerIdentities[key]
	worker, workerOK := s.worker[key]
	if !ok || !workerOK {
		return nil, ErrNotFound
	}
	if identity.State != WorkerIdentityIssuedUnconfirmed ||
		identity.PairingTokenHash == "" || identity.PairingTokenHash != command.PairingTokenHash ||
		identity.AttemptID == "" || identity.AttemptID != command.AttemptID ||
		identity.Thumbprint != command.Thumbprint {
		return nil, ErrConflict
	}
	if !command.Now.Before(command.Deadline) {
		return nil, ErrWorkerIdentityWindowClosed
	}
	worker.Resources = cloneMap(worker.Resources)
	if worker.Resources == nil {
		worker.Resources = map[string]any{}
	}
	worker.Resources[WorkerCredentialTokenSHA256ResourceKey] = command.NewCredentialSHA256
	worker.UpdatedAt = s.now()
	s.worker[key] = worker
	identity.CredentialEpoch++
	identity.UpdatedAt = s.now()
	s.workerIdentities[key] = identity
	return cloneWorkerIdentity(identity), nil
}

func (s *MemoryStore) BindWorkerIdentity(_ context.Context, command WorkerIdentityBind) (*WorkerIdentity, error) {
	tenantID, workerID, err := normalizeIdentityScope(command.TenantID, command.WorkerID)
	if err != nil {
		return nil, err
	}
	if err := validateIdentityKey(command.Thumbprint, command.PublicKeyJWK); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := workerKey(tenantID, workerID)
	worker, ok := s.worker[key]
	if !ok {
		return nil, ErrNotFound
	}
	if workerCredentialHash(worker) != strings.TrimSpace(command.ExpectedCredentialSHA256) {
		return nil, ErrConflict
	}
	now := command.Now.UTC()
	if existing, found := s.workerIdentities[key]; found {
		switch {
		case existing.State == WorkerIdentityRevoked:
			return nil, ErrWorkerIdentityRevoked
		case existing.Thumbprint == command.Thumbprint:
			if existing.State == WorkerIdentityUpgradePending && !now.Before(existing.IssuedAt.Add(WorkerIdentityUpgradeWindow)) {
				existing.IssuedAt, existing.UpdatedAt = now, s.now()
				s.workerIdentities[key] = existing
			}
			return cloneWorkerIdentity(existing), nil
		case existing.State == WorkerIdentityUpgradePending && now.Before(existing.IssuedAt.Add(WorkerIdentityUpgradeWindow)):
			return nil, ErrWorkerIdentityUpgradePending
		case existing.State != WorkerIdentityUpgradePending:
			return nil, ErrWorkerIdentityBound
		}
	}
	identity := WorkerIdentity{
		TenantID: tenantID, WorkerID: workerID, State: WorkerIdentityUpgradePending,
		Thumbprint: command.Thumbprint, PublicKeyJWK: command.PublicKeyJWK, AttemptID: command.AttemptID,
		CredentialEpoch: 1, IssuedAt: now, UpdatedAt: s.now(),
	}
	s.workerIdentities[key] = identity
	return cloneWorkerIdentity(identity), nil
}

func (s *MemoryStore) ApproveWorkerIdentityUpgrade(_ context.Context, command WorkerIdentityUpgradeDecision) (*WorkerIdentity, error) {
	tenantID, workerID, err := normalizeIdentityScope(command.TenantID, command.WorkerID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := workerKey(tenantID, workerID)
	identity, ok := s.workerIdentities[key]
	now := command.Now.UTC()
	if !ok || identity.State != WorkerIdentityUpgradePending || identity.Thumbprint != strings.TrimSpace(command.Thumbprint) ||
		!now.Before(identity.IssuedAt.Add(WorkerIdentityUpgradeWindow)) {
		return nil, ErrNotFound
	}
	identity.State, identity.ConfirmedAt = WorkerIdentityBound, &now
	identity.ApprovedBy, identity.ApprovedAt = strings.TrimSpace(command.ActorID), &now
	identity.UpdatedAt = s.now()
	s.workerIdentities[key] = identity
	return cloneWorkerIdentity(identity), nil
}

func (s *MemoryStore) RejectWorkerIdentityUpgrade(_ context.Context, tenantID, workerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := workerKey(tenantID, workerID)
	if identity, ok := s.workerIdentities[key]; ok && identity.State == WorkerIdentityUpgradePending {
		delete(s.workerIdentities, key)
	}
	return nil
}

func (s *MemoryStore) ListWorkerIdentityUpgrades(_ context.Context, tenantID string) ([]WorkerIdentity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []WorkerIdentity
	for _, identity := range s.workerIdentities {
		if identity.TenantID == tenantID && identity.State == WorkerIdentityUpgradePending {
			out = append(out, *cloneWorkerIdentity(identity))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].WorkerID < out[j].WorkerID })
	return out, nil
}

func (s *MemoryStore) ConfirmWorkerIdentity(_ context.Context, tenantID, workerID, thumbprint string, now time.Time) (*WorkerIdentity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := workerKey(tenantID, workerID)
	identity, ok := s.workerIdentities[key]
	if !ok {
		return nil, ErrNotFound
	}
	if identity.State == WorkerIdentityRevoked {
		return nil, ErrWorkerIdentityRevoked
	}
	if !identity.AcceptsThumbprint(thumbprint, now) {
		return nil, ErrConflict
	}
	if identity.State == WorkerIdentityIssuedUnconfirmed {
		confirmed := now.UTC()
		identity.State = WorkerIdentityBound
		identity.ConfirmedAt = &confirmed
		identity.UpdatedAt = s.now()
		s.workerIdentities[key] = identity
	}
	return cloneWorkerIdentity(identity), nil
}

func (s *MemoryStore) RotateWorkerIdentity(_ context.Context, command WorkerIdentityRotation) (*WorkerIdentity, error) {
	if err := validateIdentityKey(command.NewThumbprint, command.NewPublicJWK); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := workerKey(command.TenantID, command.WorkerID)
	identity, ok := s.workerIdentities[key]
	if !ok {
		return nil, ErrNotFound
	}
	switch {
	case identity.State == WorkerIdentityRevoked:
		return nil, ErrWorkerIdentityRevoked
	case identity.Thumbprint == command.NewThumbprint:
		return cloneWorkerIdentity(identity), nil
	case identity.State != WorkerIdentityBound || identity.Thumbprint != command.OldThumbprint:
		return nil, ErrConflict
	}
	validUntil := command.Now.UTC().Add(command.Overlap)
	identity.PreviousThumbprint, identity.PreviousPublicKeyJWK = identity.Thumbprint, identity.PublicKeyJWK
	identity.PreviousValidUntil = &validUntil
	identity.Thumbprint, identity.PublicKeyJWK = command.NewThumbprint, command.NewPublicJWK
	identity.UpdatedAt = s.now()
	s.workerIdentities[key] = identity
	return cloneWorkerIdentity(identity), nil
}

func (s *MemoryStore) RevokeWorkerIdentity(_ context.Context, tenantID, workerID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := workerKey(tenantID, workerID)
	identity, ok := s.workerIdentities[key]
	if !ok {
		return nil
	}
	if identity.State != WorkerIdentityRevoked {
		revoked := now.UTC()
		identity.State, identity.RevokedAt = WorkerIdentityRevoked, &revoked
		identity.UpdatedAt = s.now()
		s.workerIdentities[key] = identity
	}
	return nil
}

func (s *MemoryStore) ConsumeWorkerProof(_ context.Context, tenantID, workerID, jti string, replayUntil time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	prefix := workerKey(tenantID, workerID) + "\x00"
	for key, expires := range s.workerProofs {
		if strings.HasPrefix(key, prefix) && expires.Before(now.Add(-WorkerProofPruneSkew)) {
			delete(s.workerProofs, key)
		}
	}
	key := prefix + jti
	if _, seen := s.workerProofs[key]; seen {
		return ErrWorkerProofReplayed
	}
	s.workerProofs[key] = replayUntil
	return nil
}

var _ WorkerIdentityStore = (*MemoryStore)(nil)
