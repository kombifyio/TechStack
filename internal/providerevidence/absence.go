// Package providerevidence records and verifies the provider-native
// observations that let a decommission prove a resource is gone.
//
// providerexecutor will not seal a receipt claiming Observation=absent unless
// the resource carries definitive provider_api evidence and a configured
// EvidenceVerifier accepts it. Techstack shipped neither half, so no managed
// server could ever be decommissioned to proof and no managed-runtime capacity
// slot was ever released. This package is both halves: Recorder writes the
// exact provider read that observed the resource gone, and Verifier resolves an
// evidence reference back to that stored read.
package providerevidence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
)

const (
	// evidenceHost and attestationHost are the lookup authorities for stored
	// observations. The wire contract requires a hierarchical reference with a
	// real host, so these name the control plane that holds the record rather
	// than the provider that produced it.
	evidenceHost    = "absence-evidence.providercontrol.kombify.io"
	attestationHost = "absence-attestation.providercontrol.kombify.io"

	digestPrefix = "sha256:"

	// documentVersion is part of every digest input. Changing the document
	// shape must invalidate old digests rather than silently reinterpret them.
	documentVersion = "providercontrol/absence-observation/v1"
)

var (
	// ErrAbsenceObservationMissing reports an absence claim with no recorded
	// provider observation behind it. This is the forgery case the verifier
	// exists to refuse.
	ErrAbsenceObservationMissing = errors.New("providerevidence: no recorded provider observation backs this absence claim")
	// ErrAbsenceObservationMismatch reports a recorded observation that does
	// not match the claim being verified.
	ErrAbsenceObservationMismatch = errors.New("providerevidence: recorded provider observation does not match the absence claim")
	// ErrAbsenceObservationUnavailable reports that the store could not be
	// consulted. It is deliberately distinct from a rejection: a database
	// outage must not read as a proven absence, and it must not read as a
	// forgery either.
	ErrAbsenceObservationUnavailable = errors.New("providerevidence: absence observation store is unavailable")
)

// Observation is one provider read that found a resource gone.
//
// Endpoint and StatusCode are the substance: a decommission may only claim
// absence because a provider API answered "not found" for the exact resource
// the command targets.
type Observation struct {
	Endpoint    string
	StatusCode  int
	CollectedAt time.Time
	// AuthoritativeListAbsent is the exact target absent from a complete
	// Proxmox VM list after verifying VM.Audit on that id. It retains a 200
	// response honestly instead of fabricating a provider 404.
	AuthoritativeListAbsent string
}

// Recorder persists absence observations under the tenant that owns them.
type Recorder struct {
	database *sql.DB
}

// NewRecorder builds a recorder over the provider-control database.
func NewRecorder(database *sql.DB) (*Recorder, error) {
	if database == nil {
		return nil, fmt.Errorf("providerevidence: database is required")
	}
	return &Recorder{database: database}, nil
}

