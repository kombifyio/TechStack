package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/routes/trust"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/kombifywallet"
	"github.com/kombifyio/techstack/pkg/kombifywallet/walletfake"
	"github.com/kombifyio/techstack/pkg/orchestrator"
	"github.com/kombifyio/techstack/pkg/specv2"
)

const walletTestOwner = "auth0|user-1"

func walletProfile(t *testing.T, fields map[string]any) []byte {
	t.Helper()
	value, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

// kombifyJoinFixture is a connect-remote join against an existing stack, on a
// deployment that reads the kombify Wallet.
func kombifyJoinFixture(t *testing.T) (*controlplane.MemoryStore, wizardRunHandlers, *walletfake.Fake) {
	t.Helper()
	store := controlplane.NewMemoryStore()
	h := newWizardRunTestHandlers(store, &wizardRunFakeValidator{})
	fake := walletfake.New(t)
	client := fake.Client()
	h.cfg.KombifyWallet, h.crud.kombifyWallet = client, client
	wizardRunSeedV2Stack(t, store, "stack-v2", walletTestOwner, map[string]any{
		stackConfigKeySpecV2: wizardRunTestSeed(specv2.KitSlugBasement),
	})
	return store, h, fake
}

func joinRemoteRun(t *testing.T, h wizardRunHandlers, key string, remote *wizardRunRemoteParams) (int, map[string]any) {
	t.Helper()
	intent := wizardRunJoinIntent("stack-v2")
	intent.Server.Transport = specv2.TransportConnectRemote
	event, recorder := wizardRunTestEvent(t, wizardRunRequest{Intent: intent, Remote: remote}, key)
	if err := h.createWizardRun(event); err != nil {
		t.Fatalf("createWizardRun: %v", err)
	}
	if recorder.Code >= http.StatusBadRequest {
		_, details := decodeWizardRunError(t, recorder)
		return recorder.Code, details
	}
	return recorder.Code, decodeWizardRunSuccess(t, recorder)
}

// A deployment that reads the kombify Wallet takes the SSH credential from a
// Wallet item and nowhere else: a label or a password typed into the request is
// refused before anything is saved.
func TestWizardRunInKombifyModeRefusesLabelsAndPlaintextPasswords(t *testing.T) {
	store, h, fake := kombifyJoinFixture(t)
	validItem := fake.Grant(walletTestOwner, kombifywallet.ItemTypeNodeAccess, "Box", "account", walletProfile(t, map[string]any{"host": "node.example.test", "user": "u", "password": "p"}), "use")

	cases := map[string]struct {
		remote *wizardRunRemoteParams
		reason string
	}{
		"label only":           {&wizardRunRemoteParams{Host: "node.example.test", User: "u", AuthMethod: "ssh-key", SSHKeyLabel: "homelab-key"}, kombifywallet.ReasonItemRequired},
		"plaintext password":   {&wizardRunRemoteParams{Host: "node.example.test", User: "u", AuthMethod: "password", Password: "hunter2"}, kombifywallet.ReasonItemRequired},
		"item with a password": {&wizardRunRemoteParams{Host: "node.example.test", User: "u", AuthMethod: "password", Password: "hunter2", SSHWalletItemID: validItem}, kombifywallet.ReasonItemRequired},
		"item with a label":    {&wizardRunRemoteParams{Host: "node.example.test", User: "u", AuthMethod: "ssh-key", SSHKeyLabel: "homelab-key", SSHWalletItemID: validItem}, kombifywallet.ReasonItemRequired},
		"not a Wallet item id": {&wizardRunRemoteParams{Host: "node.example.test", User: "u", AuthMethod: "ssh-key", SSHWalletItemID: "wallet-1234"}, kombifywallet.ReasonItemInvalid},
	}
	for name, tc := range cases {
		status, details := joinRemoteRun(t, h, "refuse-"+name, tc.remote)
		if status != http.StatusBadRequest || details["reason_code"] != tc.reason {
			t.Fatalf("%s: status=%d details=%v, want 400 %s", name, status, details, tc.reason)
		}
	}
	stack, err := store.GetStack(context.Background(), "tenant-1", "stack-v2")
	if err != nil {
		t.Fatal(err)
	}
	if _, bound := stack.Config["server_remote_host"]; bound {
		t.Fatalf("a refused request persisted a connection: %v", stack.Config)
	}
	if items, _ := store.ListWalletItems(context.Background(), "tenant-1", ""); len(items) != 0 {
		t.Fatalf("a refused request wrote to the legacy wallet: %d items", len(items))
	}
	if requests := fake.Requests(); len(requests) != 0 {
		t.Fatalf("a refused request reached the Wallet: %d calls", len(requests))
	}
}

// A legacy deployment cannot read a Wallet item, so it says so instead of
// silently ignoring the id.
func TestWizardRunInLegacyModeRefusesAWalletItemID(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := newWizardRunTestHandlers(store, &wizardRunFakeValidator{})
	wizardRunSeedV2Stack(t, store, "stack-v2", walletTestOwner, map[string]any{stackConfigKeySpecV2: wizardRunTestSeed(specv2.KitSlugBasement)})

	status, details := joinRemoteRun(t, h, "legacy-item", &wizardRunRemoteParams{
		Host: "node.example.test", User: "u", AuthMethod: "ssh-key", SSHWalletItemID: "itm_" + "aaaaaaaaaaaaaaaaaaaaaaaaaa",
	})
	if status != http.StatusBadRequest || details["reason_code"] != kombifywallet.ReasonItemUnsupported {
		t.Fatalf("status=%d details=%v", status, details)
	}
}

// The item id the wizard persists is what the enrollment later connects with:
// it resolves through the Wallet into the parsed profile's secret, aimed at the
// stack's own host.
func TestStackBoundToAWalletItemConnectsWithTheProfileTheWalletDelivers(t *testing.T) {
	for _, tc := range []struct {
		name, method string
		profile      map[string]any
		check        func(t *testing.T, host, user string, port int, key, password string)
	}{
		{"ssh key", "ssh-key", map[string]any{"host": "node.example.test", "user": "ubuntu", "privateKey": "PRIVATE-KEY-MATERIAL"},
			func(t *testing.T, _, _ string, _ int, key, password string) {
				if key != "PRIVATE-KEY-MATERIAL" || password != "" {
					t.Fatalf("key=%q password=%q", key, password)
				}
			}},
		{"password", "password", map[string]any{"host": "node.example.test", "user": "ubuntu", "password": "s3cret-pass"},
			func(t *testing.T, _, _ string, _ int, key, password string) {
				if password != "s3cret-pass" || key != "" {
					t.Fatalf("key=%q password=%q", key, password)
				}
			}},
	} {
		store, h, fake := kombifyJoinFixture(t)
		itemID := fake.Grant(walletTestOwner, kombifywallet.ItemTypeNodeAccess, "Box", "account", walletProfile(t, tc.profile), "use")
		port := 2222

		status, data := joinRemoteRun(t, h, "bind-"+tc.name, &wizardRunRemoteParams{
			Host: "node.example.test", Port: &port, User: "ubuntu", AuthMethod: tc.method, SSHWalletItemID: itemID,
		})
		if status != http.StatusAccepted {
			t.Fatalf("%s: status=%d body=%v", tc.name, status, data)
		}
		stack, err := store.GetStack(context.Background(), "tenant-1", "stack-v2")
		if err != nil {
			t.Fatal(err)
		}
		if runtimeStringFromConfig(stack.Config, stackConfigKeyRemoteCredentialItemID) != itemID {
			t.Fatalf("%s: item id not persisted: %v", tc.name, stack.Config)
		}
		for _, legacyKey := range []string{"server_remote_credential_ref", "server_remote_ssh_key_label"} {
			if _, present := stack.Config[legacyKey]; present {
				t.Fatalf("%s: a label key %s was persisted beside the item id", tc.name, legacyKey)
			}
		}
		creds, err := h.credentialsFromStackConfig(context.Background(), "tenant-1", walletTestOwner, stack)
		if err != nil {
			t.Fatalf("%s: credentialsFromStackConfig: %v", tc.name, err)
		}
		if creds.Host != "node.example.test" || creds.User != "ubuntu" || creds.Port != 2222 {
			t.Fatalf("%s: target = %+v", tc.name, creds)
		}
		tc.check(t, creds.Host, creds.User, creds.Port, creds.PrivateKey, creds.Password)
		if items, _ := store.ListWalletItems(context.Background(), "tenant-1", ""); len(items) != 0 {
			t.Fatalf("%s: the legacy wallet was written: %d items", tc.name, len(items))
		}
	}
}

// A stack that has a Wallet item id must never be answered from a legacy row
// that happens to carry the same label.
func TestStackWithAWalletItemNeverFallsBackToTheLegacyLabel(t *testing.T) {
	store, h, fake := kombifyJoinFixture(t)
	if _, err := store.UpsertWalletItem(context.Background(), controlplane.WalletItem{
		ID: "legacy-key", TenantID: "tenant-1", ItemType: "ssh_key",
		Metadata: map[string]any{"name": "homelab-key", "kind": "ssh_key", "owner_id": walletTestOwner, "secret": "LEGACY-PRIVATE-KEY", "has_secret": true},
	}); err != nil {
		t.Fatal(err)
	}
	// The owner never shared this item with Techstack, so the Wallet refuses.
	unshared := fake.Grant(walletTestOwner, kombifywallet.ItemTypeNodeAccess, "Box", "account", walletProfile(t, map[string]any{"host": "node.example.test", "user": "u", "privateKey": "K"}), "read")
	stack := &controlplane.Stack{ID: "stack-v2", Config: map[string]any{
		"server_remote_host": "node.example.test", "server_remote_user": "u", "server_remote_auth_method": "ssh-key",
		"server_remote_ssh_key_label": "homelab-key", stackConfigKeyRemoteCredentialItemID: unshared,
	}}

	creds, err := h.credentialsFromStackConfig(context.Background(), "tenant-1", walletTestOwner, stack)
	denial, ok := kombifywallet.AsDenial(err)
	if !ok || denial.Reason != kombifywallet.ReasonRightMissing || denial.RequiredRight != "use" || denial.ItemID != unshared {
		t.Fatalf("err = %v, want a use-right denial for %s", err, unshared)
	}
	if creds.PrivateKey != "" || creds.Password != "" {
		t.Fatalf("credentials leaked past the denial: %+v", creds)
	}
}

// A stack created before the switch keeps its label until the migration runs.
func TestStackWithoutAWalletItemStillResolvesItsLegacyLabelInKombifyMode(t *testing.T) {
	store, h, _ := kombifyJoinFixture(t)
	h.cfg.Wallet = store
	if _, err := store.UpsertWalletItem(context.Background(), controlplane.WalletItem{
		ID: "legacy-key", TenantID: "tenant-1", ItemType: "ssh_key",
		Metadata: map[string]any{"name": "homelab-key", "kind": "ssh_key", "owner_id": walletTestOwner, "secret": "LEGACY-PRIVATE-KEY", "has_secret": true},
	}); err != nil {
		t.Fatal(err)
	}
	stack := &controlplane.Stack{ID: "stack-old", Config: map[string]any{
		"server_remote_host": "node.example.test", "server_remote_user": "u", "server_remote_auth_method": "ssh-key",
		"server_remote_ssh_key_label": "homelab-key",
	}}

	creds, err := h.credentialsFromStackConfig(context.Background(), "tenant-1", walletTestOwner, stack)
	if err != nil || creds.PrivateKey != "LEGACY-PRIVATE-KEY" {
		t.Fatalf("pre-switch stack: creds=%+v err=%v", creds, err)
	}
}

// A Wallet outage retries the enrollment; a missing right does not.
func TestRemoteEnrollmentRetriesAWalletOutageButNotAMissingRight(t *testing.T) {
	_, h, fake := kombifyJoinFixture(t)
	shared := fake.Grant(walletTestOwner, kombifywallet.ItemTypeNodeAccess, "Box", "account", walletProfile(t, map[string]any{"host": "h", "user": "u", "password": "p"}), "read")
	stack := &controlplane.Stack{ID: "stack-v2", Config: map[string]any{
		"server_remote_host": "h", "server_remote_user": "u", "server_remote_auth_method": "password", stackConfigKeyRemoteCredentialItemID: shared,
	}}

	_, missingRight := h.credentialsFromStackConfig(context.Background(), "tenant-1", walletTestOwner, stack)
	var failure *jobs.RemoteEnrollmentFailure
	if err := remoteEnrollmentCredentialFailure(missingRight); !errors.As(err, &failure) || failure.Reason != kombifywallet.ReasonRightMissing {
		t.Fatalf("a missing right must fail the enrollment, got %v", err)
	}

	fake.Outage(true)
	_, outage := h.credentialsFromStackConfig(context.Background(), "tenant-1", walletTestOwner, stack)
	var retryable *jobs.RemoteEnrollmentRetryableError
	if err := remoteEnrollmentCredentialFailure(outage); !errors.As(err, &retryable) || retryable.Reason != kombifywallet.ReasonUnavailable {
		t.Fatalf("a Wallet outage must be retryable, got %v", err)
	}
}

// A rollout saves recovery material in the owner's Wallet, so it does not start
// until the owner allowed Techstack to save there.
func TestDeployInKombifyModeRefusesToStartWithoutTheCreateRight(t *testing.T) {
	ctx := t.Context()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-deploy", TenantID: walletTestOwner, OwnerSubjectID: walletTestOwner, Name: "Deploy", Status: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	fake := walletfake.New(t)
	h := crudRouteHandlers{stackStore: store, jobStore: store, kombifyWallet: fake.Client()}

	event, recorder := stackStoreRequestEvent(walletTestOwner, "")
	event.Request.SetPathValue("id", "stack-deploy")
	_ = h.deployStack(event)
	_, details := decodeWizardRunError(t, recorder)
	if recorder.Code != http.StatusConflict || details["reason_code"] != kombifywallet.ReasonCreateRightMissing || details["required_right"] != "create" {
		t.Fatalf("status=%d details=%v, want 409 wallet_create_right_missing", recorder.Code, details)
	}
	if jobsQueued, _ := store.ListJobsByStack(ctx, walletTestOwner, "stack-deploy", 10); len(jobsQueued) != 0 {
		t.Fatalf("a refused rollout queued %d jobs", len(jobsQueued))
	}

	fake.AllowCreate(walletTestOwner)
	event, recorder = stackStoreRequestEvent(walletTestOwner, "")
	event.Request.SetPathValue("id", "stack-deploy")
	_ = h.deployStack(event)
	// With the right granted the request moves past the preflight and reaches
	// the next gate, which has no orchestrator in this fixture.
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("after the grant: status=%d body=%s, want the orchestrator gate (503)", recorder.Code, recorder.Body.String())
	}
}

