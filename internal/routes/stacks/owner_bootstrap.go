package stacks

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	tsauth "github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/pocketbase/pocketbase/core"
)

const (
	ownerSourceLocal       = "local"
	ownerSourceCloud       = "cloud"
	ownerSourceCloudLinked = "cloud-linked"
	ownerConfigKey         = "owner"

	ownerBootstrapModeAuto   = "auto"
	ownerBootstrapModeCustom = "custom"
	ownerBootstrapModeNone   = "none"

	ownerWalletServiceID    = "pocketid:owner"
	recoveryWalletServiceID = "stackkit:recovery"
)

var ownerUsernameUnsafeChars = regexp.MustCompile(`[^a-z0-9_-]+`)

// ownerSourceSeedsPocketID reports whether the owner source materializes a
// Pocket ID owner seed on the stack (owner fields present at rollout). Plain
// local owners and owners derived from a verified kombify Cloud link both
// travel the local seeding path; only the SaaS auto "cloud" lane defers owner
// materialization to the platform.
func ownerSourceSeedsPocketID(source string) bool {
	return source == ownerSourceLocal || source == ownerSourceCloudLinked
}

type ownerBootstrapSpec struct {
	BootstrapMode          string
	Source                 string
	Email                  string
	Username               string
	DisplayName            string
	RecoveryPassphraseHash string
}

func validateCreateOwnerBootstrap(req normalizedCreateStackRequest) string {
	bootstrap, present := ownerBootstrapFromRequest(req)
	if !present {
		return ""
	}

	bootstrap.applyDefaults()

	switch bootstrap.BootstrapMode {
	case ownerBootstrapModeAuto:
		if msg := validateOwnerBootstrapSource(bootstrap.Source); msg != "" {
			return msg
		}
		if bootstrap.RecoveryPassphraseHash != "" && !strings.HasPrefix(bootstrap.RecoveryPassphraseHash, "$argon2id$") {
			return "Recovery passphrase hash must use argon2id"
		}
		return ""
	case ownerBootstrapModeCustom:
	default:
		return "Owner bootstrap mode must be 'auto', 'custom', or 'none'"
	}

	switch bootstrap.Source {
	case ownerSourceLocal:
		if bootstrap.Email == "" {
			return "Owner email is required for custom owner bootstrap"
		}
		if bootstrap.RecoveryPassphraseHash != "" && !strings.HasPrefix(bootstrap.RecoveryPassphraseHash, "$argon2id$") {
			return "Recovery passphrase hash must use argon2id"
		}
	case ownerSourceCloudLinked:
		// Identity fields must come from the server-side verified cloud link,
		// never from the browser: a client-supplied email would let the request
		// impersonate an arbitrary owner identity.
		if bootstrap.Email != "" || bootstrap.Username != "" || bootstrap.DisplayName != "" {
			return "Owner identity fields are not accepted for a cloud-linked owner; they derive from the verified kombify Cloud link"
		}
		if bootstrap.RecoveryPassphraseHash != "" && !strings.HasPrefix(bootstrap.RecoveryPassphraseHash, "$argon2id$") {
			return "Recovery passphrase hash must use argon2id"
		}
	case ownerSourceCloud:
		if !cloudOwnerBootstrapEnabled() {
			return "Cloud owner bootstrap is not available in this release. Use local owner bootstrap."
		}
	default:
		return "Owner source must be 'local', 'cloud', or 'cloud-linked'"
	}

	return ""
}

func ownerBootstrapFromRequest(req normalizedCreateStackRequest) (ownerBootstrapSpec, bool) {
	var out ownerBootstrapSpec

	applyOwnerBootstrapFromConfig(&out, req.UserConfig)
	applyOwnerBootstrapFromOptions(&out, req.Options)
	out.normalize()

	return out, out.present()
}

