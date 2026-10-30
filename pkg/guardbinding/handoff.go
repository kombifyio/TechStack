package guardbinding

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Cross-tenant handoff (Guard G04). A server whose Guard journal still holds
// command evidence of one tenant may be bound to another tenant only after that
// evidence left the Guard's state: the prior tenant's custodian (its owner)
// confirms an export or a quarantine in the control plane and receives a
// one-time confirmation; on the server, `techstack agent handoff` writes the
// prior tenant's journal entries, outcomes and identity state to a root-only
// archive outside the Guard state, removes them from the state securely and
// records a receipt. The re-pairing Guard declares what it still holds and
// presents the receipt; the control plane refuses a cross-tenant binding while
// prior payloads are held or a receipt is missing, and consumes the custodian
// confirmation exactly once. The new tenant never receives a prior payload:
// nothing of it remains in the Guard state, and the receipt carries digests and
// counts only.
const (
	// TenantHandoffContractVersion names the confirmation and the receipt.
	TenantHandoffContractVersion = "guard.tenant-handoff/v1"
	// HandoffExport moves the prior payloads to an archive the custodian takes
	// off the server.
	HandoffExport = "export"
	// HandoffQuarantine keeps the prior payloads on the server in a root-only
	// quarantine directory that no Guard process reads.
	HandoffQuarantine = "quarantine"
	// TenantHandoffConfirmationLifetime bounds how long a custodian
	// confirmation can be redeemed by a rebind.
	TenantHandoffConfirmationLifetime = 72 * time.Hour
	// MaxHandoffReceipts bounds the receipts one rebind may present.
	MaxHandoffReceipts = 4

	// CodeTenantHandoffRequired refuses a binding to another tenant while the
	// Guard still holds a prior tenant's payloads or presents no receipt.
	CodeTenantHandoffRequired = "tenant_handoff_required"
	// CodeTenantHandoffInvalid refuses a malformed, expired, foreign or
	// already consumed handoff confirmation or receipt.
	CodeTenantHandoffInvalid = "tenant_handoff_invalid"

	confirmationTokenPrefix = "kgh1_"
	confirmationCodeBytes   = 32
)

// ValidHandoffDisposition reports whether disposition is export or quarantine.
func ValidHandoffDisposition(disposition string) bool {
	return disposition == HandoffExport || disposition == HandoffQuarantine
}

// HandoffConfirmation is what the custodian hands to the server operator. The
// code is a one-time secret; the control plane stores only its hash.
type HandoffConfirmation struct {
	TenantID    string    `json:"t"`
	HandoffID   string    `json:"h"`
	Code        string    `json:"c"`
	Disposition string    `json:"d"`
	ExpiresAt   time.Time `json:"x"`
}

// NewHandoffConfirmation creates a fresh confirmation for one prior tenant.
func NewHandoffConfirmation(tenantID, disposition string, now time.Time) (HandoffConfirmation, error) {
	if strings.TrimSpace(tenantID) == "" || !ValidHandoffDisposition(disposition) {
		return HandoffConfirmation{}, fmt.Errorf("a handoff confirmation needs a tenant and an export or quarantine disposition")
	}
	var id [16]byte
	var code [confirmationCodeBytes]byte
	if _, err := rand.Read(id[:]); err != nil {
		return HandoffConfirmation{}, err
	}
	if _, err := rand.Read(code[:]); err != nil {
		return HandoffConfirmation{}, err
	}
	return HandoffConfirmation{
		TenantID: strings.TrimSpace(tenantID), HandoffID: "gth_" + hex.EncodeToString(id[:]),
		Code: base64.RawURLEncoding.EncodeToString(code[:]), Disposition: disposition,
		ExpiresAt: now.UTC().Truncate(time.Second).Add(TenantHandoffConfirmationLifetime),
	}, nil
}