func ownerRecoveryFixture(t *testing.T, fake *walletfake.Fake) (*controlplane.MemoryStore, crudRouteHandlers) {
	t.Helper()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(t.Context(), controlplane.CreateStackRequest{
		ID: "stack-1", TenantID: "owner-1", OwnerSubjectID: "owner-1", Config: map[string]any{
			"owner": map[string]any{"bootstrapMode": ownerBootstrapModeCustom, "source": ownerSourceLocal, "email": "owner@example.com", "username": "owner", "displayName": "Owner"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	return store, crudRouteHandlers{stackStore: store, walletStore: store, activityStore: store, kombifyWallet: fake.Client()}
}

// The recovery hash lives in the owner's Wallet, never in the legacy store, and
// a Wallet outage during the StackKit's one bootstrap call does not spend its
// single-use token.
func TestOwnerRecoveryHashLivesInTheWalletAndAnOutageDoesNotSpendTheBootstrapToken(t *testing.T) {
	t.Setenv(ownerSpecBootstrapTokenSecretEnv, "0123456789abcdef0123456789abcdef")
	fake := walletfake.New(t)
	fake.AllowCreate("owner-1")
	store, h := ownerRecoveryFixture(t, fake)

	err := h.applyOwnerBootstrap(t.Context(), "owner-1", "owner-1", "stack-1", "Stack", ownerBootstrapSpec{
		BootstrapMode: ownerBootstrapModeCustom, Source: ownerSourceLocal, Email: "owner@example.com", Username: "owner",
		RecoveryPassphraseHash: testRecoveryPassphraseHash,
	})
	if err != nil {
		t.Fatalf("applyOwnerBootstrap: %v", err)
	}
	items := fake.Items("owner-1")
	if len(items) != 1 || items[0].Scope != "project:stack-1" || string(items[0].Value()) != testRecoveryPassphraseHash || items[0].Type != kombifywallet.ItemTypeGenericSecret {
		t.Fatalf("Wallet items = %+v", items)
	}
	if rows, _ := store.ListWalletItems(t.Context(), "owner-1", ""); len(rows) != 0 {
		t.Fatalf("the legacy wallet was written: %d rows", len(rows))
	}
	stack, err := store.GetStack(t.Context(), "owner-1", "stack-1")
	if err != nil || runtimeStringFromConfig(stack.Config, stackConfigKeyOwnerRecoveryItemID) != items[0].ID {
		t.Fatalf("stack does not record the recovery item: %v %v", stack, err)
	}

	access, err := h.issueOwnerSpecBootstrapAccessForTenant(t.Context(), "owner-1", "stack-1", "owner-1", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	fake.Outage(true)
	event, recorder := ownerSpecRequestEvent("stack-1", access.Token)
	if err := h.ownerSpec(event); err != nil {
		t.Fatal(err)
	}
	_, details := decodeWizardRunError(t, recorder)
	if recorder.Code != http.StatusServiceUnavailable || details["reason_code"] != kombifywallet.ReasonUnavailable {
		t.Fatalf("outage: status=%d details=%v", recorder.Code, details)
	}

	fake.Outage(false)
	event, recorder = ownerSpecRequestEvent("stack-1", access.Token)
	if err := h.ownerSpec(event); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Data ownerSpecResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK || envelope.Data.Identity.Recovery.PassphraseHash != testRecoveryPassphraseHash {
		t.Fatalf("after the outage the same token must still work: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

// A node access item is saved for one host. A stack whose persisted target is
// another host never receives its credential.
func TestStackConnectionNeverSendsAWalletItemToAHostItWasNotSavedFor(t *testing.T) {
	_, h, fake := kombifyJoinFixture(t)
	itemID := fake.Grant(walletTestOwner, kombifywallet.ItemTypeNodeAccess, "Box", "account",
		walletProfile(t, map[string]any{"host": "node.example.test", "user": "u", "password": "WALLET-PASSWORD"}), "use")
	stack := &controlplane.Stack{ID: "stack-v2", Config: map[string]any{
		"server_remote_host": "attacker.example.test", "server_remote_user": "u", "server_remote_auth_method": "password",
		stackConfigKeyRemoteCredentialItemID: itemID,
	}}

	creds, err := h.credentialsFromStackConfig(context.Background(), "tenant-1", walletTestOwner, stack)
	denial, ok := kombifywallet.AsDenial(err)
	if !ok || denial.Reason != kombifywallet.ReasonHostMismatch {
		t.Fatalf("err = %v, want a host mismatch", err)
	}
	if creds.Password != "" {
		t.Fatalf("the password was handed to the wrong host: %+v", creds)
	}
}

// Creating a stack with an owner bootstrap saves its recovery hash in the
// owner's Wallet, so Techstack refuses before it persists anything while it may
// not save there, and saves the hash once it may.
func TestCreateStackWithAnOwnerBootstrapInKombifyModeNeedsTheCreateRight(t *testing.T) {
	t.Setenv(ownerSpecBootstrapTokenSecretEnv, "0123456789abcdef0123456789abcdef")
	store := controlplane.NewMemoryStore()
	fake := walletfake.New(t)
	h := crudRouteHandlers{stackStore: store, homelabStore: store, jobStore: store, walletStore: store, kombifyWallet: fake.Client()}
	request := normalizedCreateStackRequest{
		Name: "Owner bootstrap", Mode: stackModeEasy,
		UserConfig: map[string]interface{}{ownerConfigKey: map[string]interface{}{
			"bootstrapMode": ownerBootstrapModeCustom, "source": ownerSourceLocal, "email": "owner@example.test",
		}},
		Options: map[string]interface{}{"recovery_passphrase_hash": testRecoveryPassphraseHash},
	}

	event, recorder := stackStoreRequestEvent("owner-1", "")
	_ = h.createNormalizedStack(event, "owner-1", "owner-1", request)
	_, details := decodeWizardRunError(t, recorder)
	stacks, _ := store.ListStacksByTenant(t.Context(), "owner-1")
	if recorder.Code != http.StatusConflict || details["reason_code"] != kombifywallet.ReasonCreateRightMissing || len(stacks) != 0 {
		t.Fatalf("status=%d details=%v stacks=%d, want 409 and nothing persisted", recorder.Code, details, len(stacks))
	}

	fake.AllowCreate("owner-1")
	event, recorder = stackStoreRequestEvent("owner-1", "")
	if err := h.createNormalizedStack(event, "owner-1", "owner-1", request); err != nil {
		t.Fatalf("create with the right: %v", err)
	}
	stacks, _ = store.ListStacksByTenant(t.Context(), "owner-1")
	items := fake.Items("owner-1")
	if recorder.Code != http.StatusAccepted || len(stacks) != 1 || len(items) != 1 || string(items[0].Value()) != testRecoveryPassphraseHash {
		t.Fatalf("status=%d stacks=%d wallet items=%+v body=%s", recorder.Code, len(stacks), items, recorder.Body.String())
	}
	if items[0].Scope != "project:"+stacks[0].ID || runtimeStringFromConfig(stacks[0].Config, stackConfigKeyOwnerRecoveryItemID) != items[0].ID {
		t.Fatalf("the recovery item is not bound to the stack: scope=%q config=%v", items[0].Scope, stacks[0].Config)
	}
}

func resumeRemoteEnrollmentEvent(t *testing.T, body string) (*httpx.Event, *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/stacks/stack-remote/resume-remote-enrollment", strings.NewReader(body))
	req.SetPathValue("id", "stack-remote")
	req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{UserID: walletTestOwner, OrgID: "tenant-1"}))
	recorder := httptest.NewRecorder()
	return &httpx.Event{Request: req, Response: recorder}, recorder
}

// Resuming an enrollment never takes a typed password where credentials live in
// the kombify Wallet; it rebinds the stack to another Wallet item instead.
func TestResumeRemoteEnrollmentInKombifyModeRefusesPasswordsAndRebindsToAWalletItem(t *testing.T) {
	store := controlplane.NewMemoryStore()
	fake := walletfake.New(t)
	h := wizardRunHandlers{
		crud: crudRouteHandlers{stackStore: store, jobStore: store},
		cfg:  WizardRunRouteConfig{Trust: trust.RouteStores{Stacks: store, Workers: store, Jobs: store}, KombifyWallet: fake.Client()},
	}
	oldItem := fake.Grant(walletTestOwner, kombifywallet.ItemTypeNodeAccess, "Old", "account", walletProfile(t, map[string]any{"host": "node.example.test", "user": "u", "privateKey": "OLD"}), "use")
	newItem := fake.Grant(walletTestOwner, kombifywallet.ItemTypeNodeAccess, "New", "account", walletProfile(t, map[string]any{"host": "node.example.test", "user": "u", "privateKey": "NEW"}), "use")
	if _, err := store.CreateStack(t.Context(), controlplane.CreateStackRequest{
		ID: "stack-remote", TenantID: "tenant-1", OwnerSubjectID: walletTestOwner, Name: "Remote", Status: "running",
		Config: map[string]any{
			"server_provisioning_mode": specv2.TransportConnectRemote, "server_remote_host": "node.example.test", "server_remote_user": "u",
			"server_remote_auth_method": "ssh-key", stackConfigKeyRemoteCredentialItemID: oldItem,
		},
	}); err != nil {
		t.Fatal(err)
	}
	boundItem := func() string {
		stack, err := store.GetStack(t.Context(), "tenant-1", "stack-remote")
		if err != nil {
			t.Fatal(err)
		}
		return runtimeStringFromConfig(stack.Config, stackConfigKeyRemoteCredentialItemID)
	}

	event, recorder := resumeRemoteEnrollmentEvent(t, `{"password":"typed-password"}`)
	if err := h.resumeRemoteEnrollment(event); err != nil {
		t.Fatal(err)
	}
	_, details := decodeWizardRunError(t, recorder)
	if recorder.Code != http.StatusBadRequest || details["reason_code"] != kombifywallet.ReasonItemRequired || boundItem() != oldItem {
		t.Fatalf("typed password: status=%d details=%v bound=%q", recorder.Code, details, boundItem())
	}

	event, recorder = resumeRemoteEnrollmentEvent(t, `{"ssh_wallet_item_id":"`+newItem+`"}`)
	_ = h.resumeRemoteEnrollment(event)
	if boundItem() != newItem {
		t.Fatalf("the stack was not rebound to the picked item: bound=%q status=%d body=%s", boundItem(), recorder.Code, recorder.Body.String())
	}
}

// Retrying a rollout and resuming an enrollment start a rollout again, so they
// need the same create right as a first deploy.
func TestRolloutRecoveryInKombifyModeNeedsTheCreateRight(t *testing.T) {
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(t.Context(), controlplane.CreateStackRequest{
		ID: "stack-recover", TenantID: walletTestOwner, OwnerSubjectID: walletTestOwner, Name: "Recover", Status: "error",
	}); err != nil {
		t.Fatal(err)
	}
	orch := orchestrator.New(&orchestrator.Config{Workers: 1, StackStore: store, JobStore: store}, nil)
	defer orch.Stop()
	h := crudRouteHandlers{orch: orch, stackStore: store, jobStore: store, kombifyWallet: walletfake.New(t).Client()}

	for name, handler := range map[string]func(*httpx.Event) error{"retry rollout": h.retryStackRollout, "resume enrollment": h.resumeStackEnrollment} {
		event, recorder := stackStoreRequestEvent(walletTestOwner, "")
		event.Request.SetPathValue("id", "stack-recover")
		_ = handler(event)
		_, details := decodeWizardRunError(t, recorder)
		if recorder.Code != http.StatusConflict || details["reason_code"] != kombifywallet.ReasonCreateRightMissing {
			t.Fatalf("%s: status=%d body=%s, want 409 wallet_create_right_missing", name, recorder.Code, recorder.Body.String())
		}
	}
}
