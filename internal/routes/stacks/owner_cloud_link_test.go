package stacks

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/authlocal"
	"github.com/pocketbase/pocketbase/core"

	"github.com/kombifyio/techstack/pkg/controlplane"
)

func cloudLinkedCreateRequest() normalizedCreateStackRequest {
	return normalizedCreateStackRequest{
		Name: "Linked Owner",
		Mode: "easy",
		UserConfig: map[string]interface{}{
			"name": "linked-owner",
			"metadata": map[string]interface{}{
				"created_by":           "wizard",
				"owner_bootstrap_mode": ownerBootstrapModeCustom,
				"owner_source":         ownerSourceCloudLinked,
			},
		},
		Options: map[string]interface{}{
			"owner_bootstrap_mode": ownerBootstrapModeCustom,
			"owner_source":         ownerSourceCloudLinked,
		},
	}
}

func TestResolveCreateOwnerBootstrap_CloudLinkedDerivesIdentityFromLink(t *testing.T) {
	resolved, denial := resolveCreateOwnerBootstrap(cloudLinkedCreateRequest(), ownerBootstrapContext{
		CloudLink: &cloudLinkIdentity{
			ExternalID:    "auth0|cloud-subject",
			Email:         "Linked.Owner@example.com",
			EmailVerified: true,
			DisplayName:   "Linked Owner",
		},
	})
	if denial != nil {
		t.Fatalf("resolveCreateOwnerBootstrap() denial = %q, want none", denial.Message)
	}

	bootstrap, ok := ownerBootstrapFromRequest(resolved)
	if !ok {
		t.Fatal("expected resolved owner bootstrap")
	}
	if bootstrap.Source != ownerSourceCloudLinked ||
		bootstrap.Email != "Linked.Owner@example.com" ||
		bootstrap.Username != "linked-owner" ||
		bootstrap.DisplayName != "Linked Owner" {
		t.Fatalf("unexpected resolved bootstrap: %+v", bootstrap)
	}
	if !strings.HasPrefix(bootstrap.RecoveryPassphraseHash, "$argon2id$") {
		t.Fatalf("expected generated argon2id recovery hash, got %q", bootstrap.RecoveryPassphraseHash)
	}
}

func TestResolveCreateOwnerBootstrap_AutomaticCloudUsesVerifiedCurrentProfile(t *testing.T) {
	req := normalizedCreateStackRequest{
		Name:       "Profile Owner",
		Mode:       "easy",
		UserConfig: map[string]interface{}{"name": "profile-owner"},
		Options: map[string]interface{}{
			"owner_bootstrap_mode": ownerBootstrapModeAuto,
			"owner_source":         ownerSourceCloud,
		},
	}
	resolved, denial := resolveCreateOwnerBootstrap(req, ownerBootstrapContext{
		CurrentProfile: &cloudLinkIdentity{
			ExternalID:    "auth0|current-owner",
			Email:         "Current.Owner@example.com",
			EmailVerified: true,
			DisplayName:   "Current Owner",
		},
	})
	if denial != nil {
		t.Fatalf("resolveCreateOwnerBootstrap() denial = %q, want none", denial.Message)
	}
	bootstrap, ok := ownerBootstrapFromRequest(resolved)
	if !ok {
		t.Fatal("expected resolved owner bootstrap")
	}
	if bootstrap.Source != ownerSourceCloud ||
		bootstrap.Email != "Current.Owner@example.com" ||
		bootstrap.Username != "current-owner" ||
		bootstrap.DisplayName != "Current Owner" {
		t.Fatalf("unexpected resolved bootstrap: %+v", bootstrap)
	}
	if !strings.HasPrefix(bootstrap.RecoveryPassphraseHash, "$argon2id$") {
		t.Fatal("expected server-generated recovery material")
	}
	jobSpec := createStackJobSpec(resolved)
	identitySpec, _ := jobSpec["identity"].(map[string]interface{})
	ownerSpec, _ := identitySpec["owner"].(map[string]interface{})
	if ownerSpec["source"] != ownerSourceLocal || ownerSpec["source_origin"] != ownerSourceCloud {
		t.Fatalf("unexpected StackKits owner provenance: %+v", ownerSpec)
	}
}

