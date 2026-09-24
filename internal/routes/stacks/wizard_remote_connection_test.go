package stacks

import (
	"context"
	"testing"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/specv2"
)

func TestPersistWizardRemoteConnectionBindingStoresPasswordInWallet(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := newWizardRunTestHandlers(store, &wizardRunFakeValidator{})
	h.cfg.Wallet = store

	run := &wizardRunState{
		tenantID: "tenant-1",
		ownerID:  "auth0|user-1",
		request: wizardRunRequest{
			Intent: specv2.WizardIntent{
				Server: specv2.ServerIntent{Transport: specv2.TransportConnectRemote},
			},
			Remote: &wizardRunRemoteParams{
				Host:       "10.0.0.5",
				User:       "root",
				AuthMethod: "password",
				Password:   "secret-pass",
			},
		},
	}
	stackID := "stack-remote-bind"
	if _, err := store.CreateStack(context.Background(), controlplane.CreateStackRequest{
		ID: stackID, TenantID: "tenant-1", OwnerSubjectID: "auth0|user-1", Name: "remote",
		Config: map[string]any{},
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	if err := h.persistWizardRemoteConnectionBinding(context.Background(), run, stackID); err != nil {
		t.Fatalf("persistWizardRemoteConnectionBinding: %v", err)
	}
	stack, err := store.GetStack(context.Background(), "tenant-1", stackID)
	if err != nil {
		t.Fatalf("GetStack: %v", err)
	}
	if runtimeStringFromConfig(stack.Config, "server_remote_host") != "10.0.0.5" {
		t.Fatalf("host = %q", stack.Config["server_remote_host"])
	}
	if runtimeStringFromConfig(stack.Config, "server_remote_credential_ref") != remoteSSHCredentialRef(stackID) {
		t.Fatalf("credential ref = %q", stack.Config["server_remote_credential_ref"])
	}
	creds, err := h.credentialsFromStackConfig(context.Background(), "tenant-1", "auth0|user-1", stack)
	if err != nil {
		t.Fatalf("credentialsFromStackConfig: %v", err)
	}
	if creds.Password != "secret-pass" || creds.Host != "10.0.0.5" {
		t.Fatalf("resolved creds = %#v", creds)
	}

	retryRun := &wizardRunState{
		tenantID: "tenant-1",
		ownerID:  "auth0|user-1",
		request: wizardRunRequest{
			Remote: &wizardRunRemoteParams{
				Host:       "10.0.0.5",
				User:       "root",
				AuthMethod: "password",
			},
		},
	}
	retryCreds, err := h.resolveWizardRemoteCredentials(context.Background(), retryRun, stackID)
	if err != nil {
		t.Fatalf("resolveWizardRemoteCredentials without password: %v", err)
	}
	if retryCreds.Password != "secret-pass" {
		t.Fatalf("retry creds password = %q", retryCreds.Password)
	}
}
