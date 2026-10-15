package walletmigration

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// SourceRow is one wallet_items row as the store returns it.
type SourceRow struct {
	ID          string
	TenantID    string
	InstanceID  string
	StackID     string
	ItemType    string
	Provider    string
	ExternalRef string
	Metadata    map[string]any
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ResourceType is a Core Wallet item type (nested v3 walletItemTypeProfilesV3).
type ResourceType string

const (
	ResourceLogin             ResourceType = "login"
	ResourceGenericSecret     ResourceType = "generic_secret"
	ResourceConfiguration     ResourceType = "configuration"
	ResourceOAuthConnection   ResourceType = "oauth_connection"
	ResourceNodeAccessProfile ResourceType = "node_access_profile"
	ResourceAICredential      ResourceType = "ai_credential" // #nosec G101 -- item type name, not a credential value.
)

// CustodianWalletStore is the custodian of every migrated item. The
// migration-only "product" custodian is what this slice retires.
const CustodianWalletStore = "wallet_store"

// CredentialForm says what the secret field holds, so the writer can label
// the Wallet version and choose the right use action.
type CredentialForm string

const (
	FormNone             CredentialForm = "none"
	FormPassword         CredentialForm = "password"
	FormAPIKey           CredentialForm = "api_key"
	FormToken            CredentialForm = "token"
	FormOAuthToken       CredentialForm = "oauth_token" // #nosec G101 -- value form name, not a credential value.
	FormCertificate      CredentialForm = "certificate"
	FormPrivateKey       CredentialForm = "private_key"
	FormPublicKeyOnly    CredentialForm = "public_key_only"
	FormSecret           CredentialForm = "secret"
	FormRecoveryMaterial CredentialForm = "recovery_material"
	FormSecretReference  CredentialForm = "secret_reference"
)

// Flag marks an eligible row with a fact the executor or the owner must see.
type Flag string

const (
	// FlagLegacyPlaintext: a secret or TOTP is stored without encryption. It
	// migrates, and the source must be destroyed after readback.
	FlagLegacyPlaintext Flag = "legacy_plaintext"
	// FlagNoSecretValue: neither secret nor TOTP is present, so the row maps to
	// a configuration item.
	FlagNoSecretValue Flag = "no_secret_value"
	// FlagStackOrphaned: the metadata names a stack the row no longer links
	// (the stack was deleted), so the item falls back to account scope.
	FlagStackOrphaned Flag = "stack_orphaned"
	// FlagStackReferenceConflict: row stack_id and metadata stack disagree; the
	// row's foreign key wins.
	FlagStackReferenceConflict Flag = "stack_reference_conflict"
	// FlagDerivedProjection: a Techstack writer (not the owner) wrote the row
	// and rewrites it on its next run unless it switches to the Wallet first.
	FlagDerivedProjection Flag = "derived_projection"
	// FlagLabelResolved: a caller resolves this kind by case-insensitive name
	// label and must switch to the Wallet item id.
	FlagLabelResolved Flag = "label_resolved"
)

// Writer names the code path that wrote a row.
type Writer string

const (
	WriterOwner           Writer = "owner"
	WriterStackKitOutputs Writer = "stackkit_outputs"
	WriterOwnerBootstrap  Writer = "owner_bootstrap"
	WriterSystemAccount   Writer = "system_account"
	WriterRemoteSSHWizard Writer = "remote_ssh_wizard"
)

// ScopeKind is the Wallet item scope.
type ScopeKind string

const (
	ScopeAccount ScopeKind = "account"
	ScopeProject ScopeKind = "project"
)

// Scope is an item's Wallet scope: the owner's account or one project, where
// a Techstack stack (kit deployment) is the project.
type Scope struct {
	Kind       ScopeKind
	ProjectRef string
}

// String renders "account" or "project:<stack>".
func (s Scope) String() string {
	if s.Kind == ScopeProject && s.ProjectRef != "" {
		return string(ScopeProject) + ":" + s.ProjectRef
	}
	return string(ScopeAccount)
}

// MarshalText renders the same form for JSON.
func (s Scope) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// SourceRef keeps the Techstack identity of a migrated item so callers can
// switch from name labels, composite ids and service references to the
// Wallet item id.
type SourceRef struct {
	TenantID        string
	ItemID          string
	Kind            string
	StackID         string
	MetadataStackID string
	InstanceID      string
	ServiceID       string
	SourceType      string
	ExternalRef     string
	Writer          Writer
	// NameLabel is the trimmed, lower-cased name the label callers match.
	NameLabel string
	CreatedAt time.Time
	// RecordedWalletItemID is the Wallet item an earlier execute recorded on
	// the row (metadata kombify_wallet_item_id); empty before the migration.
	RecordedWalletItemID string
}

// ItemMetadata is the Wallet item metadata. The Wallet seals metadata under
// the owner key; anything that may hold a secret goes into ItemValues.
type ItemMetadata struct {
	DisplayName string
	Description string
	URL         string
	ItemClass   string
	AccessMode  string
	// Provider is set for oauth_connection and ai_credential items.
	Provider string
}

// ItemValues are the fields captured as the item's first version.
type ItemValues struct {
	Form      CredentialForm
	Secret    SourceValue
	TOTP      SourceValue
	Username  SourceValue
	PublicKey SourceValue
	Notes     SourceValue
}

// TargetItem is the Wallet item one row becomes.
type TargetItem struct {
	ResourceType ResourceType
	Custodian    string
	Scope        Scope
	Metadata     ItemMetadata
	Values       ItemValues
	// MigrationKey is the stable idempotency key for the capture, so a
	// repeated execute converges on one Wallet item.
	MigrationKey string
}

// ItemPlan is the migration plan for one row.
type ItemPlan struct {
	Source SourceRef
	Owner  OwnerResolution
	// Quarantine is empty for an eligible row.
	Quarantine QuarantineReason
	Flags      []Flag
	Target     TargetItem
}

// Eligible reports whether the row may migrate.
func (p ItemPlan) Eligible() bool { return p.Quarantine == "" }

// HasFlag reports whether the plan carries flag.
func (p ItemPlan) HasFlag(flag Flag) bool {
	for _, candidate := range p.Flags {
		if candidate == flag {
			return true
		}
	}
	return false
}

// Techstack kinds: the UI's credential types plus server-written ssh_password.
const (
	kindPassword    = "password"
	kindAPIKey      = "api_key"
	kindSSHKey      = "ssh_key"
	kindSSHPassword = "ssh_password"
	kindOAuthToken  = "oauth_token" // #nosec G101 -- Techstack kind name, not a credential value.
	kindCertificate = "certificate"
	kindOther       = "other"
)

// labelResolvedKinds are the kinds the remote-connection callers resolve by
// name label today.
var labelResolvedKinds = map[string]bool{kindSSHKey: true, kindSSHPassword: true}

// oauthProviders are providers whose token is an OAuth connection; the item
// keeps the token as a wallet_store value until the owner reconnects it
// through Token Vault.
var oauthProviders = map[string]bool{
	"bitbucket": true, "discord": true, "github": true, "gitlab": true,
	"google": true, "linear": true, "microsoft": true, "notion": true, "slack": true,
}

// aiProviders are providers whose API key is a BYOK ai_credential.
var aiProviders = map[string]bool{
	"anthropic": true, "cohere": true, "deepseek": true, "gemini": true,
	"google_ai": true, "groq": true, "mistral": true, "openai": true,
	"openrouter": true, "xai": true,
}

// Service ids the Techstack writers use (internal/walletsync,
// internal/routes/stacks owner bootstrap, internal/systemwallet).
const (
	serviceOwnerAccess = "pocketid:owner"
	serviceRecovery    = "stackkit:recovery"
	serviceRemoteSSH   = "remote-ssh"
)

// PlanItem plans one row inside tenantScope: it resolves the owner, maps the
// row to a Wallet item and decides eligibility. It is pure and reads no value.
func PlanItem(row SourceRow, tenantScope string) ItemPlan {
	owner := ResolveOwner(row, tenantScope)
	target, flags, mappingReason := MapItem(row)
	return ItemPlan{
		Source:     sourceRefOf(row),
		Owner:      owner,
		Quarantine: firstReason(ownerQuarantineReason(owner), mappingReason),
		Flags:      flags,
		Target:     target,
	}
}

// MapItem maps a row to its Wallet item, independent of ownership. A non-empty
// reason means the row has no sound mapping; the returned target is then
// partial and must not be captured.
func MapItem(row SourceRow) (TargetItem, []Flag, QuarantineReason) {
	kind, kindOK := normalizedKind(row)
	values, valueErr := sourceValuesOf(row, kind)
	scope, flags := scopeOf(row)
	flags = append(flags, rowFlags(row, kind, values)...)
	key := migrationKey(row.TenantID, row.ID)
	target := TargetItem{Custodian: CustodianWalletStore, Scope: scope, Metadata: metadataOf(row), MigrationKey: key}

	resourceType, form, provider, supported := classify(kind, providerOf(row), target.Metadata.ItemClass, values)
	// A row without a secret or TOTP keeps its non-secret data as a
	// configuration item; an SSH public key alone is a node access profile.
	if supported && !values.Secret.Present() && !values.TOTP.Present() && form != FormPublicKeyOnly {
		resourceType = ResourceConfiguration
		if form != FormSecretReference {
			form = FormNone
		}
		flags = append(flags, FlagNoSecretValue)
	}
	values.Form = form
	target.ResourceType = resourceType
	target.Metadata.Provider = provider
	target.Metadata.DisplayName = displayName(resourceType, form, key)
	target.Metadata.Description = description(kind, key)
	target.Values = values

	var reason QuarantineReason
	switch {
	case !kindOK:
		reason = ReasonKindConflict
	case valueErr != nil:
		reason = ReasonValueMalformed
	case !supported:
		reason = ReasonUnsupportedKind
	case values.TOTP.Present() && resourceType != ResourceLogin:
		reason = ReasonValueUnmappable
	}
	return target, sortedFlags(flags), reason
}

// sourceValuesOf collects the value fields without reading them.
func sourceValuesOf(row SourceRow, kind string) (ItemValues, error) {
	secret, secretErr := inspectStoredValue(row.Metadata["secret"])
	totp, totpErr := inspectStoredValue(row.Metadata["totp"])
	values := ItemValues{
		Form:     FormNone,
		Secret:   secret,
		TOTP:     totp,
		Username: plainSourceValue(metadataString(row.Metadata, "username")),
		Notes:    plainSourceValue(metadataString(row.Metadata, "notes")),
	}
	if kind == kindSSHKey {
		values.PublicKey = sshPublicKeyOf(row.Metadata)
	}
	if secretErr != nil {
		return values, secretErr
	}
	return values, totpErr
}

func rowFlags(row SourceRow, kind string, values ItemValues) []Flag {
	var flags []Flag
	if values.Secret.Encoding() == ValuePlaintext || values.TOTP.Encoding() == ValuePlaintext {
		flags = append(flags, FlagLegacyPlaintext)
	}
	if writerOf(row) != WriterOwner {
		flags = append(flags, FlagDerivedProjection)
	}
	if labelResolvedKinds[kind] {
		flags = append(flags, FlagLabelResolved)
	}
	return flags
}

// metadataOf carries the non-secret descriptive fields of a row. The Wallet
// display name and description are generic (see displayName) and set by
// MapItem once the Wallet type is known.
func metadataOf(row SourceRow) ItemMetadata {
	return ItemMetadata{
		URL:        metadataString(row.Metadata, "url"),
		ItemClass:  metadataString(row.Metadata, "item_class"),
		AccessMode: metadataString(row.Metadata, "access_mode"),
	}
}

// classify picks the Wallet type and value form for a kind. supported is
// false for a kind without a mapping.
func classify(kind, provider, itemClass string, values ItemValues) (ResourceType, CredentialForm, string, bool) {
	switch kind {
	case kindSSHKey:
		return ResourceNodeAccessProfile, sshKeyForm(values), "", true
	case kindSSHPassword:
		return ResourceNodeAccessProfile, FormPassword, "", true
	case kindPassword:
		// A TOTP secret only fits a login, so it decides like a username does.
		if values.Username.Present() || values.TOTP.Present() {
			return ResourceLogin, FormPassword, "", true
		}
		return ResourceGenericSecret, FormPassword, "", true
	case kindAPIKey:
		if aiProviders[provider] {
			return ResourceAICredential, FormAPIKey, provider, true
		}
		return ResourceGenericSecret, FormAPIKey, "", true
	case kindOAuthToken:
		if oauthProviders[provider] {
			return ResourceOAuthConnection, FormOAuthToken, provider, true
		}
		return ResourceGenericSecret, FormToken, "", true
	case kindCertificate:
		return ResourceGenericSecret, FormCertificate, "", true
	case kindOther:
		resourceType, form := classifyOther(itemClass)
		return resourceType, form, "", true
	default:
		return "", FormNone, "", false
	}
}

func sshKeyForm(values ItemValues) CredentialForm {
	switch {
	case values.Secret.Present():
		return FormPrivateKey
	case values.PublicKey.Present():
		return FormPublicKeyOnly
	default:
		return FormNone
	}
}

func classifyOther(itemClass string) (ResourceType, CredentialForm) {
	switch itemClass {
	case "machine_secret_ref":
		// The value is a reference; StackKits keeps the secret itself.
		return ResourceConfiguration, FormSecretReference
	case "recovery":
		return ResourceGenericSecret, FormRecoveryMaterial
	default:
		return ResourceGenericSecret, FormSecret
	}
}

// normalizedKind mirrors the store's identity normalization: item_type wins,
// metadata kind and item_type may only repeat it.
func normalizedKind(row SourceRow) (string, bool) {
	aliases := make([]string, 0, 2)
	for _, key := range []string{"kind", "item_type"} {
		value, present := row.Metadata[key]
		if !present || value == nil {
			continue
		}
		text, ok := value.(string)
		if !ok {
			return strings.TrimSpace(row.ItemType), false
		}
		aliases = append(aliases, strings.TrimSpace(text))
	}
	kind := strings.TrimSpace(row.ItemType)
	for _, alias := range aliases {
		if kind == "" {
			kind = alias
		}
	}
	if kind == "" {
		kind = kindOther
	}
	for _, alias := range aliases {
		if alias != "" && alias != kind {
			return kind, false
		}
	}
	return kind, true
}

func scopeOf(row SourceRow) (Scope, []Flag) {
	stack := strings.TrimSpace(row.StackID)
	metadataStack := metadataStackID(row.Metadata)
	switch {
	case stack != "" && metadataStack != "" && metadataStack != stack:
		return Scope{Kind: ScopeProject, ProjectRef: stack}, []Flag{FlagStackReferenceConflict}
	case stack != "":
		return Scope{Kind: ScopeProject, ProjectRef: stack}, nil
	case metadataStack != "":
		return Scope{Kind: ScopeAccount}, []Flag{FlagStackOrphaned}
	default:
		return Scope{Kind: ScopeAccount}, nil
	}
}

func metadataStackID(metadata map[string]any) string {
	return firstNonEmpty(metadataString(metadata, "stack_id"), metadataString(metadata, "kit_deployment_id"))
}

func sourceRefOf(row SourceRow) SourceRef {
	kind, _ := normalizedKind(row)
	return SourceRef{
		TenantID:        strings.TrimSpace(row.TenantID),
		ItemID:          strings.TrimSpace(row.ID),
		Kind:            kind,
		StackID:         strings.TrimSpace(row.StackID),
		MetadataStackID: metadataStackID(row.Metadata),
		InstanceID:      strings.TrimSpace(row.InstanceID),
		ServiceID:       metadataString(row.Metadata, "service_id"),
		SourceType:      strings.TrimSpace(row.Provider),
		ExternalRef:     strings.TrimSpace(row.ExternalRef),
		Writer:          writerOf(row),
		NameLabel:       foldLabel(metadataString(row.Metadata, "name")),
		CreatedAt:       row.CreatedAt,

		RecordedWalletItemID: metadataString(row.Metadata, RecordedItemKey),
	}
}

// writerOf attributes a row to the code path that wrote it, from the ids and
// markers each writer sets.
func writerOf(row SourceRow) Writer {
	marks := writerMarksOf(row)
	switch {
	case marks.remoteSSHWizard():
		return WriterRemoteSSHWizard
	case marks.systemAccount():
		return WriterSystemAccount
	case marks.ownerBootstrap():
		return WriterOwnerBootstrap
	case marks.stackKitOutputs():
		return WriterStackKitOutputs
	default:
		return WriterOwner
	}
}

type writerMarks struct {
	id, kind, serviceID, sourceType, stack string
	generated                              bool
}

func writerMarksOf(row SourceRow) writerMarks {
	kind, _ := normalizedKind(row)
	return writerMarks{
		id:         strings.TrimSpace(row.ID),
		kind:       kind,
		serviceID:  metadataString(row.Metadata, "service_id"),
		sourceType: firstNonEmpty(row.Provider, metadataString(row.Metadata, "source_type")),
		stack:      firstNonEmpty(row.StackID, metadataStackID(row.Metadata)),
		generated:  metadataBool(row.Metadata, "auto_generated"),
	}
}

// remoteSSHWizard: upsertRemoteSSHPasswordWalletItem writes <stack>:remote-ssh.
func (m writerMarks) remoteSSHWizard() bool {
	return m.kind == kindSSHPassword && (m.sourceType == "remote_ssh" || (m.stack != "" && m.id == m.stack+":"+serviceRemoteSSH))
}

// systemAccount: systemwallet.UpsertCredential writes system:<role> rows.
func (m writerMarks) systemAccount() bool {
	return m.generated && m.sourceType == "system_account" && strings.HasPrefix(m.serviceID, "system:")
}

// ownerBootstrap: the owner bootstrap writes <stack>:<service id>.
func (m writerMarks) ownerBootstrap() bool {
	return m.generated && m.stack != "" && (m.serviceID == serviceOwnerAccess || m.serviceID == serviceRecovery) &&
		m.id == m.stack+":"+m.serviceID
}

// stackKitOutputs: walletsync writes hashed wallet_<digest> ids.
func (m writerMarks) stackKitOutputs() bool {
	return m.generated && strings.HasPrefix(m.id, "wallet_") && (m.sourceType == "stackkit" || m.sourceType == "pocketid")
}

func providerOf(row SourceRow) string {
	provider := firstNonEmpty(strings.TrimSpace(row.Provider), metadataString(row.Metadata, "provider"))
	return strings.ReplaceAll(strings.ToLower(provider), "-", "_")
}

// sshPublicKeyOf mirrors the server-terminal reader: the public_key field,
// then any authorized-key line in the notes.
func sshPublicKeyOf(metadata map[string]any) SourceValue {
	candidates := []string{metadataString(metadata, "public_key")}
	candidates = append(candidates, strings.Split(metadataString(metadata, "notes"), "\n")...)
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || len(candidate) > 16*1024 {
			continue
		}
		if _, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(candidate)); err == nil && strings.TrimSpace(string(rest)) == "" {
			return plainSourceValue(candidate)
		}
	}
	return SourceValue{encoding: ValueAbsent}
}

