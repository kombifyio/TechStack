package stacks

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/kombifywallet"
	"github.com/kombifyio/techstack/pkg/remoteguard"
	"github.com/kombifyio/techstack/pkg/specv2"
)

// stackConfigKeyRemoteCredentialItemID holds the kombify Wallet node access
// item a connect-remote stack logs in with. A stack that has it never falls
// back to a name label; a stack created before the switch has only the label
// until the P3.3 migration executes.
const stackConfigKeyRemoteCredentialItemID = "server_remote_credential_item_id"

// stackConfigKeyOwnerRecoveryItemID holds the kombify Wallet item that keeps a
// stack's owner-bootstrap recovery hash.
const stackConfigKeyOwnerRecoveryItemID = "owner_recovery_wallet_item_id"

var kombifyWalletBinding struct {
	mu     sync.RWMutex
	client *kombifywallet.Client
}

// ConfigureKombifyWallet late-binds the kombify Wallet client for the CRUD and
// import routes. A nil client is the legacy wallet.
func ConfigureKombifyWallet(client *kombifywallet.Client) {
	kombifyWalletBinding.mu.Lock()
	defer kombifyWalletBinding.mu.Unlock()
	kombifyWalletBinding.client = client
}

func currentKombifyWallet() *kombifywallet.Client {
	kombifyWalletBinding.mu.RLock()
	defer kombifyWalletBinding.mu.RUnlock()
	return kombifyWalletBinding.client
}

// remoteCredentialSourceDenial checks, at the request boundary, that a
// connect-remote run names its SSH credential the way this deployment reads it:
// a kombify Wallet item (and no label or plaintext password), or, for a legacy
// deployment, a label and no item.
func (h wizardRunHandlers) remoteCredentialSourceDenial(request wizardRunRequest) *kombifywallet.Denial {
	remote := request.Remote
	if remote == nil || wizardRunTransport(request) != specv2.TransportConnectRemote {
		return nil
	}
	itemID := strings.TrimSpace(remote.SSHWalletItemID)
	if h.cfg.KombifyWallet == nil {
		if itemID != "" {
			return kombifywallet.ItemUnsupported()
		}
		return nil
	}
	if itemID == "" || strings.TrimSpace(remote.SSHKeyLabel) != "" || strings.TrimSpace(remote.Password) != "" {
		return kombifywallet.ItemRequired()
	}
	if !kombifywallet.IsItemID(itemID) {
		return kombifywallet.ItemInvalid()
	}
	return nil
}

// credentialsFromWalletItem reads a stack's SSH credential from its kombify
// Wallet node access item. The stack's own host, user and port are the target;
// the item supplies only the secret, and only for the host it is bound to.
func (h wizardRunHandlers) credentialsFromWalletItem(
	ctx context.Context, ownerID, itemID string, target remoteguard.Credentials, authMethod string,
) (remoteguard.Credentials, error) {
	if h.cfg.KombifyWallet == nil {
		return remoteguard.Credentials{}, kombifywallet.SelfHostedOnly(
			"This stack's SSH credential is in the kombify Wallet",
			"This deployment does not read the kombify Wallet, so the stack's SSH credential cannot be used here.")
	}
	access, err := h.cfg.KombifyWallet.NodeAccess(ctx, ownerID, itemID)
	if err != nil {
		return remoteguard.Credentials{}, err
	}
	defer access.Wipe()
	if err := access.RequireHost(target.Host); err != nil {
		return remoteguard.Credentials{}, err
	}
	if authMethod == "password" {
		if err := access.RequirePassword(); err != nil {
			return remoteguard.Credentials{}, err
		}
		// remoteguard takes plain strings; the copy lives for this attempt only.
		target.Password = access.Password.Reveal()
		return target, nil
	}
	if err := access.RequirePrivateKey(); err != nil {
		return remoteguard.Credentials{}, err
	}
	target.PrivateKey = access.PrivateKey.Reveal()
	return target, nil
}

