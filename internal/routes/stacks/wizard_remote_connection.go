package stacks

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/kombifywallet"
	"github.com/kombifyio/techstack/pkg/remoteguard"
	"github.com/kombifyio/techstack/pkg/specv2"
)

const remoteSSHCredentialServiceID = "remote-ssh"

// remoteSSHCredentialRef is the stable wallet label and stack credential_ref
// for one connect-remote deployment binding.
func remoteSSHCredentialRef(stackID string) string {
	stackID = strings.TrimSpace(stackID)
	if stackID == "" {
		return remoteSSHCredentialServiceID
	}
	return remoteSSHCredentialServiceID + ":" + stackID
}

// persistWizardRemoteConnectionBinding stores the non-secret remote target on
// the stack and custodies SSH secrets in the wallet. Connection and StackKit
// rollout are intentionally separate: a later enrollment or rollout retry reads
// this binding instead of requiring the wizard payload again.
func (h wizardRunHandlers) persistWizardRemoteConnectionBinding(ctx context.Context, run *wizardRunState, stackID string) error {
	if run == nil || run.request.Remote == nil || wizardRunTransport(run.request) != specv2.TransportConnectRemote {
		return nil
	}
	if h.crud.stackStore == nil {
		return errors.New("stack store is not configured")
	}
	remote := run.request.Remote
	host := strings.TrimSpace(remote.Host)
	user := strings.TrimSpace(remote.User)
	if host == "" || user == "" {
		return errors.New("remote host and SSH user are required")
	}
	authMethod := strings.ToLower(strings.TrimSpace(remote.AuthMethod))
	if authMethod == "" {
		authMethod = "ssh-key"
	}
	credRef := strings.TrimSpace(remote.SSHKeyLabel)
	walletItemID := ""
	switch {
	case h.cfg.KombifyWallet != nil:
		// The credential already lives in the owner's kombify Wallet; the stack
		// keeps only the item id. Nothing is written to the legacy wallet.
		walletItemID = strings.TrimSpace(remote.SSHWalletItemID)
		if !kombifywallet.IsItemID(walletItemID) {
			return kombifywallet.ItemRequired()
		}
		credRef = ""
	case authMethod == "password":
		password := strings.TrimSpace(remote.Password)
		if password == "" {
			return errors.New("SSH password is required to persist the remote connection")
		}
		if h.cfg.Wallet == nil {
			return errors.New("wallet is not configured; store the SSH password before connecting remotely")
		}
		credRef = remoteSSHCredentialRef(stackID)
		if err := h.upsertRemoteSSHPasswordWalletItem(ctx, run.tenantID, run.ownerID, stackID, credRef, password); err != nil {
			return err
		}
	case credRef == "":
		return errors.New("SSH key label is required for key authentication")
	}

	stack, err := h.crud.stackStore.GetStack(ctx, run.tenantID, stackID)
	if err != nil {
		return err
	}
	newConfig := cloneMapForMutation(stack.Config)
	newConfig["server_remote_host"] = host
	newConfig["server_remote_user"] = user
	newConfig["server_remote_host_present"] = true
	newConfig["server_remote_user_present"] = true
	newConfig["server_remote_auth_method"] = authMethod
	if walletItemID != "" {
		newConfig[stackConfigKeyRemoteCredentialItemID] = walletItemID
		delete(newConfig, "server_remote_credential_ref")
		delete(newConfig, "server_remote_ssh_key_label")
	} else {
		newConfig["server_remote_credential_ref"] = credRef
	}
	if remote.Port != nil && *remote.Port > 0 {
		newConfig["server_remote_port"] = *remote.Port
	}
	if remote.UseSudo {
		newConfig["server_remote_use_sudo"] = true
	}
	if authMethod != "password" && walletItemID == "" {
		newConfig["server_remote_ssh_key_label"] = credRef
	}
	_, err = h.crud.stackStore.CompareAndSwapStackConfig(ctx, controlplane.StackConfigCAS{
		TenantID:          run.tenantID,
		StackID:           stackID,
		ExpectedUpdatedAt: stack.UpdatedAt,
		Config:            newConfig,
	})
	if err != nil {
		return err
	}
	if walletItemID == "" {
		remote.SSHKeyLabel = credRef
	}
	return nil
}

func (h wizardRunHandlers) upsertRemoteSSHPasswordWalletItem(ctx context.Context, tenantID, ownerID, stackID, name, password string) error {
	if h.cfg.KombifyWallet != nil {
		// Defence in depth: kombify mode never writes the legacy wallet.
		return kombifywallet.ItemRequired()
	}
	encrypted, err := auth.EncryptIfNeeded(auth.GetEncryptor(), password)
	if err != nil {
		return fmt.Errorf("encrypt remote ssh password: %w", err)
	}
	item := controlplane.WalletItem{
		ID:       fmt.Sprintf("%s:%s", stackID, remoteSSHCredentialServiceID),
		TenantID: tenantID,
		StackID:  stackID,
		ItemType: "ssh_password",
		Provider: "remote_ssh",
		Metadata: map[string]any{
			"id":                fmt.Sprintf("%s:%s", stackID, remoteSSHCredentialServiceID),
			"kind":              "ssh_password",
			"name":              name,
			"owner_id":          ownerID,
			"owner_subject_id":  ownerID,
			"kit_deployment_id": stackID,
			"stack_id":          stackID,
			"secret":            encrypted,
			"has_secret":        true,
		},
	}
	_, err = h.cfg.Wallet.UpsertWalletItem(ctx, item)
	return err
}