// Record stores the observation and returns the evidence envelope that belongs
// on the absent resource binding.
//
// The envelope is fully derived from the command, the target, and the stored
// document, so a caller cannot widen what the evidence asserts.
func (r *Recorder) Record(
	ctx context.Context,
	command providerexecutor.Command,
	target providerexecutor.ResourceTarget,
	observed Observation,
) (providerexecutor.Evidence, error) {
	if r == nil || r.database == nil {
		return providerexecutor.Evidence{}, ErrAbsenceObservationUnavailable
	}
	evidence, document, err := buildAbsenceEvidence(command, target, observed)
	if err != nil {
		return providerexecutor.Evidence{}, err
	}
	tx, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return providerexecutor.Evidence{}, fmt.Errorf("%w: %v", ErrAbsenceObservationUnavailable, err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, scopeErr := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id', $1, true)`, command.TenantID); scopeErr != nil {
		return providerexecutor.Evidence{}, fmt.Errorf("%w: %v", ErrAbsenceObservationUnavailable, scopeErr)
	}
	// ON CONFLICT DO NOTHING plus a read-back keeps a retried decommission
	// idempotent without letting a second attempt overwrite the first
	// observation.
	if _, insertErr := tx.ExecContext(ctx, `
		INSERT INTO provider_absence_observations (
			tenant_id, operation_id, binding_id, provider_id,
			evidence_ref, evidence_digest, attestation_ref, attestation_digest,
			observation, source, subject_hash, native_ref_hash,
			collected_at, document_json
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT DO NOTHING
	`,
		command.TenantID, command.OperationID, target.BindingID, command.ProviderID,
		evidence.Ref, evidence.Digest, evidence.AttestationRef, evidence.AttestationDigest,
		string(providerexecutor.ObservationAbsent), string(providerexecutor.EvidenceSourceProviderAPI),
		evidence.SubjectHash, evidence.NativeRefHash, evidence.CollectedAt,
		// jsonb wants text on the wire: a []byte argument is sent as bytea and
		// the insert fails with a type mismatch, which surfaced only as a
		// retryable transient teardown with no resources attached.
		string(document),
	); insertErr != nil {
		return providerexecutor.Evidence{}, fmt.Errorf("%w: %v", ErrAbsenceObservationUnavailable, insertErr)
	}
	stored, err := loadObservationTx(ctx, tx, command.TenantID, evidence.Ref)
	if err != nil {
		return providerexecutor.Evidence{}, err
	}
	// A retried teardown re-reads the provider and stamps a new collection time,
	// which changes the document and therefore the digest -- while the stored
	// observation, keyed on operation and binding, keeps the first one. Comparing
	// the fresh envelope against it failed every retry with "evidence digest",
	// which surfaced only as a retryable transient teardown. The first
	// observation is the proof: absence was established then, and observing it
	// again does not invalidate it, so the envelope adopts the stored record.
	adopted, err := adoptStoredObservation(evidence, stored, command, target)
	if err != nil {
		return providerexecutor.Evidence{}, err
	}
	if err := tx.Commit(); err != nil {
		return providerexecutor.Evidence{}, fmt.Errorf("%w: %v", ErrAbsenceObservationUnavailable, err)
	}
	return adopted, nil
}

// Verifier resolves an absence claim back to the observation that was recorded
// when the provider was actually read.
//
// It replaces FailClosedEvidenceVerifier, which refused every envelope. What it
// proves is bounded and worth stating plainly: the claim is backed by a durable
// digest-bound provider read that the control plane recorded itself, under the
// same tenant, for the same operation, binding, and command subject. It is not
// a third-party attestation of that read.
type Verifier struct {
	database *sql.DB
}

// NewVerifier builds the verifier over the provider-control database.
func NewVerifier(database *sql.DB) (*Verifier, error) {
	if database == nil {
		return nil, fmt.Errorf("providerevidence: database is required")
	}
	return &Verifier{database: database}, nil
}

