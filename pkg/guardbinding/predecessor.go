package guardbinding

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Predecessor result recovery (Guard G04). When a server is bound again to a
// new worker of the same tenant, the Guard journal can still hold outcome
// evidence of commands the previous worker received on that server. That
// evidence is held, and keeps fencing conflicting work, because posting it
// under the new identity used to be refused and then pruned. A
// PredecessorRecovery is the control plane's short-lived, signed statement that
// the current worker may deliver the outcome revisions of exactly the listed
// prior command IDs of the same tenant and server. It is result-only: it admits
// no command, no mutation and no other worker's evidence, and it never extends
// a revoked credential, because the delivery is authenticated by the current
// worker's own credential. A cross-tenant predecessor is never covered: its
// evidence belongs to another tenant and waits for a receipted handoff.
const (
	// PredecessorRecoveryContractVersion names the recovery statement.
	PredecessorRecoveryContractVersion = "guard.predecessor-recovery/v1"
	// PredecessorRecoveryLifetime bounds how long a recovery statement admits a
	// delivery after its issue.
	PredecessorRecoveryLifetime = 10 * time.Minute
	// MaxPredecessorCommands bounds the prior commands one statement names.
	MaxPredecessorCommands = MaxKnownCommands

	// CodePredecessorScope refuses a delivery the recovery statement does not
	// cover, or a statement that does not name this Guard's binding.
	CodePredecessorScope = "predecessor_recovery_scope"

	predecessorSignatureDomain = "techstack.guard.predecessor-recovery/v1\x00"
)

// PredecessorCommand names one prior command and the worker it was offered to.
type PredecessorCommand struct {
	CommandID string `json:"command_id"`
	WorkerID  string `json:"worker_id"`
}

// PredecessorRecovery is the signed, result-only recovery statement for one
// current worker.
type PredecessorRecovery struct {
	ContractVersion string               `json:"contract_version"`
	TenantID        string               `json:"tenant_id"`
	WorkerID        string               `json:"worker_id"`
	ServerID        string               `json:"server_id"`
	Commands        []PredecessorCommand `json:"commands"`
	IssuedAt        time.Time            `json:"issued_at"`
	ExpiresAt       time.Time            `json:"expires_at"`
	KeyID           string               `json:"key_id,omitempty"`
	Signature       string               `json:"signature,omitempty"`
}

// NewPredecessorRecovery builds an unsigned statement for the current worker
// over the given prior commands, valid for PredecessorRecoveryLifetime.
func NewPredecessorRecovery(tenantID, workerID, serverID string, commands []PredecessorCommand, now time.Time) PredecessorRecovery {
	now = now.UTC().Truncate(time.Second)
	return PredecessorRecovery{
		ContractVersion: PredecessorRecoveryContractVersion,
		TenantID:        tenantID, WorkerID: workerID, ServerID: serverID,
		Commands: commands, IssuedAt: now, ExpiresAt: now.Add(PredecessorRecoveryLifetime),
	}
}

// SignPredecessorRecovery sets the statement's key id and signature.
func (s *Signer) SignPredecessorRecovery(r *PredecessorRecovery) error {
	if s == nil || r == nil {
		return fmt.Errorf("a predecessor recovery statement needs a signer")
	}
	r.KeyID, r.Signature = s.pin.KeyID, ""
	message, err := r.signedMessage()
	if err != nil {
		return err
	}
	r.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(s.key, message))
	return nil
}

// Verify checks the statement against the pinned keys and the binding that
// holds it. It is never accepted unsigned, for another tenant, worker or
// server, past its expiry, or for a lifetime longer than the contract allows;
// it never names the current worker's own commands.
func (r PredecessorRecovery) Verify(pins []PinnedKey, expect Expectation, now time.Time) *Denial {
	if len(pins) == 0 {
		return Denyf(CodePredecessorScope, "this Guard pins no control-plane signing key, so it accepts no predecessor recovery")
	}
	if r.Signature == "" {
		return Denyf(CodeBindingMismatch, "the predecessor recovery statement is unsigned")
	}
	public, denial := pinnedPublicKey(pins, r.KeyID)
	if denial != nil {
		return denial
	}
	signature, err := base64.RawURLEncoding.DecodeString(r.Signature)
	if err != nil {
		return Denyf(CodeBindingMismatch, "the predecessor recovery signature is malformed")
	}
	message, err := r.signedMessage()
	if err != nil || !ed25519.Verify(public, message, signature) {
		return Denyf(CodeBindingMismatch, "the predecessor recovery signature does not verify")
	}
	return r.check(expect, now)
}

