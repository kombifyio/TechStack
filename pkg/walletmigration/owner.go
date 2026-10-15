package walletmigration

import (
	"regexp"
	"strings"
)

// OwnerOutcome is the result of resolving a row's owner within a tenant.
type OwnerOutcome string

const (
	// OwnerResolved: the row belongs to the planned tenant and every owner
	// alias it carries names the same owner.
	OwnerResolved OwnerOutcome = "owner_resolved"
	// OwnerMissing: no owner alias carries a value.
	OwnerMissing OwnerOutcome = "owner_missing"
	// OwnerConflict: owner aliases disagree, or one is not a string, so no
	// single owner can be named.
	OwnerConflict OwnerOutcome = "owner_conflict"
	// TenantMismatch: the row's tenant is not the planned tenant, or the
	// metadata names another tenant than the row.
	TenantMismatch OwnerOutcome = "tenant_mismatch"
)

// QuarantineReason says why a row is not eligible for migration. Quarantined
// rows stay in wallet_items until their provenance is clear.
type QuarantineReason string

const (
	ReasonTenantMismatch QuarantineReason = QuarantineReason(TenantMismatch)
	ReasonOwnerConflict  QuarantineReason = QuarantineReason(OwnerConflict)
	ReasonOwnerMissing   QuarantineReason = QuarantineReason(OwnerMissing)
	// ReasonKindConflict: item_type and the metadata kind or item_type alias
	// disagree, which the store itself rejects on write.
	ReasonKindConflict QuarantineReason = "kind_conflict"
	// ReasonUnsupportedKind: the kind has no Wallet mapping.
	ReasonUnsupportedKind QuarantineReason = "unsupported_kind"
	// ReasonValueMalformed: a secret or TOTP field is not a string, claims an
	// unknown envelope, or is a truncated enc:v1 envelope.
	ReasonValueMalformed QuarantineReason = "value_malformed"
	// ReasonOwnerUnlinked: the owner is not an Auth0 subject, which is what the
	// Wallet keeps items for (a SaaS owner id is the Auth0 sub). A local
	// account id or an e-mail address would be guessed into a Wallet.
	ReasonOwnerUnlinked QuarantineReason = "owner_unlinked"
	// ReasonValueUnmappable: the row carries a TOTP secret but maps to an item
	// type that cannot hold it, so migrating it would drop the TOTP.
	ReasonValueUnmappable QuarantineReason = "value_unmappable"
)

// quarantinePrecedence orders reasons when a row has several; the report
// groups rows by the first one. Ownership and tenancy come first because a
// value fix cannot make a row with an unclear owner eligible.
var quarantinePrecedence = []QuarantineReason{
	ReasonTenantMismatch,
	ReasonOwnerConflict,
	ReasonOwnerMissing,
	ReasonOwnerUnlinked,
	ReasonKindConflict,
	ReasonUnsupportedKind,
	ReasonValueMalformed,
	ReasonValueUnmappable,
}

// ownerAliasKeys are the legacy owner fields the store accepts only when they
// agree (pkg/controlplane WalletOwnerID).
var ownerAliasKeys = []string{"owner_id", "owner_subject_id", "user_id"}

// OwnerResolution is the owner outcome for one row.
type OwnerResolution struct {
	Outcome OwnerOutcome `json:"outcome"`
	// OwnerID is set only for OwnerResolved.
	OwnerID string `json:"-"`
	// Aliases lists the alias keys that carried a value, in key order.
	Aliases []string `json:"aliases,omitempty"`
}

// ResolveOwner resolves the owner of row inside tenantScope. It accepts the
// same identities as the store's write predicate and nothing more: a missing,
// conflicting or non-string alias never confers ownership.
func ResolveOwner(row SourceRow, tenantScope string) OwnerResolution {
	scope := strings.TrimSpace(tenantScope)
	rowTenant := strings.TrimSpace(row.TenantID)
	if scope == "" || rowTenant != scope || !metadataTenantAgrees(row.Metadata, rowTenant) {
		return OwnerResolution{Outcome: TenantMismatch}
	}
	owner := ""
	conflict := false
	var aliases []string
	for _, key := range ownerAliasKeys {
		value, present := row.Metadata[key]
		if !present || value == nil {
			continue
		}
		alias, ok := value.(string)
		if !ok {
			aliases = append(aliases, key)
			conflict = true
			continue
		}
		alias = strings.TrimSpace(alias)
		if alias == "" {
			continue
		}
		aliases = append(aliases, key)
		if owner != "" && alias != owner {
			conflict = true
		}
		if owner == "" {
			owner = alias
		}
	}
	switch {
	case conflict:
		return OwnerResolution{Outcome: OwnerConflict, Aliases: aliases}
	case owner == "":
		return OwnerResolution{Outcome: OwnerMissing}
	default:
		return OwnerResolution{Outcome: OwnerResolved, OwnerID: owner, Aliases: aliases}
	}
}

// metadataTenantAgrees rejects a row whose metadata names a different tenant
// than the row itself. The API copies caller payload into metadata, so such a
// row cannot be attributed safely.
func metadataTenantAgrees(metadata map[string]any, rowTenant string) bool {
	value, present := metadata["tenant_id"]
	if !present || value == nil {
		return true
	}
	text, ok := value.(string)
	if !ok {
		return false
	}
	text = strings.TrimSpace(text)
	return text == "" || text == rowTenant
}

func ownerQuarantineReason(resolution OwnerResolution) QuarantineReason {
	switch {
	case resolution.Outcome != OwnerResolved:
		return QuarantineReason(resolution.Outcome)
	case !OwnerSubjectEligible(resolution.OwnerID):
		return ReasonOwnerUnlinked
	default:
		return ""
	}
}

// walletOwnerPattern mirrors the Wallet's owner partition grammar
// (walletservice eligibleOwner); connectionPrefix is the Auth0 connection in
// front of the first "|" of a subject.
var (
	walletOwnerPattern = regexp.MustCompile(`^[A-Za-z0-9._:|@/-]{1,256}$`)
	connectionPrefix   = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}\|.+$`)
)

// OwnerSubjectEligible reports whether owner looks like the Auth0 subject of a
// person: a connection prefix, a "|" and an id, in the Wallet's partition
// grammar, and neither a service, an anonymous caller nor an M2M client. It is
// a syntactic check; the identity link itself is not read.
func OwnerSubjectEligible(owner string) bool {
	return walletOwnerPattern.MatchString(owner) && connectionPrefix.MatchString(owner) &&
		!strings.HasPrefix(owner, "service:") && !strings.HasPrefix(owner, "anonymous:") && !strings.HasSuffix(owner, "@clients")
}

func firstReason(reasons ...QuarantineReason) QuarantineReason {
	for _, candidate := range quarantinePrecedence {
		for _, reason := range reasons {
			if reason == candidate {
				return reason
			}
		}
	}
	return ""
}
