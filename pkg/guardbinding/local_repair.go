package guardbinding

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/guardcompat"
)

// Local repair authority (Guard G07). An owner's approval of automatic repair
// reaches the Guard as a LocalRepairGrant: a signed statement of one registered
// policy version that names the target (tenant, worker, server, credential
// generation), the one action it admits, its controls, the StackKits release,
// its expiry and the persisted repair bounds with the server's recent repairs.
// The Guard holds it in memory only. It admits a repair on its own while the
// control plane is unreachable, never longer than LocalOfflineLifetime after
// the grant was issued, never past the policy's expiry, and only while the
// local clock still continues from the receipt. Without a held grant, after a
// restart, or with an untrusted clock the Guard only observes. It cannot renew
// or widen a grant: only the control plane issues one, on an online exchange.
const (
	// LocalRepairContractVersion names the grant and the executor admission of a
	// local-policy repair.
	LocalRepairContractVersion = "guard.local-repair/v1"
	// LocalRepairAction is the only action a local policy admits: the typed
	// StackKits host-security repair of named controls. There is no shell and
	// no other executor behind a local policy.
	LocalRepairAction = "stackkit.host-security-repair"
	// LocalOfflineLifetime bounds how long after its issue a grant admits a
	// repair: the G01 command authority lifetime, which is also the bound on an
	// unobserved revocation.
	LocalOfflineLifetime = guardcompat.CommandAuthorityLifetime
	// MaxLocalRepairHistory bounds the recent repairs a grant carries.
	MaxLocalRepairHistory = 64

	// CodeLocalAuthorityMissing refuses a local repair when this Guard holds no
	// current grant: without authority it is observe-only.
	CodeLocalAuthorityMissing = "local_authority_missing"
	// CodeLocalScope refuses a local repair the grant does not cover.
	CodeLocalScope = "local_policy_scope"

	localGrantSignatureDomain = "techstack.guard.local-repair-grant/v1\x00"
	localOperationPrefix      = "local:"
)

var localPolicyIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// LocalReleasePin is the StackKits release a local repair runs on. The
// privileged executor still checks it against the release it verified itself.
type LocalReleasePin struct {
	Version            string `json:"version"`
	PlatformOS         string `json:"platform_os"`
	PlatformArch       string `json:"platform_arch"`
	ArchiveSHA256      string `json:"archive_sha256"`
	ReleaseIndexSHA256 string `json:"release_index_sha256"`
}

// LocalRepairBounds are the persisted repair bounds of the control plane: the
// cooldown per control, the daily attempt budget per control and the server's
// circuit breaker.
type LocalRepairBounds struct {
	CooldownSeconds    int64 `json:"cooldown_seconds"`
	MaxAttemptsPerDay  int   `json:"max_attempts_per_day"`
	BreakerFailures    int   `json:"breaker_failures"`
	BreakerOpenSeconds int64 `json:"breaker_open_seconds"`
}