func TestResolveCreateOwnerBootstrap_AutomaticCloudRejectsUnverifiedProfile(t *testing.T) {
	req := normalizedCreateStackRequest{
		Name:       "Profile Owner",
		Mode:       "easy",
		UserConfig: map[string]interface{}{"name": "profile-owner"},
		Options: map[string]interface{}{
			"owner_bootstrap_mode": ownerBootstrapModeAuto,
			"owner_source":         ownerSourceCloud,
		},
	}
	_, denial := resolveCreateOwnerBootstrap(req, ownerBootstrapContext{
		CurrentProfile: &cloudLinkIdentity{Email: "unverified@example.com"},
	})
	if denial == nil || denial.ReasonCode != reasonCloudProfileEmailMissing {
		t.Fatalf("resolveCreateOwnerBootstrap() denial = %+v, want verified profile denial", denial)
	}
}

func TestResolveCreateOwnerBootstrap_CloudLinkedWithoutLinkDenied(t *testing.T) {
	_, denial := resolveCreateOwnerBootstrap(cloudLinkedCreateRequest(), ownerBootstrapContext{})
	if denial == nil {
		t.Fatal("expected cloud_link_missing denial")
	}
	if denial.ReasonCode != reasonCloudLinkMissing {
		t.Fatalf("denial reason_code = %q, want %q", denial.ReasonCode, reasonCloudLinkMissing)
	}
	if denial.Status != http.StatusForbidden {
		t.Fatalf("denial status = %d, want %d", denial.Status, http.StatusForbidden)
	}
}

func TestResolveCreateOwnerBootstrap_CloudLinkedUnverifiedEmailDenied(t *testing.T) {
	_, denial := resolveCreateOwnerBootstrap(cloudLinkedCreateRequest(), ownerBootstrapContext{
		CloudLink: &cloudLinkIdentity{
			ExternalID:    "auth0|cloud-subject",
			Email:         "linked.owner@example.com",
			EmailVerified: false,
		},
	})
	if denial == nil {
		t.Fatal("expected cloud_link_email_unverified denial")
	}
	if denial.ReasonCode != reasonCloudLinkEmailUnverified {
		t.Fatalf("denial reason_code = %q, want %q", denial.ReasonCode, reasonCloudLinkEmailUnverified)
	}
}

func TestValidateCreateOwnerBootstrap_CloudLinkedRejectsClientOwnerFields(t *testing.T) {
	req := cloudLinkedCreateRequest()
	req.Options["owner_email"] = "attacker@example.com"

	msg := validateCreateOwnerBootstrap(req)
	if !strings.Contains(msg, "not accepted for a cloud-linked owner") {
		t.Fatalf("validateCreateOwnerBootstrap() = %q, want cloud-linked field rejection", msg)
	}
}

func TestCreateStackJobSpec_CloudLinkedMapsWireSourceToLocal(t *testing.T) {
	resolved, denial := resolveCreateOwnerBootstrap(cloudLinkedCreateRequest(), ownerBootstrapContext{
		CloudLink: &cloudLinkIdentity{
			ExternalID:    "auth0|cloud-subject",
			Email:         "linked.owner@example.com",
			EmailVerified: true,
			DisplayName:   "Linked Owner",
		},
	})
	if denial != nil {
		t.Fatalf("resolveCreateOwnerBootstrap() denial = %q, want none", denial.Message)
	}

	jobSpec := createStackJobSpec(resolved)
	identitySpec, ok := jobSpec["identity"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected transient identity spec, got %v", jobSpec["identity"])
	}
	ownerSpec, ok := identitySpec["owner"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected transient owner spec, got %v", identitySpec["owner"])
	}
	if ownerSpec["source"] != ownerSourceLocal {
		t.Fatalf("wire owner source = %v, want %q (StackKit contract)", ownerSpec["source"], ownerSourceLocal)
	}
	if ownerSpec["source_origin"] != ownerSourceCloudLinked {
		t.Fatalf("wire owner source_origin = %v, want %q", ownerSpec["source_origin"], ownerSourceCloudLinked)
	}
	if ownerSpec["email"] != "linked.owner@example.com" {
		t.Fatalf("wire owner email = %v, want linked.owner@example.com", ownerSpec["email"])
	}
}