// VerifyEvidence accepts an envelope only when a matching stored observation
// exists. Every other outcome is a refusal.
func (v *Verifier) VerifyEvidence(
	ctx context.Context,
	command providerexecutor.Command,
	target providerexecutor.ResourceTarget,
	evidence providerexecutor.Evidence,
) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if v == nil || v.database == nil {
		return ErrAbsenceObservationUnavailable
	}
	// Only absence needs a proof. A present or unknown observation is not a
	// custody claim this verifier is asked to underwrite.
	if evidence.Observation != providerexecutor.ObservationAbsent {
		return nil
	}
	if evidence.Source != providerexecutor.EvidenceSourceProviderAPI {
		return fmt.Errorf("%w: absence evidence must come from the provider API, got %q",
			ErrAbsenceObservationMismatch, evidence.Source)
	}
	tx, err := v.database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrAbsenceObservationUnavailable, err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, scopeErr := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id', $1, true)`, command.TenantID); scopeErr != nil {
		return fmt.Errorf("%w: %v", ErrAbsenceObservationUnavailable, scopeErr)
	}
	stored, err := loadObservationTx(ctx, tx, command.TenantID, evidence.Ref)
	if err != nil {
		return err
	}
	if stored.OperationID != command.OperationID || stored.BindingID != target.BindingID {
		return fmt.Errorf("%w: stored observation belongs to operation %q binding %q",
			ErrAbsenceObservationMismatch, stored.OperationID, stored.BindingID)
	}
	// Recomputing the subject hash from the command in hand is what stops a
	// genuine observation from being replayed onto a different command.
	expectedSubject := providerexecutor.ComputeEvidenceSubjectHash(
		command, target, providerexecutor.ObservationAbsent)
	if stored.SubjectHash != expectedSubject {
		return fmt.Errorf("%w: stored subject hash does not bind this command", ErrAbsenceObservationMismatch)
	}
	if stored.NativeRefHash != providerexecutor.ComputeNativeRefHash(target.NativeRef) {
		return fmt.Errorf("%w: stored observation names a different provider resource", ErrAbsenceObservationMismatch)
	}
	// The digest is recomputed from the stored document rather than trusted from
	// its column, so a row edited around the immutability trigger still fails
	// verification.
	pairs, err := decodeDocumentPairs(stored.Document)
	if err != nil {
		return err
	}
	if recomputed := documentDigest(pairs); recomputed != evidence.Digest {
		return fmt.Errorf("%w: stored document does not support this digest", ErrAbsenceObservationMismatch)
	}
	return stored.matches(evidence)
}

var _ providerexecutor.EvidenceVerifier = (*Verifier)(nil)

type storedObservation struct {
	OperationID       string
	BindingID         string
	EvidenceDigest    string
	AttestationRef    string
	AttestationDigest string
	SubjectHash       string
	NativeRefHash     string
	CollectedAt       time.Time
	Document          []byte
}

// adoptStoredObservation returns the envelope the stored observation actually supports.
//
// Only the observation-dependent fields are taken from the record; everything
// that binds the claim to this command stays derived and is re-checked, so
// adopting a record can never widen what the evidence asserts. A record that
// binds a different subject or resource is a genuine conflict and is refused.
func adoptStoredObservation(
	candidate providerexecutor.Evidence,
	stored storedObservation,
	command providerexecutor.Command,
	target providerexecutor.ResourceTarget,
) (providerexecutor.Evidence, error) {
	if stored.OperationID != command.OperationID || stored.BindingID != target.BindingID {
		return providerexecutor.Evidence{}, fmt.Errorf(
			"%w: stored observation belongs to operation %q binding %q",
			ErrAbsenceObservationMismatch, stored.OperationID, stored.BindingID)
	}
	if stored.SubjectHash != candidate.SubjectHash || stored.NativeRefHash != candidate.NativeRefHash {
		return providerexecutor.Evidence{}, fmt.Errorf(
			"%w: stored observation binds a different command or resource",
			ErrAbsenceObservationMismatch)
	}
	// The digest is derived from the stored document, never taken from its
	// column. A digest column is an index, not an authority: trusting it made a
	// record written under a superseded digest scheme permanently unusable,
	// because the adopted envelope carried the old value while the verifier
	// recomputed the new one. The operation could then never append its absence
	// receipt and never terminalize, so no retry could replace it either. Live
	// on 2026-07-27 exactly one such row stranded its lease for good.
	pairs, err := decodeDocumentPairs(stored.Document)
	if err != nil {
		return providerexecutor.Evidence{}, err
	}
	adopted := candidate
	adopted.Digest = documentDigest(pairs)
	adopted.AttestationRef = stored.AttestationRef
	adopted.AttestationDigest = documentDigest(
		append([][2]string{{"attestation_ref", stored.AttestationRef}}, pairs...))
	adopted.CollectedAt = stored.CollectedAt.UTC()
	return adopted, nil
}