func applyOwnerBootstrapFromConfig(out *ownerBootstrapSpec, config map[string]interface{}) {
	if out == nil || config == nil {
		return
	}

	if metadata, ok := mapFromAny(config["metadata"]); ok {
		out.BootstrapMode = firstNonEmpty(out.BootstrapMode, stringFromAny(metadata["owner_bootstrap_mode"]))
		out.Source = firstNonEmpty(out.Source, stringFromAny(metadata["owner_source"]))
	}

	if owner, ok := mapFromAny(config[ownerConfigKey]); ok {
		applyOwnerMap(out, owner)
	}

	if identity, ok := mapFromAny(config["identity"]); ok {
		if owner, ok := mapFromAny(identity[ownerConfigKey]); ok {
			applyOwnerMap(out, owner)
		}
		if recovery, ok := mapFromAny(identity["recovery"]); ok {
			out.RecoveryPassphraseHash = firstNonEmpty(
				out.RecoveryPassphraseHash,
				stringFromAny(recovery["passphrase_hash"]),
				stringFromAny(recovery["passphraseHash"]),
				stringFromAny(recovery["recovery_passphrase_hash"]),
			)
		}
	}

	if recovery, ok := mapFromAny(config["recovery"]); ok {
		out.RecoveryPassphraseHash = firstNonEmpty(
			out.RecoveryPassphraseHash,
			stringFromAny(recovery["passphrase_hash"]),
			stringFromAny(recovery["passphraseHash"]),
			stringFromAny(recovery["recovery_passphrase_hash"]),
		)
	}
}

func applyOwnerBootstrapFromOptions(out *ownerBootstrapSpec, options map[string]interface{}) {
	if out == nil || options == nil {
		return
	}

	out.BootstrapMode = firstNonEmpty(stringFromAny(options["owner_bootstrap_mode"]), out.BootstrapMode)
	out.Source = firstNonEmpty(stringFromAny(options["owner_source"]), out.Source)
	out.Email = firstNonEmpty(stringFromAny(options["owner_email"]), out.Email)
	out.Username = firstNonEmpty(stringFromAny(options["owner_username"]), out.Username)
	out.DisplayName = firstNonEmpty(stringFromAny(options["owner_display_name"]), out.DisplayName)
	out.RecoveryPassphraseHash = firstNonEmpty(
		stringFromAny(options["recovery_passphrase_hash"]),
		out.RecoveryPassphraseHash,
	)
}

func applyOwnerMap(out *ownerBootstrapSpec, owner map[string]interface{}) {
	out.BootstrapMode = firstNonEmpty(
		stringFromAny(owner["bootstrapMode"]),
		stringFromAny(owner["bootstrap_mode"]),
		out.BootstrapMode,
	)
	out.Source = firstNonEmpty(stringFromAny(owner["source"]), out.Source)
	out.Email = firstNonEmpty(stringFromAny(owner["email"]), out.Email)
	out.Username = firstNonEmpty(stringFromAny(owner["username"]), out.Username)
	out.DisplayName = firstNonEmpty(
		stringFromAny(owner["displayName"]),
		stringFromAny(owner["display_name"]),
		stringFromAny(owner["name"]),
		out.DisplayName,
	)
}

func (b *ownerBootstrapSpec) normalize() {
	b.BootstrapMode = strings.ToLower(strings.TrimSpace(b.BootstrapMode))
	b.Source = strings.ToLower(strings.TrimSpace(b.Source))
	b.Email = strings.TrimSpace(b.Email)
	b.Username = strings.TrimSpace(b.Username)
	b.DisplayName = strings.TrimSpace(b.DisplayName)
	b.RecoveryPassphraseHash = strings.TrimSpace(b.RecoveryPassphraseHash)
}

func (b *ownerBootstrapSpec) applyDefaults() {
	if b == nil {
		return
	}
	if b.Source == "" {
		b.Source = ownerSourceLocal
	}
	if b.BootstrapMode == "" {
		b.BootstrapMode = ownerBootstrapModeCustom
	}
}

func validateOwnerBootstrapSource(source string) string {
	switch source {
	case ownerSourceLocal, ownerSourceCloud:
		return ""
	default:
		return "Owner source must be 'local' or 'cloud'"
	}
}

func (b ownerBootstrapSpec) present() bool {
	if b.BootstrapMode == ownerBootstrapModeNone {
		return false
	}
	if b.BootstrapMode != "" {
		return true
	}
	return b.Source != "" ||
		b.Email != "" ||
		b.Username != "" ||
		b.DisplayName != "" ||
		b.RecoveryPassphraseHash != ""
}