// check validates the statement's scope and lifetime without its signature;
// the control plane uses it on a statement it signed itself.
func (r PredecessorRecovery) check(expect Expectation, now time.Time) *Denial {
	switch {
	case r.ContractVersion != PredecessorRecoveryContractVersion:
		return Denyf(CodeContractUnsupported, "predecessor recovery contract %q is not supported", r.ContractVersion)
	case r.TenantID == "" || r.TenantID != expect.TenantID:
		return Denyf(CodePredecessorScope, "the predecessor recovery names another tenant")
	case r.WorkerID == "" || r.WorkerID != expect.WorkerID:
		return Denyf(CodePredecessorScope, "the predecessor recovery names another worker")
	case r.ServerID == "" || r.ServerID != expect.ServerID:
		return Denyf(CodePredecessorScope, "the predecessor recovery names another server")
	case r.IssuedAt.IsZero() || !r.ExpiresAt.After(r.IssuedAt) || r.ExpiresAt.Sub(r.IssuedAt) > PredecessorRecoveryLifetime:
		return Denyf(CodePredecessorScope, "the predecessor recovery lifetime is not admitted")
	case !now.Before(r.ExpiresAt):
		return Denyf(CodeAuthorityExpired, "the predecessor recovery expired")
	case len(r.Commands) == 0 || len(r.Commands) > MaxPredecessorCommands:
		return Denyf(CodePredecessorScope, "the predecessor recovery must name between 1 and %d commands", MaxPredecessorCommands)
	}
	seen := make(map[string]bool, len(r.Commands))
	for _, c := range r.Commands {
		if strings.TrimSpace(c.CommandID) == "" || strings.TrimSpace(c.WorkerID) == "" || seen[c.CommandID] {
			return Denyf(CodePredecessorScope, "the predecessor recovery names a malformed or repeated command")
		}
		if c.WorkerID == r.WorkerID {
			return Denyf(CodePredecessorScope, "the predecessor recovery cannot name the current worker's own command")
		}
		seen[c.CommandID] = true
	}
	return nil
}

// Covers reports whether the statement admits delivering the outcome of one
// prior command that was offered to predecessorWorkerID.
func (r PredecessorRecovery) Covers(commandID, predecessorWorkerID string) bool {
	return slices.Contains(r.Commands, PredecessorCommand{CommandID: commandID, WorkerID: predecessorWorkerID})
}

// AdmitDelivery is the control plane's check of a delivery that presents the
// statement: signed by the current worker's key, for the authenticated binding,
// unexpired, and covering exactly this command and its prior worker.
func (r PredecessorRecovery) AdmitDelivery(pins []PinnedKey, expect Expectation, now time.Time, commandID, predecessorWorkerID string) *Denial {
	if denial := r.Verify(pins, expect, now); denial != nil {
		return denial
	}
	if !r.Covers(commandID, predecessorWorkerID) {
		return Denyf(CodePredecessorScope, "the predecessor recovery does not cover this command")
	}
	return nil
}

func (r PredecessorRecovery) signedMessage() ([]byte, error) {
	r.Signature = ""
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	canonical, err := Canonicalize(raw)
	if err != nil {
		return nil, err
	}
	return append([]byte(predecessorSignatureDomain), canonical...), nil
}

func pinnedPublicKey(pins []PinnedKey, keyID string) (ed25519.PublicKey, *Denial) {
	for _, pin := range pins {
		if pin.KeyID != keyID {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(pin.PublicKey)
		if err != nil || len(raw) != ed25519.PublicKeySize || KeyIDOf(raw) != pin.KeyID {
			return nil, Denyf(CodeBindingMismatch, "pinned key %q is malformed", pin.KeyID)
		}
		return raw, nil
	}
	return nil, Denyf(CodeBindingMismatch, "signed by key %q, which this Guard does not pin", keyID)
}