// displayName is the Wallet display name of a migrated item. It is generic by
// design: the owner's own label for the credential is personal data the
// Wallet lists to every service holding a right, so it stays in the legacy
// row. The short suffix of the migration key tells two items of one kind
// apart; the owner can rename them in the Wallet.
func displayName(resourceType ResourceType, form CredentialForm, key string) string {
	label := "secret"
	switch resourceType {
	case ResourceLogin:
		label = "login"
	case ResourceConfiguration:
		label = "configuration"
	case ResourceAICredential:
		label = "AI credential"
	case ResourceOAuthConnection:
		label = "OAuth token"
	case ResourceNodeAccessProfile:
		label = "SSH access"
	case ResourceGenericSecret:
		switch form {
		case FormAPIKey:
			label = "API key"
		case FormCertificate:
			label = "certificate"
		case FormRecoveryMaterial:
			label = "recovery secret"
		}
	}
	return "Techstack " + label + " " + keySuffix(key)
}

// keySuffix is the first six hex digits of a migration key.
func keySuffix(key string) string {
	hexPart := strings.TrimPrefix(key, migrationKeyPrefix)
	if len(hexPart) > 6 {
		hexPart = hexPart[:6]
	}
	return hexPart
}

// MigrationToken marks a migrated item's description so a later run finds the
// item again without any personal data: it carries the key only, which is a
// hash of tenant and row id.
const MigrationToken = "techstack-migration:" // #nosec G101 -- description marker, not a credential.