type ownerBootstrapContext struct {
	Email       string
	Username    string
	DisplayName string
	// CloudLink carries the server-side verified kombify Cloud profile link for
	// the authenticated operator. It is only populated when the request selects
	// the cloud-linked owner source; nil means no usable link exists.
	CloudLink *cloudLinkIdentity
}

func ownerBootstrapContextFromRequest(e *httpx.Event) ownerBootstrapContext {
	var ctx ownerBootstrapContext
	if e == nil {
		return ctx
	}
	if e.Auth != nil {
		ctx.Email = strings.TrimSpace(e.Auth.GetString("email"))
		ctx.Username = strings.TrimSpace(e.Auth.GetString("username"))
		ctx.DisplayName = strings.TrimSpace(firstNonEmpty(
			e.Auth.GetString("name"),
			e.Auth.GetString("displayName"),
		))
	}
	if e.Request != nil {
		if id := identity.FromContext(e.Request.Context()); id != nil {
			ctx.Email = firstNonEmpty(ctx.Email, id.Email)
			ctx.Username = firstNonEmpty(ctx.Username, id.UserID)
		}
	}
	return ctx
}

func resolveCreateOwnerBootstrap(req normalizedCreateStackRequest, ctx ownerBootstrapContext) (normalizedCreateStackRequest, *ownerBootstrapDenial) {
	bootstrap, present := ownerBootstrapFromRequest(req)
	if !present {
		return req, nil
	}
	bootstrap.applyDefaults()
	switch bootstrap.BootstrapMode {
	case ownerBootstrapModeAuto:
		if msg := validateOwnerBootstrapSource(bootstrap.Source); msg != "" {
			return req, denyOwnerSourceInvalid(msg)
		}
		if bootstrap.Source == ownerSourceCloud {
			bootstrap.Email = ""
			bootstrap.Username = ""
			bootstrap.DisplayName = ""
			bootstrap.RecoveryPassphraseHash = ""
			applyResolvedOwnerBootstrap(&req, bootstrap)
			return req, nil
		}
		email := strings.TrimSpace(ctx.Email)
		if email == "" {
			return req, denyCloudProfileEmailMissing()
		}
		bootstrap.Source = ownerSourceLocal
		bootstrap.Email = email
		bootstrap.Username = usernameFromEmail(email)
		bootstrap.DisplayName = firstNonEmpty(strings.TrimSpace(ctx.DisplayName), email)
	case ownerBootstrapModeCustom:
		if resolved, denial := resolveCustomOwnerIdentity(&bootstrap, ctx); !resolved {
			return req, denial
		}
	default:
		return req, denyOwnerBootstrapModeInvalid()
	}

	if ownerSourceSeedsPocketID(bootstrap.Source) && bootstrap.RecoveryPassphraseHash == "" {
		recoveryHash, err := generateRecoveryPassphraseHash()
		if err != nil {
			return req, denyRecoveryMaterialUnavailable()
		}
		bootstrap.RecoveryPassphraseHash = recoveryHash
	}
	applyResolvedOwnerBootstrap(&req, bootstrap)
	return req, nil
}

// resolveCustomOwnerIdentity fills the owner identity for custom bootstrap
// mode. Local owners bring their own email; cloud-linked owners derive every
// identity field from the server-side verified kombify Cloud link.
func resolveCustomOwnerIdentity(bootstrap *ownerBootstrapSpec, ctx ownerBootstrapContext) (bool, *ownerBootstrapDenial) {
	if bootstrap.Source == ownerSourceCloudLinked {
		link := ctx.CloudLink
		if link == nil || strings.TrimSpace(link.Email) == "" {
			return false, denyCloudLinkMissing()
		}
		if !link.EmailVerified {
			return false, denyCloudLinkEmailUnverified()
		}
		bootstrap.Email = strings.TrimSpace(link.Email)
		bootstrap.Username = usernameFromEmail(bootstrap.Email)
		bootstrap.DisplayName = firstNonEmpty(strings.TrimSpace(link.DisplayName), bootstrap.Email)
		return true, nil
	}
	if bootstrap.Email == "" {
		return false, denyOwnerEmailMissing()
	}
	bootstrap.Username = firstNonEmpty(bootstrap.Username, usernameFromEmail(bootstrap.Email))
	return true, nil
}

