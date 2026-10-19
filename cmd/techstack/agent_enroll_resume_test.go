package main

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/agent/hostidentity"
)

// The installer asks the installed Guard whether an interrupted enrollment holds
// the capability it was given, because the control plane already claimed that
// capability and refuses to let it download a binary again. Only the capability
// whose redemption was interrupted resumes; any other one downloads and starts
// a new attempt.
func TestAgentEnrollResumableOnlyForTheInterruptedCapability(t *testing.T) {
	stateDir := t.TempDir()
	manager, err := hostidentity.Open(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	const server = "https://control.example"
	if _, err := manager.BeginAttempt(); err != nil {
		t.Fatal(err)
	}
	if err := manager.SaveBootstrap(hostidentity.Bootstrap{Server: server, Token: "interrupted-capability"}); err != nil {
		t.Fatal(err)
	}

	resumable := func(capability string) bool {
		t.Helper()
		tokenFile := filepath.Join(t.TempDir(), "token")
		if err := os.WriteFile(tokenFile, []byte(capability), 0o600); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := runAgentEnroll(t.Context(), []string{
			"--resumable", "--server", server + "/", "--token-file", tokenFile, "--state-dir", stateDir,
		}, &out); err != nil {
			t.Fatalf("resumable check: %v", err)
		}
		if _, err := os.Stat(tokenFile); err != nil {
			t.Fatalf("the check consumed the capability file: %v", err)
		}
		return strings.Contains(out.String(), "GUARD_ENROLL_RESUMABLE=true")
	}
	if !resumable("interrupted-capability") {
		t.Fatal("the interrupted capability must resume")
	}
	if resumable("another-capability") {
		t.Fatal("a different capability must not resume the interrupted attempt")
	}
	if err := manager.SetState(hostidentity.StateBound, ""); err != nil {
		t.Fatal(err)
	}
	if resumable("interrupted-capability") {
		t.Fatal("a confirmed binding has nothing left to resume")
	}
}

// A capability the control plane definitively rejects can never be redeemed
// again: `agent enroll` must not leave the file it was handed behind, nor a
// copy in the Guard state.
func TestAgentEnrollRemovesARejectedCapabilityFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	stateDir := t.TempDir()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("kpt1.rejected-capability"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runAgentEnroll(t.Context(), []string{
		"--server", server.URL, "--token-file", tokenFile, "--state-dir", stateDir,
		"--enrollment-file", filepath.Join(t.TempDir(), "agent-enrollment.json"),
	}, &bytes.Buffer{})
	if !errors.Is(err, hostidentity.ErrRecoveryNeeded) {
		t.Fatalf("rejected enrollment = %v, want recovery needed", err)
	}
	if _, statErr := os.Stat(tokenFile); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the rejected capability file was retained: %v", statErr)
	}
	manager, err := hostidentity.Open(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if bootstrap, err := manager.LoadBootstrap(); err != nil || bootstrap != nil {
		t.Fatalf("the rejected capability is still held in Guard state: %v", err)
	}
}