func TestOwnerBootstrapDenial_DetailsShape(t *testing.T) {
	details := denyCloudLinkMissing().Details()

	for _, key := range []string{"error_code", "reason_code", "required_features", "missing_features", "retryable", "user_guidance"} {
		if _, ok := details[key]; !ok {
			t.Fatalf("denial details missing key %q: %+v", key, details)
		}
	}
	if details["error_code"] != ownerBootstrapDeniedErrorCode {
		t.Fatalf("error_code = %v, want %q", details["error_code"], ownerBootstrapDeniedErrorCode)
	}
	guidance, ok := details["user_guidance"].(map[string]any)
	if !ok {
		t.Fatalf("user_guidance is not a map: %+v", details["user_guidance"])
	}
	for _, key := range []string{"title", "body", "next_steps"} {
		if _, ok := guidance[key]; !ok {
			t.Fatalf("user_guidance missing key %q: %+v", key, guidance)
		}
	}
}

func TestCloudLinkForOwner_ReadsUserLinks(t *testing.T) {
	app := newOwnerSpecTestApp(t)
	defer app.Cleanup()

	ensureOwnerSpecTestCollection(t, app, "user_links",
		&core.TextField{Name: "user"},
		&core.TextField{Name: "provider"},
		&core.TextField{Name: "external_id"},
		&core.TextField{Name: "external_email"},
		&core.TextField{Name: "external_name"},
		&core.BoolField{Name: "email_verified"},
	)
	collection, err := app.FindCollectionByNameOrId("user_links")
	if err != nil {
		t.Fatalf("find user_links collection: %v", err)
	}
	record := core.NewRecord(collection)
	record.Set("user", authlocal.BreakGlassRecordID)
	record.Set("provider", "cloud")
	record.Set("external_id", "auth0|cloud-subject")
	record.Set("external_email", "linked.owner@example.com")
	record.Set("external_name", "Linked Owner")
	record.Set("email_verified", true)
	if err := app.Save(record); err != nil {
		t.Fatalf("save user_links record: %v", err)
	}

	link := cloudLinkForOwner(app, "breakglass:"+authlocal.BreakGlassRecordID)
	if link == nil {
		t.Fatal("expected cloud link for canonical local owner")
	}
	if link.Email != "linked.owner@example.com" || !link.EmailVerified || link.DisplayName != "Linked Owner" {
		t.Fatalf("unexpected cloud link: %+v", link)
	}
	if got := cloudLinkForOwner(app, "operator-without-link"); got != nil {
		t.Fatalf("expected nil link for unknown operator, got %+v", got)
	}
}

func TestOwnerSpecBootstrapAccessForStoreDeploy_IssuesTokenForSeededOwner(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := crudRouteHandlers{walletStore: store}
	stack := &controlplane.Stack{
		ID:             "store-stack-1",
		TenantID:       "tenant-store-1",
		OwnerSubjectID: "auth0|store-owner",
		Config: map[string]any{
			"owner": map[string]any{
				"bootstrapMode": ownerBootstrapModeCustom,
				"source":        ownerSourceCloudLinked,
				"email":         "linked.owner@example.com",
				"username":      "linked-owner",
			},
		},
	}

	access, err := h.ownerSpecBootstrapAccessForStoreDeploy(t.Context(), stack)
	if err != nil {
		t.Fatalf("ownerSpecBootstrapAccessForStoreDeploy() error = %v", err)
	}
	if !access.complete() {
		t.Fatalf("expected complete owner-spec access, got %+v", access)
	}

	claims, err := verifyOwnerSpecBootstrapToken(access.Token, stack.ID, time.Now().UTC())
	if err != nil {
		t.Fatalf("verifyOwnerSpecBootstrapToken() error = %v", err)
	}
	if claims.TenantID != stack.TenantID {
		t.Fatalf("token tenant_id = %q, want stored tenant %q", claims.TenantID, stack.TenantID)
	}
}

func TestOwnerSpecBootstrapAccessForStoreDeploy_AutomaticCloudSeedsOwner(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := crudRouteHandlers{walletStore: store}
	access, err := h.ownerSpecBootstrapAccessForStoreDeploy(t.Context(), &controlplane.Stack{
		ID:             "store-stack-2",
		TenantID:       "tenant-store-2",
		OwnerSubjectID: "auth0|store-owner",
		Config: map[string]any{
			"owner": map[string]any{
				"bootstrapMode": ownerBootstrapModeAuto,
				"source":        ownerSourceCloud,
				"email":         "owner@example.com",
				"username":      "owner",
			},
			"recovery": map[string]any{"passphrase_hash": testRecoveryPassphraseHash},
		},
	})
	if err != nil {
		t.Fatalf("ownerSpecBootstrapAccessForStoreDeploy() error = %v", err)
	}
	if !access.complete() {
		t.Fatalf("SaaS auto-cloud owner must mint owner-spec access, got %+v", access)
	}
}