func (h wizardRunHandlers) credentialsFromStackConfig(ctx context.Context, tenantID, ownerID string, stack *controlplane.Stack) (remoteguard.Credentials, error) {
	if stack == nil {
		return remoteguard.Credentials{}, errors.New("stack is required")
	}
	host := strings.TrimSpace(runtimeStringFromConfig(stack.Config, "server_remote_host"))
	user := strings.TrimSpace(runtimeStringFromConfig(stack.Config, "server_remote_user"))
	if host == "" || user == "" {
		return remoteguard.Credentials{}, errors.New("persisted remote SSH target is incomplete")
	}
	port := 22
	if raw := stack.Config["server_remote_port"]; raw != nil {
		switch typed := raw.(type) {
		case int:
			if typed > 0 {
				port = typed
			}
		case float64:
			if typed > 0 {
				port = int(typed)
			}
		case string:
			if parsed, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil && parsed > 0 {
				port = parsed
			}
		}
	}
	authMethod := strings.ToLower(strings.TrimSpace(runtimeStringFromConfig(stack.Config, "server_remote_auth_method")))
	if authMethod == "" {
		authMethod = "ssh-key"
	}
	credRef := firstNonEmpty(
		runtimeStringFromConfig(stack.Config, "server_remote_credential_ref"),
		runtimeStringFromConfig(stack.Config, "server_remote_ssh_key_label"),
	)
	creds := remoteguard.Credentials{
		Host:    host,
		Port:    port,
		User:    user,
		UseSudo: remoteUseSudoFromConfig(stack.Config),
	}
	// A stack bound to a kombify Wallet item reads only that item. It never
	// falls back to a name label, which would resolve against the legacy store.
	if itemID := strings.TrimSpace(runtimeStringFromConfig(stack.Config, stackConfigKeyRemoteCredentialItemID)); itemID != "" {
		return h.credentialsFromWalletItem(ctx, ownerID, itemID, creds, authMethod)
	}
	if authMethod == "password" {
		if credRef == "" {
			credRef = remoteSSHCredentialRef(stack.ID)
		}
		password, err := h.resolveWalletSSHPassword(ctx, tenantID, ownerID, credRef)
		if err != nil {
			return remoteguard.Credentials{}, err
		}
		creds.Password = password
		return creds, nil
	}
	if credRef == "" {
		return remoteguard.Credentials{}, errors.New("persisted remote SSH credential reference is missing")
	}
	key, err := h.resolveWalletSSHKey(ctx, tenantID, ownerID, credRef)
	if err != nil {
		return remoteguard.Credentials{}, err
	}
	creds.PrivateKey = key
	return creds, nil
}

func remoteUseSudoFromConfig(config map[string]any) bool {
	if value, present := runtimeBoolFromConfig(config, "server_remote_use_sudo"); present {
		return value
	}
	return false
}

func (h wizardRunHandlers) resolveWalletSSHPassword(ctx context.Context, tenantID, ownerID, label string) (string, error) {
	if h.cfg.Wallet == nil {
		return "", errors.New("wallet is not configured")
	}
	label = strings.TrimSpace(label)
	if label == "" {
		return "", errors.New("SSH password label is required")
	}
	items, err := h.cfg.Wallet.ListWalletItems(ctx, tenantID, "")
	if err != nil {
		return "", errors.New("could not load wallet credentials for SSH password lookup")
	}
	for _, item := range items {
		if !walletItemOwnedBy(item.Metadata, ownerID) {
			continue
		}
		name := strings.TrimSpace(walletMetadataString(item.Metadata["name"]))
		if !strings.EqualFold(name, label) {
			continue
		}
		itemKind := strings.TrimSpace(firstNonEmptyWalletString(
			walletMetadataString(item.Metadata["kind"]),
			item.ItemType,
		))
		if itemKind != "ssh_password" {
			continue
		}
		secret, revealErr := auth.DecryptIfNeeded(auth.GetEncryptor(), walletMetadataString(item.Metadata["secret"]))
		if revealErr != nil {
			return "", errors.New("could not decrypt the matching wallet credential")
		}
		secret = strings.TrimSpace(secret)
		if secret == "" {
			return "", errors.New("the matching wallet credential has no secret material")
		}
		return secret, nil
	}
	return "", errors.New("no wallet credential matched the SSH password label")
}