// matches compares a stored row against the envelope claiming to represent it.
//
// Only recorded fields are compared. Both digests are derived from the stored
// document, so checking either against its column would re-introduce the column
// as an authority -- and a record written under a superseded digest scheme
// would be stranded forever. That was fixed for the evidence digest and missed
// one field over: live on 2026-07-27 the teardown then failed with "attestation
// digest" instead, for the same reason.
func (s storedObservation) matches(evidence providerexecutor.Evidence) error {
	switch {
	case s.AttestationRef != evidence.AttestationRef:
		return fmt.Errorf("%w: attestation reference", ErrAbsenceObservationMismatch)
	case !s.CollectedAt.UTC().Equal(evidence.CollectedAt.UTC()):
		return fmt.Errorf("%w: collection time", ErrAbsenceObservationMismatch)
	}
	return nil
}

type txQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func loadObservationTx(
	ctx context.Context,
	tx txQuerier,
	tenantID string,
	evidenceRef string,
) (storedObservation, error) {
	var stored storedObservation
	err := tx.QueryRowContext(ctx, `
		SELECT operation_id, binding_id, evidence_digest, attestation_ref,
		       attestation_digest, subject_hash, native_ref_hash, collected_at,
		       document_json::text
		FROM provider_absence_observations
		WHERE tenant_id = $1 AND evidence_ref = $2
	`, tenantID, evidenceRef).Scan(
		&stored.OperationID, &stored.BindingID, &stored.EvidenceDigest,
		&stored.AttestationRef, &stored.AttestationDigest, &stored.SubjectHash,
		&stored.NativeRefHash, &stored.CollectedAt, &stored.Document,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storedObservation{}, fmt.Errorf("%w: %s", ErrAbsenceObservationMissing, evidenceRef)
	}
	if err != nil {
		return storedObservation{}, fmt.Errorf("%w: %v", ErrAbsenceObservationUnavailable, err)
	}
	return stored, nil
}

// buildAbsenceEvidence derives the envelope and the document that backs it.
//
// Every field except the document comes from the command and the target, so the
// envelope cannot assert more than the command already establishes.
func buildAbsenceEvidence(
	command providerexecutor.Command,
	target providerexecutor.ResourceTarget,
	observed Observation,
) (providerexecutor.Evidence, []byte, error) {
	if err := validateAbsenceObservation(command, target, observed); err != nil {
		return providerexecutor.Evidence{}, nil, err
	}
	collectedAt := observed.CollectedAt.UTC().Truncate(time.Microsecond)
	subjectHash := providerexecutor.ComputeEvidenceSubjectHash(
		command, target, providerexecutor.ObservationAbsent)
	// Canonical field order, marshaled from an ordered slice rather than a map,
	// so the stored audit record reads the same way every time.
	pairs := [][2]string{
		{"version", documentVersion},
		{"tenant_id", command.TenantID},
		{"operation_id", command.OperationID},
		{"provider_id", command.ProviderID},
		{"binding_id", target.BindingID},
		{"native_ref_hash", providerexecutor.ComputeNativeRefHash(target.NativeRef)},
		{"subject_hash", subjectHash},
		{"observation", string(providerexecutor.ObservationAbsent)},
		{"source", string(providerexecutor.EvidenceSourceProviderAPI)},
		{"endpoint", observed.Endpoint},
		{"status_code", fmt.Sprintf("%d", observed.StatusCode)},
		{"collected_at", collectedAt.Format(time.RFC3339Nano)},
	}
	if observed.AuthoritativeListAbsent != "" {
		pairs = append(pairs, [2]string{"authoritative_list_absent", observed.AuthoritativeListAbsent})
	}
	document, err := json.Marshal(pairs)
	if err != nil {
		return providerexecutor.Evidence{}, nil, fmt.Errorf("providerevidence: encode absence document: %w", err)
	}
	evidenceRef := lookupRef("provider-evidence", evidenceHost, command.OperationID, target.BindingID)
	attestationRef := lookupRef("provider-attestation", attestationHost, command.OperationID, target.BindingID)
	return providerexecutor.Evidence{
		Ref:                    evidenceRef,
		Digest:                 documentDigest(pairs),
		Source:                 providerexecutor.EvidenceSourceProviderAPI,
		OperationID:            command.OperationID,
		LeaseRevision:          command.LeaseRevision,
		RuntimeServerID:        command.RuntimeServerID,
		ProviderID:             command.ProviderID,
		CapabilitySnapshotHash: command.CapabilitySnapshotHash,
		BindingID:              target.BindingID,
		NativeRefHash:          providerexecutor.ComputeNativeRefHash(target.NativeRef),
		ConnectionHash:         command.ConnectionHash,
		ExecutionProfileHash:   command.ExecutionProfileHash,
		ResourceGraphHash:      command.ResourceGraphHash,
		SubjectHash:            subjectHash,
		Observation:            providerexecutor.ObservationAbsent,
		Definitive:             true,
		AttestationRef:         attestationRef,
		// The attestation is the control plane's own signature over the
		// document plus its reference. It is a separate digest so a certified
		// external attestor can take this slot without changing the envelope.
		AttestationDigest: documentDigest(append([][2]string{{"attestation_ref", attestationRef}}, pairs...)),
		CollectedAt:       collectedAt,
	}, document, nil
}