// requireWalletCreateRight is the rollout preflight for a deployment that
// keeps credentials in the kombify Wallet: a rollout writes recovery material
// and StackKit outputs there, so it refuses to start while Techstack may not
// save to the owner's Wallet. It answers the request and returns a non-nil
// error when it refused.
func (h crudRouteHandlers) requireWalletCreateRight(e *httpx.Event, ownerID string) error {
	if h.kombifyWallet == nil {
		return nil
	}
	if err := h.kombifyWallet.CanCreate(e.Request.Context(), ownerID); err != nil {
		denial := kombifywallet.Denied(err)
		_ = denial.Write(e)
		return denial
	}
	return nil
}

// walletDenialOrNil returns the Wallet denial inside err, if any.
func walletDenialOrNil(err error) *kombifywallet.Denial {
	denial, _ := kombifywallet.AsDenial(err)
	return denial
}

// remoteEnrollmentCredentialFailure turns a credential failure into the job
// failure the enrollment queue understands: a Wallet outage retries, anything
// else is actionable and does not.
func remoteEnrollmentCredentialFailure(err error) error {
	denial := walletDenialOrNil(err)
	if denial == nil {
		return err
	}
	if denial.Retryable {
		return &jobs.RemoteEnrollmentRetryableError{Reason: denial.Reason, Message: denial.Title, Cause: denial}
	}
	return &jobs.RemoteEnrollmentFailure{Reason: denial.Reason, Message: denial.Title, Cause: denial}
}

// ownerRecoveryDisplayName is the Wallet display name of the owner-bootstrap
// recovery item. It carries no personal data.
const ownerRecoveryDisplayName = "Stack recovery passphrase"

// saveOwnerRecoveryInKombifyWallet keeps the owner-bootstrap recovery hash in
// the owner's kombify Wallet instead of the legacy wallet: one generic secret
// in the stack's project, whose item id the stack configuration records for
// the owner spec to read back. Nothing is written to the legacy store.
func (h crudRouteHandlers) saveOwnerRecoveryInKombifyWallet(ctx context.Context, tenantID, ownerID, stackID string, bootstrap ownerBootstrapSpec) error {
	hash := strings.TrimSpace(bootstrap.RecoveryPassphraseHash)
	if hash == "" {
		return nil
	}
	scope, ok := kombifywallet.ProjectScope(stackID)
	if !ok {
		return errors.New("stack id is not a valid Wallet project reference")
	}
	if h.stackStore == nil {
		return errors.New("stack store is not configured")
	}
	value := []byte(hash)
	defer clear(value)
	saved, err := h.kombifyWallet.Ensure(ctx, ownerID, kombifywallet.ItemSpec{
		Type: kombifywallet.ItemTypeGenericSecret, DisplayName: ownerRecoveryDisplayName,
		Description: "Argon2id hash of the recovery passphrase of a kombify stack. Techstack never stores the passphrase.",
		Scope:       scope, Value: value,
	})
	if err != nil {
		return err
	}
	// Other create phases write the stack too; a lost compare-and-swap rereads.
	for attempt := 0; attempt < 3; attempt++ {
		stack, getErr := h.stackStore.GetStack(ctx, tenantID, stackID)
		if getErr != nil {
			return getErr
		}
		config := cloneMapForMutation(stack.Config)
		if config == nil {
			config = map[string]any{}
		}
		config[stackConfigKeyOwnerRecoveryItemID] = saved.ItemID
		_, err = h.stackStore.CompareAndSwapStackConfig(ctx, controlplane.StackConfigCAS{
			TenantID: tenantID, StackID: stackID, ExpectedUpdatedAt: stack.UpdatedAt, Config: config,
		})
		if !errors.Is(err, controlplane.ErrConflict) {
			return err
		}
	}
	return err
}

// requireWalletForCreate refuses to start a create that writes to the owner's
// kombify Wallet (an owner bootstrap with recovery material) or that rolls out
// by itself (a managed server) while Techstack may not save there.
func (h crudRouteHandlers) requireWalletForCreate(e *httpx.Event, ownerID string, normalized normalizedCreateStackRequest) error {
	if h.kombifyWallet == nil {
		return nil
	}
	bootstrap, hasBootstrap := ownerBootstrapFromRequest(normalized)
	writesRecovery := hasBootstrap && ownerSourceSeedsPocketID(bootstrap.Source)
	if !writesRecovery && !shouldStartRolloutAfterCreate(createStackJobSpec(normalized)) {
		return nil
	}
	return h.requireWalletCreateRight(e, ownerID)
}