// LocalRepairRecord is one recent repair of the server as the control plane
// persisted it; the Guard counts it toward the bounds together with its own
// unreported local repairs.
type LocalRepairRecord struct {
	OperationID string     `json:"operation_id,omitempty"`
	Controls    []string   `json:"controls"`
	RequestedAt time.Time  `json:"requested_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	Failed      bool       `json:"failed,omitempty"`
}

// LocalRepairGrant is the control plane's signed statement of one registered
// local repair policy version for one worker.
type LocalRepairGrant struct {
	ContractVersion string              `json:"contract_version"`
	PolicyID        string              `json:"policy_id"`
	PolicyVersion   int64               `json:"policy_version"`
	TenantID        string              `json:"tenant_id"`
	WorkerID        string              `json:"worker_id"`
	ServerID        string              `json:"server_id,omitempty"`
	Generation      int64               `json:"generation"`
	Action          string              `json:"action"`
	Controls        []string            `json:"controls"`
	Release         LocalReleasePin     `json:"release"`
	PolicyExpiresAt time.Time           `json:"policy_expires_at"`
	IssuedAt        time.Time           `json:"issued_at"`
	Bounds          LocalRepairBounds   `json:"bounds"`
	History         []LocalRepairRecord `json:"history"`
	KeyID           string              `json:"key_id,omitempty"`
	Signature       string              `json:"signature,omitempty"`
}

// AuthorityUntil is the end of the grant's authority: the earlier of the policy
// expiry and the offline lifetime after its issue.
func (g LocalRepairGrant) AuthorityUntil() time.Time {
	until := g.IssuedAt.Add(LocalOfflineLifetime)
	if g.PolicyExpiresAt.Before(until) {
		return g.PolicyExpiresAt
	}
	return until
}

// NewLocalOperationID is the identity of one local repair under this grant:
// it names the policy version, so the executor and the control plane can tell
// which authority it ran under. nonce must be unique per operation.
func (g LocalRepairGrant) NewLocalOperationID(nonce string) string {
	return localOperationPrefix + g.PolicyID + ":v" + strconv.FormatInt(g.PolicyVersion, 10) + ":" + nonce
}

// OwnsOperation reports whether an operation ID was made under this grant's
// policy version.
func (g LocalRepairGrant) OwnsOperation(operationID string) bool {
	nonce, ok := strings.CutPrefix(operationID, g.NewLocalOperationID(""))
	return ok && nonce != "" && len(operationID) <= 128 && !strings.ContainsAny(nonce, ":/ ")
}

// SignLocalGrant sets the grant's key id and signature.
func (s *Signer) SignLocalGrant(g *LocalRepairGrant) error {
	if s == nil || g == nil {
		return fmt.Errorf("a local repair grant needs a signer")
	}
	g.KeyID, g.Signature = s.pin.KeyID, ""
	message, err := g.signedMessage()
	if err != nil {
		return err
	}
	g.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(s.key, message))
	return nil
}

// VerifySignature checks the grant against the pinned keys. Unlike a command
// binding, a grant is never admitted unsigned: without a pinned key a Guard
// holds no local repair authority.
func (g LocalRepairGrant) VerifySignature(pins []PinnedKey) *Denial {
	if len(pins) == 0 {
		return Denyf(CodeLocalAuthorityMissing, "this Guard pins no control-plane signing key, so it holds no local repair authority")
	}
	if g.Signature == "" {
		return Denyf(CodeBindingMismatch, "the local repair grant is unsigned")
	}
	var public ed25519.PublicKey
	for _, pin := range pins {
		if pin.KeyID != g.KeyID {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(pin.PublicKey)
		if err != nil || len(raw) != ed25519.PublicKeySize || KeyIDOf(raw) != pin.KeyID {
			return Denyf(CodeBindingMismatch, "pinned key %q is malformed", pin.KeyID)
		}
		public = raw
	}
	if public == nil {
		return Denyf(CodeBindingMismatch, "the local repair grant is signed by key %q, which this Guard does not pin", g.KeyID)
	}
	signature, err := base64.RawURLEncoding.DecodeString(g.Signature)
	if err != nil {
		return Denyf(CodeBindingMismatch, "the local repair grant signature is malformed")
	}
	message, err := g.signedMessage()
	if err != nil || !ed25519.Verify(public, message, signature) {
		return Denyf(CodeBindingMismatch, "the local repair grant signature does not verify")
	}
	return nil
}

func (g LocalRepairGrant) signedMessage() ([]byte, error) {
	g.Signature = ""
	raw, err := json.Marshal(g)
	if err != nil {
		return nil, err
	}
	canonical, err := Canonicalize(raw)
	if err != nil {
		return nil, err
	}
	return append([]byte(localGrantSignatureDomain), canonical...), nil
}

// verify is the check both sides of the Guard share: a signed grant of this
// contract, well formed, for the one local action.
func (g LocalRepairGrant) verify(pins []PinnedKey) *Denial {
	if denial := g.VerifySignature(pins); denial != nil {
		return denial
	}
	switch {
	case g.ContractVersion != LocalRepairContractVersion:
		return Denyf(CodeContractUnsupported, "local repair contract %q is not supported", g.ContractVersion)
	case !localPolicyIDPattern.MatchString(g.PolicyID) || g.PolicyVersion < 1 || g.Generation < 1:
		return Denyf(CodeBindingMismatch, "the local repair grant names no complete policy binding")
	case g.TenantID == "" || g.WorkerID == "":
		return Denyf(CodeBindingMismatch, "the local repair grant names no worker")
	case g.Action != LocalRepairAction:
		return Denyf(CodeLocalScope, "action %q cannot run under a local policy", g.Action)
	case len(g.Controls) == 0:
		return Denyf(CodeLocalScope, "the local repair grant covers no control")
	case g.PolicyExpiresAt.IsZero() || g.IssuedAt.IsZero() || !g.IssuedAt.Before(g.PolicyExpiresAt):
		return Denyf(CodeAuthorityExpired, "the local repair grant has no lifetime left")
	case len(g.History) > MaxLocalRepairHistory:
		return Denyf(CodeBindingMismatch, "the local repair grant carries too much history")
	}
	return nil
}

func (g LocalRepairGrant) covers(controls []string) *Denial {
	if len(controls) == 0 {
		return Denyf(CodeLocalScope, "a local repair names at least one control")
	}
	seen := map[string]bool{}
	for _, id := range controls {
		if seen[id] || !slices.Contains(g.Controls, id) {
			return Denyf(CodeLocalScope, "control %q is not covered by policy version %d", id, g.PolicyVersion)
		}
		seen[id] = true
	}
	return nil
}

// HeldLocalGrant is a grant this Guard verified when it received it online,
// with the wall and monotonic clock readings of that receipt. It lives only in
// memory: after a restart there is no trusted remaining lifetime.
type HeldLocalGrant struct {
	Grant        LocalRepairGrant
	ReceivedAt   time.Time
	ReceivedMono time.Duration
}

// ReceiveLocalGrant verifies a grant at receipt. The grant must be signed by a
// pinned key, name this Guard and a credential generation no older than the
// highest it has seen, and its issue time must agree with the local clock
// within ResultClockMargin: a clock that disagrees with the control plane is
// not trusted to measure the grant's lifetime.
func ReceiveLocalGrant(g LocalRepairGrant, pins []PinnedKey, expect Expectation, now time.Time, mono time.Duration) (*HeldLocalGrant, *Denial) {
	if denial := g.verify(pins); denial != nil {
		return nil, denial
	}
	switch {
	case expect.TenantID != "" && g.TenantID != expect.TenantID:
		return nil, Denyf(CodeBindingMismatch, "the local repair grant is bound to another tenant")
	case expect.WorkerID != "" && g.WorkerID != expect.WorkerID:
		return nil, Denyf(CodeBindingMismatch, "the local repair grant is bound to another worker")
	case expect.ServerID != "" && g.ServerID != "" && g.ServerID != expect.ServerID:
		return nil, Denyf(CodeBindingMismatch, "the local repair grant is bound to another server")
	case g.Generation < expect.HighestGeneration:
		return nil, Denyf(CodeStaleGeneration, "the local repair grant names generation %d, older than %d", g.Generation, expect.HighestGeneration)
	}
	if skew := now.Sub(g.IssuedAt); skew > ResultClockMargin || skew < -ResultClockMargin {
		return nil, Denyf(CodeRevalidationRequired, "the local clock differs from the control plane by %s", skew.Round(time.Second))
	}
	return &HeldLocalGrant{Grant: g, ReceivedAt: now, ReceivedMono: mono}, nil
}

// Admit is the pre-effect check of a local repair on the Guard: a held grant,
// no observed revocation, the same Guard identity, controls the grant covers,
// and authority left on a clock that still continues from the receipt. A nil
// grant is observe-only.
func (h *HeldLocalGrant) Admit(state EffectState, controls []string) *Denial {
	if h == nil {
		return Denyf(CodeLocalAuthorityMissing, "this Guard holds no current local repair authority")
	}
	if state.Revoked {
		return Denyf(CodeAuthorityRevoked, "this Guard observed that its identity was revoked")
	}
	g := h.Grant
	switch {
	case state.Expect.TenantID != "" && g.TenantID != state.Expect.TenantID,
		state.Expect.WorkerID != "" && g.WorkerID != state.Expect.WorkerID:
		return Denyf(CodeBindingMismatch, "the local repair grant is bound to another worker")
	case g.Generation < state.Expect.HighestGeneration:
		return Denyf(CodeStaleGeneration, "the local repair grant names an older credential generation")
	}
	if denial := g.covers(controls); denial != nil {
		return denial
	}
	// The window starts at receipt but never extends past the grant's own end.
	window := AuthorityWindow{ExpiresAt: g.AuthorityUntil(), ReceivedAt: h.ReceivedAt, ReceivedMono: h.ReceivedMono}
	return window.Check(state.Now, state.Mono)
}

// AdmitAtExecutor is the privileged executor's own check of a local repair: a
// signed grant from a pinned key, an operation ID made under that policy
// version, controls it covers, and a start inside the grant's lifetime on the
// executor's clock (with the result clock margin for the daemon's receipt).
func (g LocalRepairGrant) AdmitAtExecutor(pins []PinnedKey, operationID string, controls []string, now time.Time) *Denial {
	if denial := g.verify(pins); denial != nil {
		return denial
	}
	if !g.OwnsOperation(operationID) {
		return Denyf(CodeBindingMismatch, "operation %q was not made under policy %s version %d", operationID, g.PolicyID, g.PolicyVersion)
	}
	if denial := g.covers(controls); denial != nil {
		return denial
	}
	if !now.Before(g.PolicyExpiresAt) || !now.Before(g.IssuedAt.Add(LocalOfflineLifetime+ResultClockMargin)) {
		return Denyf(CodeAuthorityExpired, "the local repair authority ended at %s", g.AuthorityUntil().UTC().Format(time.RFC3339))
	}
	return nil
}