func applyResolvedOwnerBootstrap(req *normalizedCreateStackRequest, bootstrap ownerBootstrapSpec) {
	if req == nil {
		return
	}
	if req.Options == nil {
		req.Options = map[string]interface{}{}
	}
	req.Options["owner_bootstrap_mode"] = bootstrap.BootstrapMode
	req.Options["owner_source"] = bootstrap.Source
	setOptionalOption(req.Options, "owner_email", bootstrap.Email)
	setOptionalOption(req.Options, "owner_username", bootstrap.Username)
	if bootstrap.DisplayName != "" {
		req.Options["owner_display_name"] = bootstrap.DisplayName
	} else {
		delete(req.Options, "owner_display_name")
	}
	if bootstrap.RecoveryPassphraseHash != "" {
		req.Options["recovery_passphrase_hash"] = bootstrap.RecoveryPassphraseHash
	} else {
		delete(req.Options, "recovery_passphrase_hash")
	}
	if req.UserConfig == nil {
		return
	}
	metadata, ok := mapFromAny(req.UserConfig["metadata"])
	if !ok {
		metadata = map[string]interface{}{}
	}
	metadata["owner_bootstrap_mode"] = bootstrap.BootstrapMode
	metadata["owner_source"] = bootstrap.Source
	metadata["owner_email_present"] = bootstrap.Email != ""
	metadata["owner_username_present"] = bootstrap.Username != ""
	metadata["owner_display_name_present"] = bootstrap.DisplayName != ""
	metadata["recovery_passphrase_hash_present"] = bootstrap.RecoveryPassphraseHash != ""
	req.UserConfig["metadata"] = metadata

	owner, ok := mapFromAny(req.UserConfig[ownerConfigKey])
	if !ok {
		owner = map[string]interface{}{}
	}
	owner["bootstrapMode"] = bootstrap.BootstrapMode
	owner["source"] = bootstrap.Source
	setOptionalString(owner, "email", bootstrap.Email)
	setOptionalString(owner, "username", bootstrap.Username)
	if bootstrap.DisplayName != "" {
		owner["displayName"] = bootstrap.DisplayName
	} else {
		delete(owner, "displayName")
	}
	req.UserConfig[ownerConfigKey] = owner
}

func setOptionalOption(options map[string]interface{}, key, value string) {
	if strings.TrimSpace(value) == "" {
		delete(options, key)
		return
	}
	options[key] = value
}

func setOptionalString(values map[string]interface{}, key, value string) {
	if strings.TrimSpace(value) == "" {
		delete(values, key)
		return
	}
	values[key] = value
}

func usernameFromEmail(email string) string {
	email = strings.ToLower(strings.TrimSpace(email))
	local := strings.TrimSpace(strings.Split(email, "@")[0])
	local = ownerUsernameUnsafeChars.ReplaceAllString(local, "-")
	local = strings.Trim(local, "-_")
	if local == "" {
		return ownerConfigKey
	}
	return local
}

func generateRecoveryPassphraseHash() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return tsauth.HashPassword(base64.RawURLEncoding.EncodeToString(buf))
}

// transientOwnerSource maps the persisted owner source onto the StackKit wire
// contract: cloud-linked owners seed Pocket ID exactly like local owners, so
// the wire keeps source="local" and carries the provenance in source_origin.
// This avoids a StackKits owner-bootstrap schema change.
func transientOwnerSource(source string) string {
	if source == ownerSourceCloudLinked {
		return ownerSourceLocal
	}
	return source
}

// ownerSourceOrigin returns the additive provenance marker for owner sources
// that are wire-mapped onto "local"; empty for sources that map to themselves.
func ownerSourceOrigin(source string) string {
	if source == ownerSourceCloudLinked {
		return ownerSourceCloudLinked
	}
	return ""
}

func cloudOwnerBootstrapEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("TECHSTACK_ENABLE_CLOUD_OWNER_BOOTSTRAP"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func applyOwnerBootstrapToTransientSpec(spec map[string]interface{}, bootstrap ownerBootstrapSpec) {
	if spec == nil || !bootstrap.present() {
		return
	}

	identity, ok := mapFromAny(spec["identity"])
	if !ok {
		identity = map[string]interface{}{}
	}
	owner, ok := mapFromAny(identity[ownerConfigKey])
	if !ok {
		owner = map[string]interface{}{}
	}
	recovery, ok := mapFromAny(identity["recovery"])
	if !ok {
		recovery = map[string]interface{}{}
	}

	owner["source"] = transientOwnerSource(bootstrap.Source)
	if origin := ownerSourceOrigin(bootstrap.Source); origin != "" {
		owner["source_origin"] = origin
	}
	owner["email"] = bootstrap.Email
	owner["username"] = bootstrap.Username
	if bootstrap.DisplayName != "" {
		owner["displayName"] = bootstrap.DisplayName
	}
	recovery["passphrase_hash"] = bootstrap.RecoveryPassphraseHash
	recovery["passphraseHashPresent"] = bootstrap.RecoveryPassphraseHash != ""

	identity[ownerConfigKey] = owner
	identity["recovery"] = recovery
	spec["identity"] = identity
}

func cloneMapForMutation(in map[string]interface{}) map[string]interface{} {
	if in == nil {
		return nil
	}
	data, err := json.Marshal(in)
	if err != nil {
		out := make(map[string]interface{}, len(in))
		for key, value := range in {
			out[key] = value
		}
		return out
	}
	var out map[string]interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		out = make(map[string]interface{}, len(in))
		for key, value := range in {
			out[key] = value
		}
	}
	return out
}

func (h crudRouteHandlers) applyOwnerBootstrap(ctx context.Context, tenantID, ownerID, stackID, stackName string, bootstrap ownerBootstrapSpec) error {
	if !bootstrap.present() {
		return nil
	}
	if !ownerSourceSeedsPocketID(bootstrap.Source) {
		return nil
	}
	if h.walletStore != nil {
		if err := h.upsertOwnerAccessWalletStoreEntry(ctx, tenantID, ownerID, stackID, stackName, bootstrap); err != nil {
			return err
		}
		return h.upsertRecoveryWalletStoreEntry(ctx, tenantID, ownerID, stackID, stackName, bootstrap)
	}
	if h.app == nil {
		return nil
	}

	if err := h.upsertOwnerAccessWalletEntry(ownerID, stackID, stackName, bootstrap); err != nil {
		return err
	}
	return h.upsertRecoveryWalletEntry(ownerID, stackID, stackName, bootstrap)
}

func (h crudRouteHandlers) upsertOwnerAccessWalletStoreEntry(ctx context.Context, tenantID, ownerID, stackID, stackName string, bootstrap ownerBootstrapSpec) error {
	fields := ownerAccessWalletFields(ownerID, stackID, stackName, bootstrap)
	return h.upsertWalletStoreEntry(ctx, tenantID, stackID, ownerWalletServiceID, fields)
}

func (h crudRouteHandlers) upsertRecoveryWalletStoreEntry(ctx context.Context, tenantID, ownerID, stackID, stackName string, bootstrap ownerBootstrapSpec) error {
	fields := recoveryWalletFields(ownerID, stackID, stackName, bootstrap)
	return h.upsertWalletStoreEntry(ctx, tenantID, stackID, recoveryWalletServiceID, fields)
}

func (h crudRouteHandlers) upsertWalletStoreEntry(ctx context.Context, tenantID, stackID, serviceID string, fields map[string]any) error {
	if h.walletStore == nil {
		return nil
	}
	item := controlplane.WalletItem{
		ID:          fmt.Sprintf("%s:%s", stackID, serviceID),
		TenantID:    tenantID,
		StackID:     stackID,
		ItemType:    firstNonEmpty(stringFromAny(fields["kind"]), "other"),
		Provider:    stringFromAny(fields["source_type"]),
		ExternalRef: serviceID,
		Metadata:    cloneMapForMutation(fields),
	}
	item.Metadata["id"] = item.ID
	if _, err := h.walletStore.UpsertWalletItem(ctx, item); err != nil {
		return err
	}
	return nil
}