// description is the Wallet description of a migrated item: the Techstack
// kind (a fixed vocabulary) and the migration token. It carries no owner,
// tenant, name or label.
func description(kind, key string) string {
	return fmt.Sprintf("Migrated from the Techstack wallet (%s). %s%s", kind, MigrationToken, key)
}

const migrationKeyPrefix = "tswm1_"

// migrationKey is the idempotency key for one row's capture.
func migrationKey(tenantID, itemID string) string {
	sum := sha256.Sum256([]byte("techstack.wallet-migration.v1\x00" + strings.TrimSpace(tenantID) + "\x00" + strings.TrimSpace(itemID)))
	return migrationKeyPrefix + hex.EncodeToString(sum[:16])
}

func foldLabel(label string) string { return strings.ToLower(strings.TrimSpace(label)) }

func metadataString(metadata map[string]any, key string) string {
	text, _ := metadata[key].(string)
	return strings.TrimSpace(text)
}

func metadataBool(metadata map[string]any, key string) bool {
	switch typed := metadata[key].(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func sortedFlags(flags []Flag) []Flag {
	if len(flags) == 0 {
		return nil
	}
	seen := make(map[Flag]bool, len(flags))
	out := make([]Flag, 0, len(flags))
	for _, flag := range flags {
		if !seen[flag] {
			seen[flag] = true
			out = append(out, flag)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