// Token encodes the confirmation as the single string the operator passes to
// `techstack agent handoff --confirmation`.
func (c HandoffConfirmation) Token() string {
	raw, _ := json.Marshal(c)
	return confirmationTokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
}

// ParseHandoffConfirmation decodes and checks a confirmation token.
func ParseHandoffConfirmation(token string, now time.Time) (HandoffConfirmation, error) {
	encoded, ok := strings.CutPrefix(strings.TrimSpace(token), confirmationTokenPrefix)
	if !ok {
		return HandoffConfirmation{}, fmt.Errorf("the handoff confirmation is not a %s token", TenantHandoffContractVersion)
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) > 1024 {
		return HandoffConfirmation{}, fmt.Errorf("the handoff confirmation is malformed")
	}
	var c HandoffConfirmation
	if err := json.Unmarshal(raw, &c); err != nil {
		return HandoffConfirmation{}, fmt.Errorf("the handoff confirmation is malformed")
	}
	if err := c.check(now); err != nil {
		return HandoffConfirmation{}, err
	}
	return c, nil
}

func (c HandoffConfirmation) check(now time.Time) error {
	code, err := base64.RawURLEncoding.DecodeString(c.Code)
	switch {
	case strings.TrimSpace(c.TenantID) == "" || !strings.HasPrefix(c.HandoffID, "gth_") || len(c.HandoffID) != 36:
		return fmt.Errorf("the handoff confirmation names no prior tenant or handoff")
	case err != nil || len(code) != confirmationCodeBytes:
		return fmt.Errorf("the handoff confirmation code is malformed")
	case !ValidHandoffDisposition(c.Disposition):
		return fmt.Errorf("the handoff confirmation names no export or quarantine disposition")
	case c.ExpiresAt.IsZero() || !now.Before(c.ExpiresAt):
		return fmt.Errorf("the handoff confirmation expired; ask the prior owner for a new one")
	}
	return nil
}