func (h crudRouteHandlers) upsertOwnerAccessWalletEntry(ownerID, stackID, stackName string, bootstrap ownerBootstrapSpec) error {
	fields := ownerAccessWalletFields(ownerID, stackID, stackName, bootstrap)
	_, err := h.upsertWalletEntry(ownerID, stackID, ownerWalletServiceID, fields)
	return err
}

func (h crudRouteHandlers) upsertRecoveryWalletEntry(ownerID, stackID, stackName string, bootstrap ownerBootstrapSpec) error {
	fields := recoveryWalletFields(ownerID, stackID, stackName, bootstrap)
	_, err := h.upsertWalletEntry(ownerID, stackID, recoveryWalletServiceID, fields)
	return err
}

func (h crudRouteHandlers) upsertWalletEntry(ownerID, stackID, serviceID string, fields map[string]any) (*core.Record, error) {
	walletCollection, err := h.app.FindCollectionByNameOrId("wallet")
	if err != nil {
		return nil, fmt.Errorf("wallet collection missing: %w", err)
	}

	rec, _ := h.app.FindFirstRecordByFilter(
		"wallet",
		"owner_id = {:ownerID} && stack_id = {:stackID} && service_id = {:serviceID}",
		map[string]any{
			"ownerID":   ownerID,
			"stackID":   stackID,
			"serviceID": serviceID,
		},
	)
	if rec == nil {
		rec = core.NewRecord(walletCollection)
	}

	for key, value := range fields {
		rec.Set(key, value)
	}

	if err := h.app.Save(rec); err != nil {
		return nil, err
	}
	return rec, nil
}

//nolint:goconst // Wallet field names intentionally mirror PocketBase schema keys.
func ownerAccessWalletFields(ownerID, stackID, stackName string, bootstrap ownerBootstrapSpec) map[string]any {
	display := firstNonEmpty(bootstrap.DisplayName, bootstrap.Username, bootstrap.Email, "Owner")
	notes := fmt.Sprintf("Local owner bootstrap for %s (%s). Manage in Pocket ID after provisioning.", stackName, display)
	if bootstrap.Source == ownerSourceCloudLinked {
		notes = fmt.Sprintf("Owner bootstrap for %s (%s), seeded from the linked kombify Cloud profile. Manage in Pocket ID after provisioning.", stackName, display)
	}
	return map[string]any{
		"owner_id":       ownerID,
		"stack_id":       stackID,
		"service_id":     ownerWalletServiceID,
		"name":           "Pocket ID Owner",
		"kind":           "other",
		"username":       firstNonEmpty(bootstrap.Email, bootstrap.Username),
		"secret":         "",
		"item_class":     "user_account",
		"source_type":    "pocketid",
		"source_ref":     "pocketid:" + stackID + ":owner",
		"access_mode":    "manage",
		"revealable":     false,
		"auto_generated": true,
		"notes":          notes,
	}
}

//nolint:goconst // Wallet field names intentionally mirror PocketBase schema keys.
func recoveryWalletFields(ownerID, stackID, stackName string, bootstrap ownerBootstrapSpec) map[string]any {
	return map[string]any{
		"owner_id":       ownerID,
		"stack_id":       stackID,
		"service_id":     recoveryWalletServiceID,
		"name":           "Stack recovery passphrase",
		"kind":           "other",
		"username":       firstNonEmpty(bootstrap.Email, bootstrap.Username),
		"secret":         encryptWalletSecretIfPossible(bootstrap.RecoveryPassphraseHash),
		"item_class":     "recovery",
		"source_type":    "stackkit",
		"source_ref":     "stackkit:" + stackID + ":recovery-passphrase-hash",
		"access_mode":    "reveal",
		"revealable":     true,
		"auto_generated": true,
		"notes":          fmt.Sprintf("Recovery passphrase hash for %s. TechStack never stores the plaintext passphrase.", stackName),
	}
}

func encryptWalletSecretIfPossible(secret string) string {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return ""
	}
	encrypted, err := tsauth.EncryptIfNeeded(tsauth.GetEncryptor(), secret)
	if err != nil {
		return secret
	}
	return encrypted
}