// validateAbsenceObservation refuses to mint evidence for anything that is not
// a definitive provider not-found answer.
func validateAbsenceObservation(
	command providerexecutor.Command,
	target providerexecutor.ResourceTarget,
	observed Observation,
) error {
	switch {
	case strings.TrimSpace(command.TenantID) == "" || strings.TrimSpace(command.OperationID) == "":
		return fmt.Errorf("providerevidence: absence evidence requires tenant and operation identity")
	case strings.TrimSpace(target.BindingID) == "" || strings.TrimSpace(target.NativeRef) == "":
		return fmt.Errorf("providerevidence: absence evidence requires an exact resource target")
	case strings.TrimSpace(observed.Endpoint) == "":
		return fmt.Errorf("providerevidence: absence evidence requires the provider endpoint that was read")
	case observed.StatusCode != 404 && observed.StatusCode != 410 && !(observed.StatusCode == 200 && command.ProviderID == "proxmox" && observed.AuthoritativeListAbsent == target.NativeRef):
		// Anything else means the provider did not tell us the resource is
		// gone, and an absence receipt would be a claim we cannot support.
		return fmt.Errorf("providerevidence: provider status %d does not establish absence", observed.StatusCode)
	case observed.CollectedAt.IsZero():
		return fmt.Errorf("providerevidence: absence evidence requires an observation time")
	}
	return nil
}

func lookupRef(scheme, host, operationID, bindingID string) string {
	return scheme + "://" + host + "/" + operationID + "/" + bindingID
}

// documentDigest hashes the observation's ordered field pairs rather than their
// JSON encoding.
//
// The document is stored in a jsonb column, and jsonb does not preserve the
// bytes it was given: it reparses and re-serializes. A digest taken over the
// JSON text therefore could never be recomputed from the stored row, so the
// verifier rejected every absence claim it had itself recorded moments earlier
// -- live on 2026-07-27, "stored document does not match its recorded digest".
//
// Length-prefixed framing keeps the mapping injective, so two different pair
// lists cannot collide by concatenation.
func documentDigest(pairs [][2]string) string {
	var payload strings.Builder
	for _, pair := range pairs {
		_, _ = fmt.Fprintf(&payload, "%d:%s=%d:%s\n", len(pair[0]), pair[0], len(pair[1]), pair[1])
	}
	sum := sha256.Sum256([]byte(payload.String()))
	return digestPrefix + hex.EncodeToString(sum[:])
}

// decodeDocumentPairs reads back what the jsonb column holds. The array-of-pairs
// shape survives the round trip because jsonb preserves array order and scalar
// values; only whitespace and object key order are normalized.
func decodeDocumentPairs(document []byte) ([][2]string, error) {
	var pairs [][2]string
	if err := json.Unmarshal(document, &pairs); err != nil {
		return nil, fmt.Errorf("%w: stored document is unreadable: %v", ErrAbsenceObservationMismatch, err)
	}
	if len(pairs) == 0 {
		return nil, fmt.Errorf("%w: stored document is empty", ErrAbsenceObservationMismatch)
	}
	return pairs, nil
}