// ConfirmationHash is the stored form of a confirmation code.
func ConfirmationHash(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// ConfirmationMatches compares a presented code with a stored hash in constant
// time.
func ConfirmationMatches(code, storedHash string) bool {
	return storedHash != "" && subtle.ConstantTimeCompare([]byte(ConfirmationHash(code)), []byte(storedHash)) == 1
}

// TenantHandoffReceipt is the server's record that one prior tenant's payloads
// left the Guard state under a custodian confirmation. It carries counts and
// digests, never a payload.
type TenantHandoffReceipt struct {
	ContractVersion string `json:"contract_version"`
	PriorTenantID   string `json:"prior_tenant_id"`
	HandoffID       string `json:"handoff_id"`
	// Confirmation is the custodian's one-time code. The control plane checks
	// it against the stored hash and consumes it; it is excluded from Digest.
	Confirmation string `json:"confirmation"`
	Disposition  string `json:"disposition"`
	// Entries is how many journal entries of the prior tenant were moved.
	Entries int `json:"entries"`
	// CommandsDigest is the SHA-256 of the sorted, newline-joined command IDs.
	CommandsDigest string `json:"commands_digest"`
	// ArchiveDigest is the SHA-256 of the archive the payloads were written to.
	ArchiveDigest string    `json:"archive_digest"`
	RemovedAt     time.Time `json:"removed_at"`
}

// CommandsDigest is the digest a receipt names for a set of command IDs.
func CommandsDigest(commandIDs []string) string {
	ids := slices.Clone(commandIDs)
	slices.Sort(ids)
	sum := sha256.Sum256([]byte(strings.Join(ids, "\n")))
	return hex.EncodeToString(sum[:])
}

// Validate checks the receipt's shape.
func (r TenantHandoffReceipt) Validate() error {
	hexDigest := func(value string) bool {
		raw, err := hex.DecodeString(value)
		return err == nil && len(raw) == sha256.Size
	}
	switch {
	case r.ContractVersion != TenantHandoffContractVersion:
		return fmt.Errorf("handoff receipt contract %q is not supported", r.ContractVersion)
	case strings.TrimSpace(r.PriorTenantID) == "" || !strings.HasPrefix(r.HandoffID, "gth_") || r.Confirmation == "":
		return fmt.Errorf("the handoff receipt names no prior tenant, handoff or confirmation")
	case !ValidHandoffDisposition(r.Disposition):
		return fmt.Errorf("the handoff receipt names no export or quarantine disposition")
	case r.Entries < 0 || !hexDigest(r.CommandsDigest) || !hexDigest(r.ArchiveDigest) || r.RemovedAt.IsZero():
		return fmt.Errorf("the handoff receipt has no complete removal record")
	}
	return nil
}

// Digest is the canonical SHA-256 of the receipt without its confirmation
// code; the control plane records it as the consumed receipt.
func (r TenantHandoffReceipt) Digest() (string, error) {
	r.Confirmation = ""
	raw, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	return CanonicalDigest(raw)
}

// PriorCustody is what a re-pairing Guard declares about payloads of earlier
// bindings that are still, or were until a handoff, in its state. The control
// plane does not rely on it alone: AdmitRebind also takes the tenants the
// control plane itself linked to the pairing's host key.
type PriorCustody struct {
	// Contract is TenantHandoffContractVersion for a Guard that declares; a
	// declaration under another contract is refused.
	Contract string `json:"contract,omitempty"`
	// HeldTenants lists every tenant whose journal entries are still held.
	HeldTenants []string `json:"held_tenants,omitempty"`
	// HeldEntries counts those entries.
	HeldEntries int `json:"held_entries,omitempty"`
	// Receipts are the handoffs completed since the last binding.
	Receipts []TenantHandoffReceipt `json:"receipts,omitempty"`
}

// AdmitRebind decides whether a pairing may bind tenantID. custodyTenants are
// the other tenants whose workers already used the host key this pairing
// proved (the control plane's own record). Every such tenant, and every
// other tenant the Guard declares as still held, refuses the binding unless a
// receipt of that tenant is presented: a missing declaration or a deleted
// receipts file never passes a tenant the control plane knows. Held payloads
// of another tenant refuse even with a receipt. It returns the receipts whose
// confirmation the control plane must consume (bound to the same key) before
// the binding is issued; payloads of tenantID itself are no obstacle
// (same-tenant predecessor recovery delivers them). When the pairing proved a
// host key (keyProven), only receipts of tenants the control plane links to
// that key are consumed: any other receipt is not needed and is ignored, so a
// stale or already consumed receipt never blocks a pairing and a receipt
// copied from another server is never spent. Without a key proof every
// receipt of another tenant is consumed.
func (p *PriorCustody) AdmitRebind(tenantID string, custodyTenants []string, keyProven bool) ([]TenantHandoffReceipt, *Denial) {
	tenantID = strings.TrimSpace(tenantID)
	var declared PriorCustody
	if p != nil {
		declared = *p
		if declared.Contract != "" && declared.Contract != TenantHandoffContractVersion {
			return nil, Denyf(CodeContractUnsupported, "prior custody contract %q is not supported", declared.Contract)
		}
	}
	for _, held := range declared.HeldTenants {
		if strings.TrimSpace(held) != tenantID {
			return nil, Denyf(CodeTenantHandoffRequired, "this server still holds command evidence of another tenant; its owner must confirm an export or quarantine and the operator must run `techstack agent handoff` first")
		}
	}
	if len(declared.Receipts) > MaxHandoffReceipts {
		return nil, Denyf(CodeTenantHandoffInvalid, "at most %d handoff receipts are accepted", MaxHandoffReceipts)
	}
	linked := map[string]bool{}
	for _, custody := range custodyTenants {
		linked[strings.TrimSpace(custody)] = true
	}
	seen := map[string]bool{}
	covered := map[string]bool{}
	var consume []TenantHandoffReceipt
	for _, receipt := range declared.Receipts {
		if err := receipt.Validate(); err != nil {
			return nil, Denyf(CodeTenantHandoffInvalid, "%v", err)
		}
		if seen[receipt.HandoffID] {
			return nil, Denyf(CodeTenantHandoffInvalid, "a handoff receipt is repeated")
		}
		seen[receipt.HandoffID] = true
		if receipt.PriorTenantID != tenantID && (!keyProven || linked[receipt.PriorTenantID]) && !covered[receipt.PriorTenantID] {
			consume = append(consume, receipt)
			covered[receipt.PriorTenantID] = true
		}
	}
	for _, custody := range custodyTenants {
		if custody = strings.TrimSpace(custody); custody != "" && custody != tenantID && !covered[custody] {
			return nil, Denyf(CodeTenantHandoffRequired, "this server's key served another tenant; its owner must confirm an export or quarantine and the operator must run `techstack agent handoff` before it can be paired here")
		}
	}
	return consume, nil
}

// HandoffRepeatWindow bounds how long the same pairing may present an already
// consumed receipt again (a redemption whose response was lost).
const HandoffRepeatWindow = 15 * time.Minute

// HandoffConfirmationRecord is the control plane's stored confirmation as the
// consumption decision sees it.
type HandoffConfirmationRecord struct {
	TenantID           string
	ConfirmationSHA256 string
	Disposition        string
	// KeyThumbprint is the prior worker's host key at confirmation. A record
	// without one is never consumable.
	KeyThumbprint string
	ExpiresAt     time.Time
	// The consumption, when one happened.
	ReceiptDigest    string
	ReceiptedAt      *time.Time
	ConsumerTenantID string
	ConsumerWorkerID string
	ConsumerKey      string
}

// HandoffPresentation is one pairing presenting a receipt.
type HandoffPresentation struct {
	ConfirmationSHA256 string
	Disposition        string
	ReceiptDigest      string
	ConsumerTenantID   string
	ConsumerWorkerID   string
	// KeyThumbprint is the host key the pairing proved; empty without one.
	KeyThumbprint string
	Now           time.Time
}

// DecideHandoffConsumption is the binding rule of a consumed confirmation: the
// code and disposition match, it has not expired, the pairing proved the key
// the confirmation was issued for (a confirmation of another server, or one
// bound to no key, is refused), and the binding goes to another tenant. A consumed confirmation is
// accepted again only from the same tenant, worker and key with the same
// receipt, within HandoffRepeatWindow and before expiry. ok=false refuses.
func DecideHandoffConsumption(record HandoffConfirmationRecord, p HandoffPresentation) (repeat, ok bool) {
	switch {
	case record.ConfirmationSHA256 == "" || record.ConfirmationSHA256 != p.ConfirmationSHA256 || record.Disposition != p.Disposition:
		return false, false
	case record.ExpiresAt.IsZero() || !p.Now.Before(record.ExpiresAt):
		return false, false
	case record.KeyThumbprint == "" || record.KeyThumbprint != p.KeyThumbprint:
		// A confirmation is always bound to one server key; it is never a
		// wildcard that another server could spend.
		return false, false
	case strings.TrimSpace(p.ConsumerTenantID) == "" || strings.TrimSpace(p.ConsumerWorkerID) == "" ||
		p.ConsumerTenantID == record.TenantID || p.ReceiptDigest == "":
		return false, false
	case record.ReceiptedAt == nil:
		return false, true
	}
	same := record.ReceiptDigest == p.ReceiptDigest && record.ConsumerTenantID == p.ConsumerTenantID &&
		record.ConsumerWorkerID == p.ConsumerWorkerID && record.ConsumerKey == p.KeyThumbprint &&
		p.Now.Before(record.ReceiptedAt.Add(HandoffRepeatWindow))
	return same, same
}
